package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	// browserlessCloud is Browserless's own region; a team in another names
	// it as its url.
	browserlessCloud = "wss://production-sfo.browserless.io"
	lightpandaCloud  = "wss://euwest.cloud.lightpanda.io/ws"
	brightDataHost   = "brd.superproxy.io:9222"
)

// fixed is a browser at one address, its key as ?token=: Browserless and
// Lightpanda, hosted or self-hosted, and any CDP browser.
type fixed struct {
	p     Provider
	base  string
	key   string
	cloud string
}

func (f fixed) Open(context.Context) (Session, error) {
	base := f.base
	if base == "" {
		base = f.cloud
	}
	u, err := wsURL(base)
	if err != nil {
		return Session{}, fmt.Errorf("%s: %w", f.p, err)
	}
	if f.key != "" {
		q := u.Query()
		q.Set("token", f.key)
		u.RawQuery = q.Encode()
	}
	return Session{URL: u.String()}, nil
}

// wsURL is raw as a WebSocket address: http becomes ws and https wss, as a
// browser serves both from one port.
func wsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the browser's url is not a url with a host")
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, errors.New("the browser's url must be ws, wss, http or https")
	}
	return u, nil
}

// brightData is Bright Data's Browser API, reached as its zone's user and
// password, which its key holds as USER:PASS.
func brightData(base, key string) (Opener, error) {
	user, pass, ok := strings.Cut(key, ":")
	if !ok || user == "" || pass == "" {
		return nil, errors.New("brightdata's key is the zone's username and password, as USER:PASS")
	}
	u := &url.URL{Scheme: "wss", Host: brightDataHost}
	if base != "" {
		b, err := wsURL(base)
		if err != nil {
			return nil, fmt.Errorf("brightdata: %w", err)
		}
		u = b
	}
	u.User = url.UserPassword(user, pass)
	return fixedURL(u.String()), nil
}

// fixedURL is a browser whose address is already whole.
type fixedURL string

func (f fixedURL) Open(context.Context) (Session, error) { return Session{URL: string(f)}, nil }
