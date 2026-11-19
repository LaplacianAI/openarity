package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

// Public refuses a host unless every address it names is public.
type Public func(ctx context.Context, host string) error

// ErrNoGuard is a browser that will not pause its requests, so nothing stops
// a page it opens from reaching a private address.
var ErrNoGuard = errors.New("the browser does not let its requests be checked, so it is not used")

// maxJudging is how many paused requests are judged at once; a page asks for
// dozens, each judged by a lookup.
const maxJudging = 16

// guard pauses every request the tab makes and lets through only those
// allowed accepts. A host is judged once a session: a page asks for one
// many times.
type guard struct {
	tab    context.Context
	public Public
	slots  chan struct{}

	mu      sync.Mutex
	judged  map[string]error
	blocked []string
}

// startGuard starts judging tab's requests. When the browser refuses to pause
// them, required says whether that is the end of it: a browser of the team's
// own sits on the team's network, a hosted one on its provider's.
func startGuard(tab context.Context, public Public, required bool) (*guard, error) {
	g := &guard{tab: tab, public: public, slots: make(chan struct{}, maxJudging), judged: map[string]error{}}
	if err := chromedp.Listen(tab, g.paused); err != nil {
		return nil, err
	}
	if _, err := chromedp.Call(tab, fetch.Enable, fetch.EnableParams{}); err != nil {
		if required {
			return nil, ErrNoGuard
		}
		return g, nil
	}
	return g, nil
}

// paused runs on chromedp's event loop, which a command sent from it would
// block, so each request is judged on a goroutine of its own.
func (g *guard) paused(ev any) {
	e, ok := ev.(*fetch.EventRequestPaused)
	if !ok {
		return
	}
	go func() {
		select {
		case g.slots <- struct{}{}:
		case <-g.tab.Done():
			return
		}
		defer func() { <-g.slots }()
		g.decide(e.RequestID, e.Request.URL)
	}()
}

func (g *guard) decide(id fetch.RequestID, raw string) {
	if err := g.allowed(raw); err != nil {
		g.mu.Lock()
		g.blocked = append(g.blocked, raw)
		g.mu.Unlock()
		_, _ = chromedp.Call(g.tab, fetch.FailRequest, fetch.FailRequestParams{
			RequestID: id, ErrorReason: network.ErrorReasonBlockedByClient,
		})
		return
	}
	_, _ = chromedp.Call(g.tab, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: id})
}

func (g *guard) allowed(raw string) error {
	host, err := hostOf(raw)
	if err != nil || host == "" {
		return err
	}
	g.mu.Lock()
	verdict, seen := g.judged[host]
	g.mu.Unlock()
	if seen {
		return verdict
	}
	verdict = g.public(g.tab, host)
	g.mu.Lock()
	g.judged[host] = verdict
	g.mu.Unlock()
	return verdict
}

// Blocked is every request refused since the last call, which it forgets.
func (g *guard) Blocked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.blocked
	g.blocked = nil
	return b
}

// hostOf is the host a request reaches, or "" for one that reaches none: a
// page's own data or blob. Anything else is refused.
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
	case host == "":
		return "", errors.New("no host")
	case webfetch.NumericHost(host):
		return "", fmt.Errorf("%s is a number but not an IP address", host)
	}
	return host, nil
}
