// Package browser is where a run's browser comes from: a team chooses a
// provider in settings, and Open gives the address a Chrome DevTools Protocol
// client connects to, and how to let the browser go. Every provider speaks
// CDP, so they differ only in how that address is found: built from a URL and
// a key, or handed back by a session the provider makes on request.
//
// An address carries its credentials (a token, a session's own key), so
// nothing here logs one or puts one in an error.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

type Provider string

const (
	None Provider = "none"
	// Browserless, Steel and Lightpanda are hosted, or self-hosted at a URL.
	Browserless Provider = "browserless"
	Steel       Provider = "steel"
	Lightpanda  Provider = "lightpanda"
	// CDP is any browser at a URL that speaks the protocol: headless-shell,
	// a Chrome started with --remote-debugging-port, anything else.
	CDP Provider = "cdp"
	// The rest are hosted only.
	Browserbase  Provider = "browserbase"
	Hyperbrowser Provider = "hyperbrowser"
	Anchor       Provider = "anchor"
	Kernel       Provider = "kernel"
	BrightData   Provider = "brightdata"
)

// Providers is every provider a team may choose, none first.
var Providers = []Provider{
	None, Browserless, Steel, Lightpanda, CDP, Browserbase, Hyperbrowser, Anchor, Kernel, BrightData,
}

// Known is whether p is a provider at all.
func (p Provider) Known() bool { return slices.Contains(Providers, p) }

// TakesURL is whether p may run somewhere a team names.
func (p Provider) TakesURL() bool {
	return p == Browserless || p == Steel || p == Lightpanda || p == CDP
}

// NeedsURL is whether p is nowhere without one.
func (p Provider) NeedsURL() bool { return p == CDP }

// NeedsKey is whether p, at url or hosted when url is empty, cannot be
// reached without a key. A self-hosted browser may have none; cdp is always
// at a url.
func (p Provider) NeedsKey(url string) bool {
	return p != None && url == ""
}

// A Session is a browser to drive. URL is its CDP address, credentials and
// all. Release lets it go, and is nil when there is nothing to let go; a
// caller runs it on a context of its own, since the run's may be done.
type Session struct {
	URL     string
	Release func(context.Context) error
}

// An Opener gives a run its browser.
type Opener interface {
	Open(ctx context.Context) (Session, error)
}

// ErrNoProvider is a team that has chosen no browser.
var ErrNoProvider = errors.New("the team has no browser")

// New is the opener for p, at url when it is self-hosted, reached with key.
// client makes a session provider's requests; its dialer decides which
// addresses may be reached.
func New(p Provider, url, key string, client *http.Client) (Opener, error) {
	switch {
	case p == None:
		return nil, ErrNoProvider
	case !p.Known():
		return nil, fmt.Errorf("%q is not a browser provider", p)
	case url != "" && !p.TakesURL():
		return nil, fmt.Errorf("%s is hosted only, and takes no url", p)
	case url == "" && p.NeedsURL():
		return nil, fmt.Errorf("%s needs the url of the browser", p)
	case key == "" && p.NeedsKey(url):
		return nil, fmt.Errorf("%s needs a key", p)
	}
	return open(p, url, key, client)
}

// open is New after its checks, with base standing in for a hosted
// provider's own address, so a test can point one at a server of its own.
func open(p Provider, base, key string, client *http.Client) (Opener, error) {
	switch p {
	case Browserless:
		return fixed{p, base, key, browserlessCloud}, nil
	case Lightpanda:
		return fixed{p, base, key, lightpandaCloud}, nil
	case CDP:
		return fixed{p, base, key, ""}, nil
	case BrightData:
		return brightData(base, key)
	case Steel:
		return sessions{steel(base), key, client}, nil
	case Browserbase:
		return sessions{browserbase(base), key, client}, nil
	case Hyperbrowser:
		return sessions{hyperbrowser(base), key, client}, nil
	case Anchor:
		return sessions{anchor(base), key, client}, nil
	case Kernel:
		return sessions{kernel(base), key, client}, nil
	case None:
		return nil, ErrNoProvider
	}
	return nil, fmt.Errorf("%q is not a browser provider", p)
}
