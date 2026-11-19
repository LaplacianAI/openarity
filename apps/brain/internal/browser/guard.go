package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

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
	// sent to it at start, and one that loads it is not pausing its requests,
	// whatever it answered Fetch.enable.
	canaryHost = "oa-canary.invalid"
)

// containJS runs before a page's own scripts in every frame. Fetch pauses no
// websocket, and a popup is a tab of its own the guard does not watch: so a
// page may open neither, and a link or form that targets another window
// opens in this one.
const containJS = `(() => {
  const lock = (o, k, v) => { try { Object.defineProperty(o, k, {value: v, writable: false, configurable: false}); } catch (e) {} };
  lock(window, "WebSocket", function () { throw new Error("websockets are not allowed in this browser"); });
  lock(window, "open", function () { return null; });
  const keep = (e) => {
    const t = e.target;
    const a = t && t.closest && t.closest("a[target], area[target], form[target]");
    if (a) a.target = "_self";
    if (t && t.form && t.form.target) t.form.target = "_self";
  };
  window.addEventListener("click", keep, true);
  window.addEventListener("submit", keep, true);
})();`

type paused struct {
	id  fetch.RequestID
	url string
}

// guard pauses every request the tab makes and lets through only those
// allowed accepts.
type guard struct {
	tab    context.Context
	public Public
	queue  chan paused
	// unguarded is a hosted browser that would not pause its requests, used
	// all the same: the read says so.
	unguarded bool

	mu      sync.Mutex
	allowed map[string]bool
	blocked []string
	refused int
}

// startGuard starts judging tab's requests and keeps the page in its tab.
// When the browser refuses to pause them, required says whether that is the
// end of it: a browser of the team's own sits on the team's network, a hosted
// one on its provider's. A browser that does not answer is a failure, never a
// refusal: a hosted one must not be used unguarded because it was slow.
func startGuard(tab context.Context, public Public, required bool) (*guard, error) {
	g := &guard{tab: tab, public: public, queue: make(chan paused, queued), allowed: map[string]bool{}}
	if err := chromedp.Listen(tab, g.pause); err != nil {
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
	}
	if required {
		if err := canary(tab); err != nil {
			return nil, err
		}
		// The canary is the brain's own request, not the page's to report.
		g.Blocked()
	}
	return g, nil
}

// contain keeps the page in its tab: containJS in every document.
func contain(tab context.Context) error {
	_, err := chromedp.Call(tab, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: containJS})
	return err
}

// canary is the guard proved: a browser that said it pauses its requests and
// loads the canary anyway does not.
func canary(tab context.Context) error {
	err := chromedp.Do(tab, chromedp.Navigate("http://"+canaryHost+"/"))
	switch {
	case err != nil && strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT"):
		// The error page's own events can still be queued, and the next
		// Navigate would take its load for the page it asked for: a round
		// trip lets them arrive first, as CDP keeps its order.
		_, _ = chromedp.Run(tab, chromedp.Evaluate[any]("0"))
		return nil
	case tab.Err() != nil:
		return fmt.Errorf("the browser did not answer: %w", tab.Err())
	}
	return ErrNoGuard
}

// pause runs on chromedp's event loop, which a command sent from it would
// block: a request is queued for a judge, or refused when the queue is full.
func (g *guard) pause(ev any) {
	e, ok := ev.(*fetch.EventRequestPaused)
	if !ok {
		return
	}
	p := paused{e.RequestID, e.Request.URL}
	select {
	case g.queue <- p:
	default:
		go g.refuse(p)
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
			_, _ = chromedp.Call(g.tab, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: p.id})
		}
	}
}

func (g *guard) refuse(p paused) {
	g.mu.Lock()
	g.refused++
	if len(g.blocked) < maxBlocked {
		g.blocked = append(g.blocked, clip(p.url, maxBlockedURL))
	}
	g.mu.Unlock()
	_, _ = chromedp.Call(g.tab, fetch.FailRequest, fetch.FailRequestParams{
		RequestID: p.id, ErrorReason: network.ErrorReasonBlockedByClient,
	})
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
	if err := g.public(g.tab, host); err != nil {
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
// named, and how many there were; it forgets them.
func (g *guard) Blocked() ([]string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, n := g.blocked, g.refused
	g.blocked, g.refused = nil, 0
	return b, n
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
