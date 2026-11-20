package browser

import (
	"context"
	"crypto/rand"
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
	"github.com/chromedp/cdproto/target"
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
	// wideJudges and wideQueued are the browser-wide guard's own: on a
	// browser runs share it sees every run's requests, and the tab's guard
	// judges this run's too, so it is small, apart, and never holds the tab's
	// judges. failers fail what neither queue can take, up to failQueued;
	// past that a request stays paused, never let through.
	wideJudges = 4
	wideQueued = 64
	failers    = 2
	failQueued = 4096
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
	// maxPopups is how many popups may wait to be closed at once; past it
	// the tab is said to be uncontained.
	maxPopups = 64
)

// canaryWait is how long each of the canary's requests may take to be paused
// before the browser is taken not to pause it; a test shortens it.
var canaryWait = 5 * time.Second

// containJS runs before a page's own scripts in every frame. Fetch pauses no
// socket of any kind, and no script of ours reaches into a worker to lock its
// own: so a page may open no socket and start no worker. A popup gets no
// script of ours, so a page may open none, a link or form that targets
// another window opens in this one, and one that opens all the same is closed
// (closePopups).
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
	// queue is the tab's requests, wide the browser-wide guard's, and failQ
	// and wideFail what each could not take: apart, so a flood of other runs'
	// requests never leaves the tab's own waiting to be failed.
	queue, wide, failQ, wideFail chan paused
	// unguarded is a hosted browser that would not pause its requests, used
	// all the same: the read says so. uncontained is one that would not take
	// containJS, so a page may open a socket or a popup: the read says that.
	unguarded   bool
	uncontained bool

	mu      sync.Mutex
	allowed map[string]bool
	blocked []string
	refused int
	// shed is the tab's requests refused because more were waiting than the
	// queue holds, not for where they went.
	shed int
	// watch is the canary's requests in flight, by path: each is closed and
	// dropped when that very request is paused, by either of this guard's
	// sessions. Another run's canary, or this one's other request, is not it.
	watch map[string]chan struct{}
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
		tab: tab, public: public, allowed: map[string]bool{}, watch: map[string]chan struct{}{},
		queue: make(chan paused, queued), wide: make(chan paused, wideQueued),
		failQ: make(chan paused, failQueued), wideFail: make(chan paused, failQueued),
	}
	if err := chromedp.Listen(tab, g.pause(false)); err != nil {
		return nil, err
	}
	for range judges {
		go g.judge(g.queue)
	}
	for range failers {
		go g.failer()
	}
	if _, err := chromedp.Call(tab, fetch.Enable, fetch.EnableParams{}); err != nil {
		switch {
		case tab.Err() != nil:
			return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
		case required:
			return nil, ErrNoGuard
		}
		// Used all the same, and still kept in its tab if it will be.
		g.unguarded = true
	}
	if err := contain(tab); err != nil {
		switch {
		case tab.Err() != nil:
			return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
		case required:
			return nil, ErrNoGuard
		}
		g.mu.Lock()
		g.uncontained = true
		g.mu.Unlock()
	}
	if required {
		for range wideJudges {
			go g.judge(g.wide)
		}
		if err := chromedp.ListenBrowser(tab, g.pause(true)); err != nil {
			return nil, err
		}
		if _, err := chromedp.CallBrowser(tab, fetch.Enable, fetch.EnableParams{}); err != nil {
			if tab.Err() != nil {
				return nil, fmt.Errorf("the browser did not answer: %w", tab.Err())
			}
			return nil, ErrNoGuard
		}
		if err := g.closePopups(); err != nil {
			return nil, err
		}
		if err := g.canary(); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// closePopups closes every other page the tab's browser context, or the tab
// itself, opens: a popup is a tab of its own, with none of containJS, so a
// socket it opened would be checked by nothing. Between its opening and its
// close it may run; its requests are paused browser-wide all the while.
func (g *guard) closePopups() error {
	c := chromedp.FromContext(g.tab)
	if c == nil || c.Target == nil {
		return errors.New("the tab has no target to keep popups from")
	}
	w := &popups{
		g: g, ours: c.Target.TargetID, own: c.BrowserContextID,
		closed: map[target.ID]bool{}, closing: make(chan target.ID, maxPopups),
	}
	if err := chromedp.ListenBrowser(g.tab, w.event); err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-g.tab.Done():
				return
			case id := <-w.closing:
				if _, err := chromedp.CallBrowser(g.tab, target.CloseTarget, target.CloseTargetParams{TargetID: id}); err != nil && g.tab.Err() == nil {
					w.notClosed(id)
				}
			}
		}
	}()
	_, err := chromedp.CallBrowser(g.tab, target.SetDiscoverTargets, target.SetDiscoverTargetsParams{Discover: true})
	if err != nil && g.tab.Err() != nil {
		return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
	}
	return err
}

// popups is what closePopups watches: the tab, ours, its browser context,
// own, and every popup on its way to being closed.
type popups struct {
	g       *guard
	ours    target.ID
	own     cdp.BrowserContextID
	mu      sync.Mutex
	closed  map[target.ID]bool
	closing chan target.ID
}

