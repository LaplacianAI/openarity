// Package daytona is the brain's client for Daytona sandboxes, on Daytona
// cloud or self-hosted. It speaks Daytona's REST API
// (https://www.daytona.io/docs/en/tools/api/) and its toolbox API with plain
// net/http: Daytona's Go SDK is a large module that pulls in much of its
// own platform, for five calls.
package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// CloudAPI is Daytona cloud's API base.
const CloudAPI = "https://app.daytona.io/api"

// ImagePrefix marks a Spec.Image that is a container image to build from,
// "image:node:22-slim". Anything else names a Daytona snapshot, and empty
// takes the snapshot Daytona starts from by default.
const ImagePrefix = "image:"

const (
	previewAuthHeader = "X-Daytona-Preview-Token"
	sessionID         = "openarity"
	maxErrorText      = 300
	maxBody           = 1 << 20
)

// A Client is one team's Daytona credentials. It is safe for concurrent use.
type Client struct {
	apiURL string
	apiKey string
	http   *http.Client
	// PollEvery and StartTimeout bound the wait for a new room to start.
	PollEvery    time.Duration
	StartTimeout time.Duration
	// DomainFirewall says this Daytona enforces domain allow lists. Cloud
	// does; a self-hosted one does not, so it is on only for Daytona cloud.
	// See egress.go.
	DomainFirewall bool
	// Resolve looks up host names for a Daytona without a domain firewall.
	Resolve Resolver
}

var _ sandbox.Rooms = (*Client)(nil)

// New makes a client. An empty apiURL is Daytona cloud; a self-hosted
// Daytona passes the URL of its API, ending in /api.
func New(apiURL, apiKey string, client *http.Client) *Client {
	if apiURL == "" {
		apiURL = CloudAPI
	}
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	return &Client{
		apiURL:         strings.TrimRight(apiURL, "/"),
		apiKey:         apiKey,
		http:           client,
		PollEvery:      time.Second,
		StartTimeout:   3 * time.Minute,
		DomainFirewall: strings.TrimRight(apiURL, "/") == CloudAPI,
		Resolve:        lookupIPv4,
	}
}

func lookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

