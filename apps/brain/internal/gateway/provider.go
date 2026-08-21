package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
	FetchAttachment(ctx context.Context, ref string, creds Credentials) ([]byte, error)
}

type Registry map[string]Provider

func NewRegistry(providers ...Provider) (Registry, error) {
	reg := Registry{}
	for _, p := range providers {
		name := p.Name()
		if name == "" {
			return nil, fmt.Errorf("gateway: provider %T has an empty name", p)
		}
		if _, dup := reg[name]; dup {
			return nil, fmt.Errorf("gateway: two providers named %q", name)
		}
		if len(p.Routes()) == 0 {
			return nil, fmt.Errorf("gateway: provider %q declares no routes", name)
		}
		if len(p.Keys()) == 0 {
			return nil, fmt.Errorf("gateway: provider %q declares no secret keys, so it cannot verify anything", name)
		}
		reg[name] = p
	}
	return reg, nil
}

func (r Registry) Get(name string) (Provider, bool) {
	p, ok := r[name]
	return p, ok
}
