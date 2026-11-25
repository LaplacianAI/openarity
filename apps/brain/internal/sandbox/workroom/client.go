package workroom

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
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/roomfile"
)

// maxAnswer is the largest answer read from a workspace. Its outputs are
// capped in the room (256 KiB each of stdout and stderr, 2,000 lines a
// read), so this is only a bound on a room that misbehaves.
const maxAnswer = 4 << 20

// A Problem is the model's to fix: a path that is not there, an edit that
// matched twice. Its text is the workspace's, written for the model.
type Problem struct{ Msg string }

func (p *Problem) Error() string { return p.Msg }

var (
	// ErrGone is a call that never reached the workspace: nothing listens at
	// its address, or the provider says it has no such room. The call did
	// not run. Ensure gives the room again, or a new one.
	ErrGone = errors.New("the workspace's room is gone")
	// ErrNoAnswer is a call that was sent and got no answer: the connection
	// broke, or the provider's proxy answered for a room that stopped
	// answering. The call may or may not have run.
	ErrNoAnswer = errors.New("the workspace took the call and gave no answer")
)

// Client calls one workspace.
type Client struct {
	ep   sandbox.Endpoint
	http *http.Client
}

// NewClient calls the workspace at ep with hc, whose policy decides which
// addresses a room may be at: the caller gives it the one MCP servers use.
func NewClient(ep sandbox.Endpoint, hc *http.Client) *Client {
	return &Client{ep: ep, http: hc}
}

// Exec is what a command did.
type Exec struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

func (c *Client) Exec(ctx context.Context, command string, timeoutSeconds int) (Exec, error) {
	var out Exec
	err := c.post(ctx, "/exec", map[string]any{"command": command, "timeout_seconds": timeoutSeconds}, &out)
	return out, err
}

// Read is part of a text file, its lines numbered, and how many it has.
type Read struct {
	Content string `json:"content"`
	Lines   int    `json:"lines"`
}

func (c *Client) Read(ctx context.Context, path string, offset, limit int) (Read, error) {
	var out Read
	err := c.post(ctx, "/read", map[string]any{"path": path, "offset": offset, "limit": limit}, &out)
	return out, err
}

// Written is where a write or an edit landed.
type Written struct {
	Path     string `json:"path"`
	Bytes    int    `json:"bytes"`
	Replaced int    `json:"replaced"`
}

func (c *Client) Write(ctx context.Context, path, content string) (Written, error) {
	var out Written
	err := c.post(ctx, "/write", map[string]any{"path": path, "content": content}, &out)
	return out, err
}

func (c *Client) Edit(ctx context.Context, path, old, new string, all bool) (Written, error) {
	var out Written
	err := c.post(ctx, "/edit", map[string]any{"path": path, "old_string": old, "new_string": new, "replace_all": all}, &out)
	return out, err
}

// Glob is the files a pattern matched, newest first.
type Glob struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

func (c *Client) Glob(ctx context.Context, pattern, path string) (Glob, error) {
	var out Glob
	err := c.post(ctx, "/glob", map[string]any{"pattern": pattern, "path": path}, &out)
	return out, err
}

// GrepQuery is what a grep looks for, as the workspace takes it.
type GrepQuery struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	Output     string `json:"output,omitempty"`
	Context    int    `json:"context,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
}

// Grep is a grep's answer, one line per file or per matching line.
type Grep struct {
	Output    string `json:"output"`
	Files     int    `json:"files"`
	Truncated bool   `json:"truncated"`
}

func (c *Client) Grep(ctx context.Context, q GrepQuery) (Grep, error) {
	var out Grep
	err := c.post(ctx, "/grep", q, &out)
	return out, err
}

// File is a file in the workspace whole, up to roomfile.Max, for keeping as
// an attachment: any file, text or not, by its path from the workspace.
func (c *Client) File(ctx context.Context, path string) ([]byte, error) {
	ep := sandbox.Endpoint{URL: strings.TrimRight(c.ep.URL, "/") + roomfile.Route, Header: c.ep.Header}
	data, err := roomfile.Get(ctx, c.http, ep, path)
	var p *roomfile.Problem
	switch {
	case errors.As(err, &p):
		return nil, &Problem{Msg: p.Msg}
	case errors.Is(err, roomfile.ErrGone):
		return nil, fmt.Errorf("%w: %w", ErrGone, err)
	}
	return data, err
}

func (c *Client) post(ctx context.Context, route string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.ep.URL, "/")+route, bytes.NewReader(b))
	if err != nil {
		return err
	}
	for k, vs := range c.ep.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Never the URL: a provider's address can carry its token.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return fmt.Errorf("%w: %s", ErrGone, transport(err))
		}
		return fmt.Errorf("%w: %s", ErrNoAnswer, transport(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The call was taken; how it ended is not known.
		return fmt.Errorf("%w: the answer broke off: %s", ErrNoAnswer, transport(err))
	}
	if len(raw) > maxAnswer {
		return fmt.Errorf("the workspace's answer is over %d MiB, more than it ever sends", maxAnswer>>20)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("the workspace's answer is not JSON: %w", err)
		}
		return nil
	case http.StatusUnprocessableEntity:
		var p struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &p) != nil || p.Error == "" {
			return fmt.Errorf("the workspace refused the call and said nothing")
		}
		return &Problem{Msg: p.Error}
	case http.StatusNotFound:
		// The provider's proxy answers for a room it no longer has.
		return fmt.Errorf("%w: %d", ErrGone, resp.StatusCode)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// The proxy could not get an answer, which says nothing of whether
		// the room had the call.
		return fmt.Errorf("%w: %d", ErrNoAnswer, resp.StatusCode)
	default:
		return fmt.Errorf("the workspace answered %d: %s", resp.StatusCode, firstLine(raw))
	}
}

// transport is a failed request's cause without the URL it was for.
func transport(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Err != nil {
			return ue.Err.Error()
		}
		return "the request failed"
	}
	// Not the request's own error, so it carries no URL: reading the answer.
	return err.Error()
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
