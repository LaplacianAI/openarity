package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

const maxBody = 1 << 20

// api is a provider that makes a session on request and hands back its CDP
// address: one call to make it, one to let it go. The address it hands back
// carries the session's own credentials; the key goes on the API calls alone.
type api struct {
	p    Provider
	base string
	// auth puts the key on a request, as the provider wants it.
	auth func(r *http.Request, key string)
	// create is the call that makes a session, and read takes its id and its
	// CDP address from the answer.
	create call
	read   func(body []byte) (id, cdp string, err error)
	// release is the call that ends the session with this id.
	release func(id string) call
	// selfHosted is a Steel of the team's own, which may name its address as
	// localhost: it means the host it was reached at.
	selfHosted bool
}

type call struct {
	method string
	path   string
	body   any
}

type sessions struct {
	api    api
	key    string
	client *http.Client
}

func (s sessions) Open(ctx context.Context) (Session, error) {
	body, err := s.do(ctx, s.api.create)
	if err != nil {
		return Session{}, fmt.Errorf("making a %s session: %w", s.api.p, err)
	}
	id, cdp, err := s.api.read(body)
	if err != nil || id == "" || cdp == "" {
		return Session{}, fmt.Errorf("%s made a session without saying where it is", s.api.p)
	}
	if s.api.selfHosted {
		if cdp, err = reachedAt(cdp, s.api.base); err != nil {
			return Session{}, fmt.Errorf("%s: %w", s.api.p, err)
		}
	}
	release := func(ctx context.Context) error {
		if _, err := s.do(ctx, s.api.release(id)); err != nil {
			return fmt.Errorf("ending the %s session: %w", s.api.p, err)
		}
		return nil
	}
	return Session{URL: cdp, Release: release}, nil
}

func (s sessions) do(ctx context.Context, c call) ([]byte, error) {
	var payload io.Reader
	if c.body != nil {
		raw, err := json.Marshal(c.body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, c.method, s.api.base+c.path, payload)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.key != "" {
		s.api.auth(req, s.key)
	}
	resp, err := s.client.Do(req) //nolint:gosec // G704: the client's dialer decides which addresses may be reached
	if err != nil {
		// The error names the URL, which holds nothing secret: the key is a header.
		return nil, fmt.Errorf("%s could not be reached: %w", s.api.p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s answered %d", s.api.p, resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading what %s sent: %w", s.api.p, err)
	}
	if len(out) > maxBody {
		return nil, fmt.Errorf("%s sent more than 1 MiB", s.api.p)
	}
	return out, nil
}

// reachedAt is cdp at the host base was reached at, when cdp names a
// loopback or unspecified host: a self-hosted Steel says ws://localhost:3000/
// whatever name it is reached by.
func reachedAt(cdp, base string) (string, error) {
	u, err := url.Parse(cdp)
	if err != nil {
		return "", errors.New("the session's address is not a url")
	}
	if !loopback(u.Hostname()) {
		return cdp, nil
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", errors.New("the browser's url is not a url")
	}
	port := u.Port()
	if port == "" {
		port = b.Port()
	}
	u.Host = b.Hostname()
	if port != "" {
		u.Host = net.JoinHostPort(b.Hostname(), port)
	}
	return u.String(), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func header(name string) func(*http.Request, string) {
	return func(r *http.Request, key string) { r.Header.Set(name, key) }
}

func bearer(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) }

func orDefault(base, hosted string) string {
	if base == "" {
		return hosted
	}
	return base
}

// steel is Steel, hosted or self-hosted: POST /v1/sessions, then
// POST /v1/sessions/{id}/release.
func steel(base string) api {
	return api{
		p: Steel, base: orDefault(base, "https://api.steel.dev"), auth: header("steel-api-key"),
		create: call{http.MethodPost, "/v1/sessions", struct{}{}},
		read: func(b []byte) (string, string, error) {
			var r struct {
				ID           string `json:"id"`
				WebsocketURL string `json:"websocketUrl"`
			}
			err := json.Unmarshal(b, &r)
			return r.ID, r.WebsocketURL, err
		},
		release: func(id string) call {
			return call{http.MethodPost, "/v1/sessions/" + url.PathEscape(id) + "/release", struct{}{}}
		},
		selfHosted: base != "",
	}
}

// browserbase makes a session, and ends it by asking for its release: a
// session left alone runs, and is billed, until it times out.
func browserbase(base string) api {
	return api{
		p: Browserbase, base: orDefault(base, "https://api.browserbase.com"), auth: header("X-BB-API-Key"),
		create: call{http.MethodPost, "/v1/sessions", struct{}{}},
		read: func(b []byte) (string, string, error) {
			var r struct {
				ID         string `json:"id"`
				ConnectURL string `json:"connectUrl"`
			}
			err := json.Unmarshal(b, &r)
			return r.ID, r.ConnectURL, err
		},
		release: func(id string) call {
			return call{http.MethodPost, "/v1/sessions/" + url.PathEscape(id), map[string]string{"status": "REQUEST_RELEASE"}}
		},
	}
}

func hyperbrowser(base string) api {
	return api{
		p: Hyperbrowser, base: orDefault(base, "https://api.hyperbrowser.ai"), auth: header("x-api-key"),
		create: call{http.MethodPost, "/api/session", struct{}{}},
		read: func(b []byte) (string, string, error) {
			var r struct {
				ID         string `json:"id"`
				WSEndpoint string `json:"wsEndpoint"`
			}
			err := json.Unmarshal(b, &r)
			return r.ID, r.WSEndpoint, err
		},
		release: func(id string) call { return call{http.MethodPut, "/api/session/" + url.PathEscape(id) + "/stop", nil} },
	}
}

func anchor(base string) api {
	return api{
		p: Anchor, base: orDefault(base, "https://api.anchorbrowser.io"), auth: header("anchor-api-key"),
		create: call{http.MethodPost, "/v1/sessions", struct{}{}},
		read: func(b []byte) (string, string, error) {
			var r struct {
				Data struct {
					ID     string `json:"id"`
					CDPURL string `json:"cdp_url"`
				} `json:"data"`
			}
			err := json.Unmarshal(b, &r)
			return r.Data.ID, r.Data.CDPURL, err
		},
		release: func(id string) call { return call{http.MethodDelete, "/v1/sessions/" + url.PathEscape(id), nil} },
	}
}

// kernelIdle is how long a Kernel browser waits with nothing connected before
// it is removed. A run's model call can outlast Kernel's default minute.
const kernelIdle = 300

// kernel makes a headless browser. Closing the connection does not remove
// it; the delete does.
func kernel(base string) api {
	return api{
		p: Kernel, base: orDefault(base, "https://api.onkernel.com"), auth: bearer,
		create: call{http.MethodPost, "/browsers", map[string]any{"headless": true, "timeout_seconds": kernelIdle}},
		read: func(b []byte) (string, string, error) {
			var r struct {
				SessionID string `json:"session_id"`
				CDPWSURL  string `json:"cdp_ws_url"`
			}
			err := json.Unmarshal(b, &r)
			return r.SessionID, r.CDPWSURL, err
		},
		release: func(id string) call { return call{http.MethodDelete, "/browsers/" + url.PathEscape(id), nil} },
	}
}
