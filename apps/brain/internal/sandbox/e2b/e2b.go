// Package e2b makes rooms in E2B (e2b.dev), one sandbox per room.
//
// Four calls are used, each from E2B's own sources:
//
//   - POST /v2/sandboxes on the control plane (api.<domain>), authenticated
//     with X-API-Key: create a sandbox from a template.
//     (infra repo, spec/openapi.yml)
//   - GET /sandboxes/{id}: read the sandbox's envd access token again.
//   - DELETE /sandboxes/{id}: kill it.
//   - process.Process/Start on envd, the daemon inside the sandbox at
//     https://49983-<id>.<domain>, over the Connect protocol with JSON
//     messages. (E2B repo, spec/envd/process/process.proto)
//
// A sandbox's ports are public at https://<port>-<id>.<domain>. A room is made
// with allowPublicTraffic false, so a request to them needs the sandbox's
// traffic access token in the e2b-traffic-access-token header
// (https://docs.e2b.dev/network/restrict-public-access).
package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

const (
	defaultDomain = "e2b.app"
	// envdPort is where envd listens in every sandbox.
	envdPort = 49983
	// defaultTimeout is how long a sandbox lives if nobody deletes it, so a
	// brain that dies leaves nothing running for ever.
	defaultTimeout = time.Hour
	// maxMessage cuts E2B's own error text; it is shown to people, and a
	// proxy's HTML page has no business in a log line.
	maxMessage = 200
)

// A Client makes rooms with one team's API key. It holds no state about
// rooms: everything a later call needs is in the room string or asked of E2B.
type Client struct {
	apiKey string
	http   *http.Client

	// Domain is E2B's, or a self-hosted E2B's, base domain.
	Domain string
	// APIURL is the control plane, https://api.<Domain> unless set.
	APIURL string
	// Timeout is a sandbox's lifetime before E2B kills it itself.
	Timeout time.Duration
	// Resolve turns a host into IPv4 addresses, for egress to a port E2B
	// cannot filter by name. net's resolver unless set.
	Resolve func(ctx context.Context, host string) ([]string, error)
}

var _ sandbox.Rooms = (*Client)(nil)

// New returns a client for one API key. client may be nil.
func New(apiKey string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		apiKey:  apiKey,
		http:    client,
		Domain:  defaultDomain,
		Timeout: defaultTimeout,
		Resolve: lookupIPv4,
	}
}

func (c *Client) apiURL() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return "https://api." + c.Domain
}

type createRequest struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	EnvVars    map[string]string `json:"envVars,omitempty"`
	// Secure makes envd, the daemon in the sandbox, want its access token
	// on every call, from inside the sandbox as from outside: the room's
	// environment does not hold it, so a command cannot start processes or
	// list theirs. Sent rather than left to the default.
	Secure  bool    `json:"secure"`
	Network network `json:"network"`
}

type created struct {
	SandboxID          string `json:"sandboxID"`
	EnvdAccessToken    string `json:"envdAccessToken"`
	TrafficAccessToken string `json:"trafficAccessToken"`
	Domain             string `json:"domain"`
}

// Create makes a sandbox from the template in s.Image. Egress is mapped as
// egress.go describes. A spec E2B cannot honour is ErrRefused.
func (c *Client) Create(ctx context.Context, s sandbox.Spec) (string, error) {
	if s.Image == "" {
		return "", fmt.Errorf("%w: no E2B template is set", sandbox.ErrRefused)
	}
	n, err := c.network(ctx, s)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(createRequest{
		TemplateID: s.Image,
		Timeout:    int(c.Timeout / time.Second),
		Metadata:   s.Labels,
		EnvVars:    s.Env,
		Secure:     true,
		Network:    n,
	})
	if err != nil {
		return "", fmt.Errorf("e2b: encode the create request: %w", err)
	}

	var out created
	status, err := c.api(ctx, http.MethodPost, "/v2/sandboxes", body, &out, secretsIn(s.Env, c.apiKey))
	if err != nil {
		if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusForbidden {
			// E2B looked at the template, the network rules or the plan and
			// said no; nothing is left behind. A missing template is not a
			// missing room, so ErrRefused replaces ErrNotFound here.
			return "", fmt.Errorf("%w: %s", sandbox.ErrRefused, strings.TrimPrefix(err.Error(), sandbox.ErrNotFound.Error()+" "))
		}
		return "", err
	}
	if out.SandboxID == "" {
		return "", errors.New("e2b: create returned no sandbox id")
	}
	domain := out.Domain
	if domain == "" {
		domain = c.Domain
	}
	return encodeRoom(room{ID: out.SandboxID, Traffic: out.TrafficAccessToken, Domain: domain}), nil
}