type createRequest struct {
	Snapshot         string            `json:"snapshot,omitempty"`
	BuildInfo        *buildInfo        `json:"buildInfo,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	NetworkBlockAll  *bool             `json:"networkBlockAll,omitempty"`
	NetworkAllowList string            `json:"networkAllowList,omitempty"`
	DomainAllowList  string            `json:"domainAllowList,omitempty"`
	AutoStopInterval *int              `json:"autoStopInterval,omitempty"`
}

type buildInfo struct {
	DockerfileContent string `json:"dockerfileContent"`
}

type sandboxView struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	ErrorReason     string `json:"errorReason"`
	ToolboxProxyURL string `json:"toolboxProxyUrl"`
}

// Create makes a room, from the spec's image, and waits until it is started.
// The room never auto-stops: a stopped room has lost the shim in it.
func (c *Client) Create(ctx context.Context, s sandbox.Spec) (string, error) {
	n, err := c.network(ctx, s)
	if err != nil {
		return "", err
	}
	zero := 0
	req := createRequest{
		Env:              s.Env,
		Labels:           s.Labels,
		NetworkAllowList: strings.Join(n.cidrs, ","),
		DomainAllowList:  strings.Join(n.domains, ","),
		AutoStopInterval: &zero,
	}
	if n.blockAll {
		req.NetworkBlockAll = &n.blockAll
	}
	switch {
	case strings.HasPrefix(s.Image, ImagePrefix):
		ref := strings.TrimPrefix(s.Image, ImagePrefix)
		if ref == "" || strings.ContainsAny(ref, " \t\r\n") {
			return "", refuse("%q is not an image reference", s.Image)
		}
		req.BuildInfo = &buildInfo{DockerfileContent: "FROM " + ref + "\n"}
	default:
		req.Snapshot = s.Image
	}

	var v sandboxView
	if err := c.do(ctx, http.MethodPost, c.apiURL+"/sandbox", req, &v, secretsOf(s)); err != nil {
		if isStatus(err, http.StatusBadRequest) || isStatus(err, http.StatusUnprocessableEntity) {
			return "", fmt.Errorf("%w: %w", sandbox.ErrRefused, err)
		}
		return "", err
	}
	if v.ID == "" {
		return "", errors.New("daytona: create returned no sandbox id")
	}
	if err := c.waitStarted(ctx, v); err != nil {
		// A room that did not start is not left behind. The context may be
		// what failed, so the cleanup has its own.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = c.Delete(cctx, v.ID)
		return "", err
	}
	return v.ID, nil
}

func (c *Client) waitStarted(ctx context.Context, v sandboxView) error {
	ctx, cancel := context.WithTimeout(ctx, c.StartTimeout)
	defer cancel()
	for {
		switch v.State {
		case "started":
			return nil
		case "error", "build_failed", "destroyed", "destroying":
			return fmt.Errorf("daytona: sandbox %s is %s: %s", v.ID, v.State, truncate(v.ErrorReason))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daytona: sandbox %s was still %q: %w", v.ID, v.State, ctx.Err())
		case <-time.After(c.PollEvery):
		}
		if err := c.do(ctx, http.MethodGet, c.apiURL+"/sandbox/"+url.PathEscape(v.ID), nil, &v, nil); err != nil {
			return err
		}
	}
}

// Start runs argv in the room as a command in a process session, and returns
// once Daytona has accepted it, not when it ends.
func (c *Client) Start(ctx context.Context, room string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("daytona: no command to start")
	}
	var v sandboxView
	if err := c.do(ctx, http.MethodGet, c.apiURL+"/sandbox/"+url.PathEscape(room), nil, &v, nil); err != nil {
		return err
	}
	if v.ToolboxProxyURL == "" {
		return errors.New("daytona: sandbox has no toolbox URL")
	}
	base := strings.TrimRight(v.ToolboxProxyURL, "/") + "/" + url.PathEscape(room) + "/process/session"
	err := c.do(ctx, http.MethodPost, base, map[string]string{"sessionId": sessionID}, nil, nil)
	if err != nil && !isStatus(err, http.StatusConflict) {
		return err
	}
	body := map[string]any{"command": shellJoin(argv), "runAsync": true}
	return c.do(ctx, http.MethodPost, base+"/"+sessionID+"/exec", body, nil, nil)
}

// URL is the preview address of port in the room, with the token Daytona wants
// on every request to it.
func (c *Client) URL(ctx context.Context, room string, port int) (sandbox.Endpoint, error) {
	var p struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	path := fmt.Sprintf("%s/sandbox/%s/ports/%d/preview-url", c.apiURL, url.PathEscape(room), port)
	if err := c.do(ctx, http.MethodGet, path, nil, &p, nil); err != nil {
		return sandbox.Endpoint{}, err
	}
	if p.URL == "" {
		return sandbox.Endpoint{}, errors.New("daytona: preview link has no URL")
	}
	h := http.Header{}
	if p.Token != "" {
		h.Set(previewAuthHeader, p.Token)
	}
	return sandbox.Endpoint{URL: p.URL, Header: h}, nil
}

// Delete removes the room. One Daytona no longer has is not an error.
func (c *Client) Delete(ctx context.Context, room string) error {
	err := c.do(ctx, http.MethodDelete, c.apiURL+"/sandbox/"+url.PathEscape(room), nil, nil, nil)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

// A statusError is Daytona's own answer to a call.
type statusError struct {
	status int
	method string
	path   string
	msg    string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("daytona: %s %s: %d: %s", e.method, e.path, e.status, e.msg)
}

func (e *statusError) Is(target error) bool {
	return target == sandbox.ErrNotFound && e.status == http.StatusNotFound
}

func isStatus(err error, status int) bool {
	var se *statusError
	return errors.As(err, &se) && se.status == status
}

// do sends one call. secrets are values that must not appear in an error even
// if Daytona echoes them back; the API key never does.
func (c *Client) do(ctx context.Context, method, target string, in, out any, secrets []string) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("daytona: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body) //nolint:gosec // the address is the team's saved Daytona URL; the caller's http.Client decides what it may reach
	if err != nil {
		return fmt.Errorf("daytona: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req) //nolint:gosec // see above
	if err != nil {
		return fmt.Errorf("daytona: %s %s: %w", method, req.URL.Path, c.scrub(unwrapURL(err), secrets))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("daytona: read %s %s: %w", method, req.URL.Path, err)
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{
			status: resp.StatusCode, method: method, path: req.URL.Path,
			msg: c.scrub(errors.New(daytonaMessage(raw)), secrets).Error(),
		}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("daytona: %s %s: unreadable answer: %w", method, req.URL.Path, err)
	}
	return nil
}

// unwrapURL drops the *url.Error wrapper, whose text repeats the whole URL.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (c *Client) scrub(err error, secrets []string) error {
	msg := err.Error()
	if c.apiKey != "" {
		msg = strings.ReplaceAll(msg, c.apiKey, "[redacted]")
	}
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	return &scrubbed{msg: truncate(msg), err: err}
}

func secretsOf(s sandbox.Spec) []string {
	out := make([]string, 0, len(s.Env))
	for _, v := range s.Env {
		if len(v) >= 4 {
			out = append(out, v)
		}
	}
	return out
}

// daytonaMessage is the "message" of Daytona's error body, or its text.
func daytonaMessage(raw []byte) string {
	var e struct {
		Message any `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != nil {
		switch m := e.Message.(type) {
		case string:
			return m
		default:
			b, _ := json.Marshal(m)
			return string(b)
		}
	}
	return strings.TrimSpace(string(raw))
}

func truncate(s string) string {
	if len(s) <= maxErrorText {
		return s
	}
	return s[:maxErrorText] + "..."
}

// shellJoin is argv as one line for the room's shell, each word quoted.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// scrubbed is an error whose text has had secrets taken out, and whose cause is
// kept for errors.Is, such as a deadline.
type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }
