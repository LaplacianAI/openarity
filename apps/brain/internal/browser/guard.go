package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

// Public refuses a host unless every address it names is public.
type Public func(ctx context.Context, host string) error

// ErrNoGuard is a browser that will not pause its requests, so nothing stops
// a page it opens from reaching a private address.
var ErrNoGuard = errors.New("the browser does not let its requests be checked, so it is not used")

const (
	// judges is how many paused requests are judged at once, and queued how
	// many may wait: a page asks for dozens, each judged by a lookup. One past
	// the queue is refused, not waited for, as the browser's event loop
	// cannot be held.
	judges = 16
	queued = 1024
	// maxJudged is how many hosts' verdicts a session keeps. Past it a host
	// is judged on every request, never let through unjudged.
	maxJudged = 4096
	// maxBlocked is how many refused requests are named, each cut to
	// maxBlockedURL bytes; the rest are only counted. A page chooses them.
	maxBlocked    = 100
	maxBlockedURL = 200
	// canaryHost is refused whatever public says. A self-hosted browser is
	// sent to it at start, and asked to fetch it from the page: one that loads
	// either is not pausing its requests, whatever it answered Fetch.enable.
	canaryHost = "oa-canary.invalid"
	// judgeTimeout is how long one host is looked up. A lookup that takes
	// longer is a refusal: a page's own name server cannot hold a judge.
	judgeTimeout = 5 * time.Second
	// canaryWait is how long the page's fetch of the canary may take to be
	// paused before the browser is taken not to pause it.
	canaryWait = 5 * time.Second
)

// containJS runs before a page's own scripts in every frame. Fetch pauses no
// socket of any kind, and no script of ours reaches into a worker to lock its
// own: so a page may open no socket and start no worker. A popup's requests
// are paused by the browser-wide guard, but it is a tab the read does not
// follow, so a page may open none, and a link or form that targets another
// window opens in this one.
const containJS = `(() => {
  const lock = (o, k, v) => { try { Object.defineProperty(o, k, {value: v, writable: false, configurable: false}); } catch (e) {} };
  for (const k of ["WebSocket", "WebSocketStream", "WebTransport", "RTCPeerConnection", "webkitRTCPeerConnection", "Worker", "SharedWorker"]) {
    lock(window, k, function () { throw new Error(k + " is not allowed in this browser"); });
  }
  try { lock(ServiceWorkerContainer.prototype, "register", function () { return Promise.reject(new Error("service workers are not allowed in this browser")); }); } catch (e) {}
  lock(window, "open", function () { return null; });
  const self = (el) => { if (el) { el.setAttribute("target", "_self"); el.removeAttribute("formtarget"); } };
  const keep = (e) => {
    const t = e.target;
    if (!t || !t.closest) return;
    self(t.closest("a, area, form"));
    self(t.form);
    self(e.submitter);
    if (t.hasAttribute && t.hasAttribute("formtarget")) t.removeAttribute("formtarget");
  };
  window.addEventListener("click", keep, true);
  window.addEventListener("submit", keep, true);
  for (const k of ["submit", "requestSubmit"]) {
    const own = HTMLFormElement.prototype[k];
    lock(HTMLFormElement.prototype, k, function (...a) { self(this); if (a[0]) self(a[0]); return own.apply(this, a); });
  }
})();`

type paused struct {
	id  fetch.RequestID
	url string
	// browser is a request the browser-wide guard paused: one from outside
	// the tab, a popup's or a worker's, or the tab's own a second time. It is
	// answered there, and not named in the tab's read, as on a browser runs
	// share it may be another run's.
	browser bool
}

// guard pauses every request the tab makes and lets through only those
// allowed accepts.
type guard struct {
	tab    context.Context
	public Public
	queue  chan paused
	// unguarded is a hosted browser that would not pause its requests, used
	// all the same: the read says so. uncontained is one that would not take
	// containJS, so a page may open a socket or a popup: the read says that.
	unguarded   bool
	uncontained bool

	mu      sync.Mutex
	allowed map[string]bool
	blocked []string
	refused int
	// shed is the requests refused because more were waiting than the queue
	// holds, not for where they went.
	shed int
	// canaried is closed when the page's fetch of the canary is paused.
	canaried chan struct{}
	canaryed sync.Once
}

// startGuard starts judging tab's requests and keeps the page in its tab.
// When the browser refuses to pause them, required says whether that is the
// end of it: a browser of the team's own sits on the team's network, a hosted
// one on its provider's. A browser that does not answer is a failure, never a
// refusal: a hosted one must not be used unguarded because it was slow.
//
// A browser of the team's own is guarded browser-wide too, behind the tab:
// a popup or a worker makes requests the tab's guard never sees, and on a
// browser runs share, the rule judged is the same whichever run's guard
// pauses a request.
func startGuard(tab context.Context, public Public, required bool) (*guard, error) {
	g := &guard{
		tab: tab, public: public, queue: make(chan paused, queued), allowed: map[string]bool{},
		canaried: make(chan struct{}),
	}
	if err := chromedp.Listen(tab, g.pause(false)); err != nil {
		return nil, err
	}
	for range judges {
		go g.judge()
	}
	if _, err := chromedp.Call(tab, fetch.Enable, fetch.EnableParams{}); err != nil {
		switch {
		case tab.Err() != nil:
			return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
		case required:
			return nil, ErrNoGuard
		}
		g.unguarded = true
		return g, nil
	}
	if err := contain(tab); err != nil {
		switch {
		case tab.Err() != nil:
			return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
		case required:
			return nil, ErrNoGuard
		}
		g.uncontained = true
	}
	if required {
		if err := chromedp.ListenBrowser(tab, g.pause(true)); err != nil {
			return nil, err
		}
		if _, err := chromedp.CallBrowser(tab, fetch.Enable, fetch.EnableParams{}); err != nil {
			if tab.Err() != nil {
				return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
			}
			return nil, ErrNoGuard
		}
		if err := g.canary(); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// contain keeps the page in its tab: containJS in every document.
func contain(tab context.Context) error {
	_, err := chromedp.Call(tab, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: containJS})
	return err
}

// canary is the guard proved: a browser that said it pauses its requests and
// loads the canary anyway does not, nor does one whose page fetches it
// without the fetch being paused. A browser may pause the one and not the
// other.
func (g *guard) canary() error {
	err := chromedp.Do(g.tab, chromedp.Navigate("http://"+canaryHost+"/"))
	switch {
	case err != nil && strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT"):
	case g.tab.Err() != nil:
		return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
	default:
		return ErrNoGuard
	}
	// The error page's own events can still be queued, and the next Navigate
	// would take its load for the page it asked for: this round trip lets
	// them arrive first, as CDP keeps its order.
	if _, err := chromedp.Run(g.tab, chromedp.Evaluate[any](`fetch("http://`+canaryHost+`/sub").catch(() => {}), 0`)); err != nil {
		if g.tab.Err() != nil {
			return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
		}
		return ErrNoGuard
	}
	select {
	case <-g.canaried:
		return nil
	case <-g.tab.Done():
		return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
	case <-time.After(canaryWait):
		return ErrNoGuard
	}
}

// pause runs on chromedp's event loop, which a command sent from it would
// block: a request is queued for a judge. One past the queue is left paused,
// counted and never let through: a page that asks for more than a thousand at
// once waits on them, and a goroutine for each would let it make as many.
func (g *guard) pause(browser bool) func(ev any) {
	return func(ev any) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		select {
		case g.queue <- paused{e.RequestID, e.Request.URL, browser}:
		default:
			g.mu.Lock()
			g.shed++
			g.mu.Unlock()
		}
	}
}

func (g *guard) judge() {
	for {
		select {
		case <-g.tab.Done():
			return
		case p := <-g.queue:
			if err := g.allows(p.url); err != nil {
				g.refuse(p)
				continue
			}
			// A request the browser would not let go on is failed, never
			// left for the page to wait on.
			if _, err := send(g.tab, p.browser, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: p.id}); err != nil {
				g.fail(p)
			}
		}
	}
}

func (g *guard) refuse(p paused) {
	// The canary is the brain's own request, whichever guard paused it and
	// however late: never the page's to report.
	if host, _ := hostOf(p.url); host == canaryHost {
		g.canaryed.Do(func() { close(g.canaried) })
		g.fail(p)
		return
	}
	if !p.browser {
		g.mu.Lock()
		g.refused++
		if len(g.blocked) < maxBlocked {
			g.blocked = append(g.blocked, clip(p.url, maxBlockedURL))
		}
		g.mu.Unlock()
	}
	g.fail(p)
}

func (g *guard) fail(p paused) {
	_, _ = send(g.tab, p.browser, fetch.FailRequest, fetch.FailRequestParams{
		RequestID: p.id, ErrorReason: network.ErrorReasonBlockedByClient,
	})
}

// send sends a command to the tab, or to the browser for a request the
// browser-wide guard paused.
func send[P, R any](tab context.Context, browser bool, cmd cdp.Command[P, R], params P) (R, error) {
	if browser {
		return chromedp.CallBrowser(tab, cmd, params)
	}
	return chromedp.Call(tab, cmd, params)
}

// allows is whether the request may go on. Only a host let through is
// remembered: a refusal may be a lookup that failed for a moment.
func (g *guard) allows(raw string) error {
	host, err := hostOf(raw)
	if err != nil || host == "" {
		return err
	}
	if host == canaryHost {
		return errors.New("the canary is never reached")
	}
	g.mu.Lock()
	ok := g.allowed[host]
	g.mu.Unlock()
	if ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(g.tab, judgeTimeout)
	defer cancel()
	if err := g.public(ctx, host); err != nil {
		return err
	}
	g.mu.Lock()
	if len(g.allowed) < maxJudged {
		g.allowed[host] = true
	}
	g.mu.Unlock()
	return nil
}

// Blocked is the requests refused since the last call, at most maxBlocked
// named, how many there were, and how many were held back as one too many;
// it forgets them.
func (g *guard) Blocked() ([]string, int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, n, shed := g.blocked, g.refused, g.shed
	g.blocked, g.refused, g.shed = nil, 0, 0
	return b, n, shed
}

// hostOf is the host a request reaches, or "" for one that reaches none: a
// page's own data or blob. Anything else is refused, and so is an address a
// browser and Go might read differently: a user name. A backslash in the host
// or the user name, which a browser reads as a slash, url.Parse refuses itself.
func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not an address")
	}
	switch strings.ToLower(u.Scheme) {
	case "data", "blob", "about":
		return "", nil
	case "http", "https", "ws", "wss":
	default:
		return "", fmt.Errorf("%s is not a web address", u.Scheme)
	}
	host := u.Hostname()
	switch {
	case u.User != nil:
		return "", errors.New("an address with a user name")
	case host == "":
		return "", errors.New("no host")
	case webfetch.NumericHost(host):
		return "", fmt.Errorf("%s is a number but not an IP address", host)
	}
	return host, nil
}

// clip is s cut to at most n bytes, on a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