// Start runs argv in the room through envd and returns once it has started.
// The command runs under a login shell, as E2B's own SDKs run commands, so it
// finds what the template put on PATH.
func (c *Client) Start(ctx context.Context, roomID string, argv []string) error {
	r, err := decodeRoom(roomID)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("e2b: nothing to start")
	}
	// The envd token is asked for rather than carried in the room: it is
	// only needed here, and a room that is stored should hold as little as
	// it can. The same call tells a room that is gone.
	var detail struct {
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if _, err := c.api(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(r.ID), nil, &detail, []string{c.apiKey}); err != nil {
		return err
	}
	if detail.EnvdAccessToken == "" {
		// A secure sandbox always has one; one without is not the room made.
		return errors.New("e2b: the sandbox has no envd access token, so it was not made secure")
	}
	return c.startProcess(ctx, r, detail.EnvdAccessToken, argv)
}

// URL is https://<port>-<id>.<domain>, with the traffic token the room was
// made with.
func (c *Client) URL(_ context.Context, roomID string, port int) (sandbox.Endpoint, error) {
	r, err := decodeRoom(roomID)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	if port < 1 || port > 65535 {
		return sandbox.Endpoint{}, fmt.Errorf("e2b: %d is not a port", port)
	}
	e := sandbox.Endpoint{URL: "https://" + host(r, port)}
	if r.Traffic != "" {
		e.Header = http.Header{"E2b-Traffic-Access-Token": {r.Traffic}}
	}
	return e, nil
}

// Delete kills the sandbox. One already gone is not an error.
func (c *Client) Delete(ctx context.Context, roomID string) error {
	r, err := decodeRoom(roomID)
	if err != nil {
		return err
	}
	_, err = c.api(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(r.ID), nil, nil, []string{c.apiKey})
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

func host(r room, port int) string {
	return strconv.Itoa(port) + "-" + r.ID + "." + r.Domain
}

// api calls the control plane. It returns the status with any error, so a
// caller can tell a refusal from an outage. secrets are scrubbed from what E2B
// says back before it goes in an error.
func (c *Client) api(ctx context.Context, method, path string, body []byte, out any, secrets []string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL()+path, bytes.NewReader(body)) //nolint:gosec // G704: the host is E2B's control plane, from the team's own domain setting
	if err != nil {
		return 0, fmt.Errorf("e2b: build the %s request: %w", method, err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req) //nolint:gosec // G704: as above
	if err != nil {
		return 0, fmt.Errorf("e2b: %s %s: %w", method, path, scrubErr(err, secrets))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("e2b: read the %s %s response: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, statusError(method, path, resp.StatusCode, raw, secrets)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("e2b: %s %s returned something that is not JSON", method, path)
		}
	}
	return resp.StatusCode, nil
}

// statusError is E2B's own message, cut short and scrubbed, around the status.
// A 404 is also sandbox.ErrNotFound.
func statusError(method, path string, status int, raw []byte, secrets []string) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := cut(scrub(e.Message, secrets))
	text := fmt.Sprintf("e2b: %s %s: %d", method, path, status)
	if msg != "" {
		text += ": " + msg
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("%w (%s)", sandbox.ErrNotFound, text)
	}
	return errors.New(text)
}

// cut shortens s to n bytes without cutting a rune in half.
func cut(s string) string {
	n := maxMessage
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "..."
}

// scrub hides every secret in s. It runs before cut, so a secret split by the
// cut cannot survive as a prefix.
func scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// scrubErr strips secrets from a transport error: net/http errors carry the
// URL, and one could one day carry a key.
func scrubErr(err error, secrets []string) error {
	msg := err.Error()
	if scrub(msg, secrets) == msg {
		return err
	}
	return errors.New(scrub(msg, secrets))
}

// secretsIn gathers the values that must never reach an error.
func secretsIn(env map[string]string, apiKey string) []string {
	out := []string{apiKey}
	for _, v := range env {
		out = append(out, v)
	}
	return out
}

func lookupIPv4(ctx context.Context, host string) ([]string, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

// A room is what a sandbox id needs beside it. The traffic token is returned
// once, by create, and by no later call (GET /sandboxes/{id} does not list
// it), so it has to travel with the id. The string is therefore a credential
// for that sandbox's ports and is not to be logged. Carrying it, rather than
// keeping it in the client, survives a brain restart and a second brain.
type room struct {
	ID      string `json:"i"`
	Traffic string `json:"t,omitempty"`
	Domain  string `json:"d"`
}

const roomPrefix = "e2b1."

func encodeRoom(r room) string {
	b, _ := json.Marshal(r)
	return roomPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func decodeRoom(s string) (room, error) {
	raw, ok := strings.CutPrefix(s, roomPrefix)
	if !ok {
		return room{}, errors.New("e2b: not an E2B room")
	}
	var r room
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &r) != nil || r.ID == "" || r.Domain == "" {
		return room{}, errors.New("e2b: not an E2B room")
	}
	return r, nil
}
