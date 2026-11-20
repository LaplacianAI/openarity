// Package sandboxd is the brain's client for sandboxd, the self-hosted
// sandbox provider in apps/sandbox. It speaks sandboxd's HTTP API and nothing
// else: the brain and sandboxd share no code, only that API.
package sandboxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// maxReply bounds what is read of any answer: sandboxd's are a line long.
const maxReply = 1 << 20

// Client is sandbox.Rooms against one sandboxd.
type Client struct {
	base   string
	token  string
	client *http.Client
}

var _ sandbox.Rooms = (*Client)(nil)

// New returns a client for the sandboxd at baseURL, authenticating with
// token. The http.Client is the caller's, so the caller decides what it may
// dial; a nil one panics.
func New(baseURL, token string, client *http.Client) *Client {
	if client == nil {
		panic("sandboxd: a nil http client")
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, client: client}
}

type egressBody struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type createBody struct {
	Image  string            `json:"image"`
	Env    map[string]string `json:"env"`
	Egress []egressBody      `json:"egress"`
	Labels map[string]string `json:"labels"`
	// sandboxd serves a public room through its proxy, and gives a
	// workspace room its /workspace and its size.
	Network   string `json:"network,omitempty"`
	Workspace bool   `json:"workspace,omitempty"`
}

func (c *Client) Create(ctx context.Context, s sandbox.Spec) (string, error) {
	body := createBody{
		Image:  s.Image,
		Env:    nonNil(s.Env),
		Egress: make([]egressBody, 0, len(s.Egress)),
		Labels: nonNil(s.Labels),

		Network:   string(s.Network),
		Workspace: s.Workspace,
	}
	for _, e := range s.Egress {
		body.Egress = append(body.Egress, egressBody{Host: e.Host, Port: e.Port})
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/rooms", body, http.StatusCreated, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("sandboxd made a room and named no id")
	}
	return out.ID, nil
}

func (c *Client) Start(ctx context.Context, room string, argv []string) error {
	body := struct {
		Argv []string `json:"argv"`
	}{Argv: argv}
	return c.do(ctx, http.MethodPost, roomPath(room)+"/start", body, http.StatusNoContent, nil)
}

func (c *Client) URL(ctx context.Context, room string, port int) (sandbox.Endpoint, error) {
	var out struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	path := roomPath(room) + "/ports/" + strconv.Itoa(port)
	if err := c.do(ctx, http.MethodGet, path, nil, http.StatusOK, &out); err != nil {
		return sandbox.Endpoint{}, err
	}
	if out.URL == "" {
		return sandbox.Endpoint{}, errors.New("sandboxd named no url for the port")
	}
	h := http.Header{}
	for k, v := range out.Headers {
		h.Set(k, v)
	}
	return sandbox.Endpoint{URL: out.URL, Header: h}, nil
}

// Delete treats a room that is already gone as deleted.
func (c *Client) Delete(ctx context.Context, room string) error {
	err := c.do(ctx, http.MethodDelete, roomPath(room), nil, http.StatusNoContent, nil)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

func roomPath(room string) string { return "/v1/rooms/" + url.PathEscape(room) }

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// do sends one request and checks its status. A request body holds secrets
// (the room's env), so no error here carries it.
func (c *Client) do(ctx context.Context, method, path string, in any, want int, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding the request to sandboxd: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("building the request to sandboxd: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("sandboxd %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return fmt.Errorf("reading sandboxd's answer: %w", err)
	}
	if resp.StatusCode != want {
		return statusError(resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding sandboxd's answer: %w", err)
		}
	}
	return nil
}

func statusError(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	switch status {
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", sandbox.ErrRefused, e.Error)
	case http.StatusNotFound:
		return sandbox.ErrNotFound
	}
	if e.Error == "" {
		return fmt.Errorf("sandboxd answered %d", status)
	}
	return fmt.Errorf("sandboxd answered %d: %s", status, e.Error)
}