// event runs on chromedp's event loop: it hands each popup to be closed,
// once, and forgets it when it is gone.
func (w *popups) event(ev any) {
	var info *target.Info
	switch e := ev.(type) {
	case *target.EventTargetCreated:
		info = e.TargetInfo
	case *target.EventTargetInfoChanged:
		info = e.TargetInfo
	case *target.EventTargetDestroyed:
		// Gone: forgotten, so what is kept lasts no longer than the popups.
		w.mu.Lock()
		delete(w.closed, e.TargetID)
		w.mu.Unlock()
		return
	default:
		return
	}
	if info == nil || info.Type != "page" || info.TargetID == w.ours ||
		(info.OpenerID != w.ours && (w.own == "" || info.BrowserContextID != w.own)) {
		return
	}
	w.mu.Lock()
	seen := w.closed[info.TargetID]
	w.closed[info.TargetID] = true
	w.mu.Unlock()
	if seen {
		return
	}
	select {
	case w.closing <- info.TargetID:
	default:
		// More popups than are closed at once.
		w.unclosed(info.TargetID)
	}
}

// notClosed is a popup whose close failed. One already gone failed only
// because it was: chromedp runs a target's destroyed event before the reply
// the browser sent after it, so it is no longer kept, and nothing escaped.
func (w *popups) notClosed(id target.ID) {
	w.mu.Lock()
	kept := w.closed[id]
	w.mu.Unlock()
	if kept {
		w.unclosed(id)
	}
}

// unclosed is a popup that could not be closed now: forgotten, so its next
// event tries again, and the read says the page is not kept in its tab.
func (w *popups) unclosed(id target.ID) {
	w.mu.Lock()
	delete(w.closed, id)
	w.mu.Unlock()
	w.g.mu.Lock()
	w.g.uncontained = true
	w.g.mu.Unlock()
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
	nav, sub := "/nav-"+rand.Text(), "/sub-"+rand.Text()
	navPaused := g.watching(nav)
	err := chromedp.Do(g.tab, chromedp.Navigate("http://"+canaryHost+nav))
	switch {
	case err != nil && strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT"):
	case g.tab.Err() != nil:
		return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
	default:
		return ErrNoGuard
	}
	if err := g.await(navPaused); err != nil {
		return err
	}
	// The error page's own events can still be queued, and the next Navigate
	// would take its load for the page it asked for: this round trip lets
	// them arrive first, as CDP keeps its order.
	subPaused := g.watching(sub)
	if _, err := chromedp.Run(g.tab, chromedp.Evaluate[any](`fetch("http://`+canaryHost+sub+`").catch(() => {}), 0`)); err != nil {
		if g.tab.Err() != nil {
			return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
		}
		return ErrNoGuard
	}
	return g.await(subPaused)
}

// watching is a channel closed when the canary's request for path is paused.
func (g *guard) watching(path string) chan struct{} {
	ch := make(chan struct{})
	g.mu.Lock()
	g.watch[path] = ch
	g.mu.Unlock()
	return ch
}

// seen closes path's channel, once: the tab's guard and the browser-wide one
// may both pause the same request.
func (g *guard) seen(path string) {
	g.mu.Lock()
	ch, ok := g.watch[path]
	delete(g.watch, path)
	g.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (g *guard) await(paused chan struct{}) error {
	select {
	case <-paused:
		return nil
	case <-g.tab.Done():
		return fmt.Errorf("the browser did not answer: %w", g.tab.Err())
	case <-time.After(canaryWait):
		return ErrNoGuard
	}
}

// pause runs on chromedp's event loop, which a command sent from it would
// block: a request is queued for a judge, the tab's or the browser-wide
// guard's. One past its queue is failed by a failer, never let through, and
// counted when it is the tab's: a browser-wide one may be another run's. Past
// the failers' queue too it stays paused, which holds the page but lets
// nothing out: a goroutine for each would let a page make as many as it liked.
func (g *guard) pause(browser bool) func(ev any) {
	q, failQ := g.queue, g.failQ
	if browser {
		q, failQ = g.wide, g.wideFail
	}
	return func(ev any) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		p := paused{e.RequestID, e.Request.URL, browser}
		select {
		case q <- p:
			return
		default:
		}
		if !browser {
			g.mu.Lock()
			g.shed++
			g.mu.Unlock()
		}
		select {
		case failQ <- p:
		default:
		}
	}
}

// failer fails what no judge could take, the tab's first. The canary is
// noticed here too: one held back under load is still this guard's.
func (g *guard) failer() {
	for {
		p, ok := g.nextToFail()
		if !ok {
			return
		}
		g.noticeCanary(p)
		g.fail(p)
	}
}

// nextToFail is the tab's own when one waits, else whichever comes first;
// false once the tab is done.
func (g *guard) nextToFail() (paused, bool) {
	// First: once the tab is done, a select below may still pick a ready
	// queue over Done.
	if g.tab.Err() != nil {
		return paused{}, false
	}
	select {
	case <-g.tab.Done():
		return paused{}, false
	case p := <-g.failQ:
		return p, true
	default:
	}
	select {
	case <-g.tab.Done():
		return paused{}, false
	case p := <-g.failQ:
		return p, true
	case p := <-g.wideFail:
		return p, true
	}
}

// noticeCanary says the canary's request was paused, when p is one of its
// own, and whether p is the canary host's at all.
func (g *guard) noticeCanary(p paused) bool {
	u, err := url.Parse(p.url)
	if err != nil || u.Hostname() != canaryHost {
		return false
	}
	g.seen(u.Path)
	return true
}

func (g *guard) judge(q chan paused) {
	for {
		select {
		case <-g.tab.Done():
			return
		case p := <-q:
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
	// however late: never the page's to report. Only this guard's own, by
	// its path, says the guard works.
	if g.noticeCanary(p) {
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
