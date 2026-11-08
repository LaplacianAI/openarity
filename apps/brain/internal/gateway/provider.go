package gateway

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	KeySigning     = "signing_secret"
	KeyVerifyToken = "verify_token"
)

type WebhookRequest struct {
	Suffix     string
	Method     string
	Header     http.Header
	Query      url.Values
	Body       []byte
	ReceivedAt time.Time
}

type Route struct {
	Method string
	Suffix string
}

type Credentials map[string]string

func (c Credentials) Get(key string) string { return c[key] }

type Provider interface {
	Name() string
	Routes() []Route
	Keys() []string
	Verify(req WebhookRequest, creds Credentials) error
	Parse(req WebhookRequest) (Result, error)
}

type Fetcher interface {
	FetchAttachment(
		ctx context.Context, req WebhookRequest, ref string, creds Credentials,
	) ([]byte, error)
}

// Replier sends a run's answer back to the conversation it came from. It is
// beside Provider rather than on it: a provider with nowhere to post back has
// no honest implementation, and a stub returning nil would read as sent.
//
// Reply is called again for the same Outbound when an attempt fails or its
// result is lost, so a provider passes out.ID along for the receiver to drop
// a repeat by.
type Replier interface {
	// Reply is pure, like Parse: no network, no clock.
	Reply(ch Channel, out Outbound, creds Credentials, at time.Time) (OutboundRequest, error)
}

type OutboundRequest struct {
	URL    string
	Header http.Header
	Body   []byte
}

// Outbound is one reply, as a provider needs it.
type Outbound struct {
	// ID is the same on every attempt to send this reply.
	ID string
	// Session is the conversation it goes to, as the provider named it.
	Session Session
	// InReplyTo is the provider's id for the message it answers. Empty when it
	// answers several that came in while the team was working.
	InReplyTo string
	// Kind is "answer", or "apology" when the run failed.
	Kind string
	Text string
}

type Registry struct {
	providers map[string]Provider
	repliers  map[string]Replier
}

func NewRegistry(providers ...Provider) (Registry, error) {
	reg := Registry{providers: map[string]Provider{}, repliers: map[string]Replier{}}
	for _, p := range providers {
		name := p.Name()
		if name == "" {
			return Registry{}, fmt.Errorf("gateway: provider %T has an empty name", p)
		}
		if _, dup := reg.providers[name]; dup {
			return Registry{}, fmt.Errorf("gateway: two providers named %q", name)
		}
		if err := checkRoutes(name, p.Routes()); err != nil {
			return Registry{}, err
		}
		if len(p.Keys()) == 0 {
			return Registry{}, fmt.Errorf("gateway: provider %q declares no secret keys, so it cannot verify anything", name)
		}
		reg.providers[name] = p
		if rp, ok := p.(Replier); ok {
			reg.repliers[name] = rp
		}
	}
	return reg, nil
}

func (r Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Replier is how the named provider sends replies, if it sends them at all.
func (r Registry) Replier(name string) (Replier, bool) {
	rp, ok := r.repliers[name]
	return rp, ok
}

// Names is every registered provider, sorted.
func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.providers))
}

func checkRoutes(name string, routes []Route) error {
	if len(routes) == 0 {
		return fmt.Errorf("gateway: provider %q declares no routes", name)
	}

	seen := make(map[Route]bool, len(routes))
	for _, rt := range routes {
		switch {
		case rt.Method == "":
			return fmt.Errorf("gateway: provider %q declares a route with no method", name)
		case rt.Suffix != "" && !strings.HasPrefix(rt.Suffix, "/"):
			return fmt.Errorf("gateway: provider %q has suffix %q, which must start with /", name, rt.Suffix)
		case strings.HasSuffix(rt.Suffix, "/"):
			return fmt.Errorf("gateway: provider %q has suffix %q, which must not end with /", name, rt.Suffix)
		case seen[rt]:
			return fmt.Errorf("gateway: provider %q declares %s %q twice", name, rt.Method, rt.Suffix)
		}
		seen[rt] = true
	}
	return nil
}
