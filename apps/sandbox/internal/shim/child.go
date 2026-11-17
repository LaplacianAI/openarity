package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
)

// maxLine is the longest message the server may write: a tool's result is
// one line of JSON, and a large one is still a message, not a stream.
const maxLine = 16 << 20

// childProtocol is what the shim asks the server for: the newest version
// with the initialize handshake. A newer client is answered with what the
// server agreed to.
const childProtocol = "2025-11-25"

var ErrExited = errors.New("the MCP server has exited")

// A Child is the one MCP session with the server on stdio, shared by every
// session the shim serves: each request is sent under an id of the shim's
// own and answered to whoever sent it.
type Child struct {
	cmd *exec.Cmd

	wmu   sync.Mutex
	stdin io.WriteCloser

	mu      sync.Mutex
	next    int64
	waiting map[int64]chan Response

	done chan struct{}
	err  error

	initialized json.RawMessage
}

// A Response is the server's answer to one request, without its id.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// message is any JSON-RPC message, read far enough to route it.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Response
}

// Start runs argv with its stdio as the server's transport; its stderr goes
// to stderr. The server is stopped when ctx ends.
func Start(ctx context.Context, argv []string, stderr io.Writer) (*Child, error) {
	if len(argv) == 0 {
		return nil, errors.New("no command to run")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // running the server is the point
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the MCP server: %w", err)
	}

	c := &Child{cmd: cmd, stdin: stdin, waiting: map[int64]chan Response{}, done: make(chan struct{})}
	go c.read(stdout)
	return c, nil
}

// Done is closed once the server has exited; Err then says why.
func (c *Child) Done() <-chan struct{} { return c.done }

func (c *Child) Err() error {
	<-c.done
	return c.err
}

// Initialize performs the handshake once, and keeps the server's answer for
// every session that opens later.
func (c *Child) Initialize(ctx context.Context) error {
	params, err := json.Marshal(map[string]any{
		"protocolVersion": childProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "openarity-sandbox-shim", "version": "1"},
	})
	if err != nil {
		return err
	}
	r, err := c.Call(ctx, "initialize", params, nil)
	if err != nil {
		return err
	}
	if r.Error != nil {
		return fmt.Errorf("the MCP server refused initialize: %s", r.Error)
	}
	c.mu.Lock()
	c.initialized = r.Result
	c.mu.Unlock()
	return c.Notify("notifications/initialized", nil)
}

// Initialized is the server's answer to initialize, or nil before it.
func (c *Child) Initialized() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

// Call sends a request and waits for its answer. sent, when given, learns
// the id the server knows the request by, so a cancellation the caller
// receives later can be passed on. When ctx ends first, the server is told
// the request is cancelled.
func (c *Child) Call(ctx context.Context, method string, params json.RawMessage, sent func(id int64)) (Response, error) {
	id, wait, err := c.await()
	if err != nil {
		return Response{}, err
	}
	return c.send(ctx, id, wait, method, params, sent)
}

func (c *Child) send(ctx context.Context, id int64, wait chan Response, method string, params json.RawMessage, sent func(int64)) (Response, error) {
	if err := c.write(message{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: params}); err != nil {
		c.forget(id)
		return Response{}, err
	}
	if sent != nil {
		sent(id)
	}

	select {
	case r, ok := <-wait:
		if !ok {
			return Response{}, c.exitErr()
		}
		return r, nil
	case <-ctx.Done():
		c.forget(id)
		_ = c.Cancel(id)
		return Response{}, ctx.Err()
	}
}

// Cancel tells the server to stop working on the request it knows as id.
func (c *Child) Cancel(id int64) error {
	params, err := json.Marshal(map[string]int64{"requestId": id})
	if err != nil {
		return err
	}
	return c.Notify("notifications/cancelled", params)
}

// Notify sends a notification; nothing comes back.
func (c *Child) Notify(method string, params json.RawMessage) error {
	return c.write(message{Method: method, Params: params})
}

func (c *Child) await() (int64, chan Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return 0, nil, c.exitErr()
	default:
	}
	c.next++
	wait := make(chan Response, 1)
	c.waiting[c.next] = wait
	return c.next, wait, nil
}

func (c *Child) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.waiting, id)
}

func (c *Child) write(m message) error {
	line, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		message
	}{"2.0", m})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("%w: %w", ErrExited, err)
	}
	return nil
}

// read routes each line the server writes: an answer to whoever waits for
// it, a request refused (no client is there to answer it), a notification
// dropped. It ends when the server does.
func (c *Child) read(stdout io.Reader) {
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 64<<10), maxLine)
	for lines.Scan() {
		var m message
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			continue // a server that logs to stdout; nothing to route
		}
		switch {
		case m.Method != "" && m.ID != nil:
			_ = c.write(message{ID: m.ID, Response: Response{Error: noClient}})
		case m.Method != "":
			// A notification has no session to go to.
		default:
			c.answer(m)
		}
	}

	err := lines.Err()
	if wait := c.cmd.Wait(); err == nil {
		err = wait
	}
	c.mu.Lock()
	c.err = err
	for id, wait := range c.waiting {
		close(wait)
		delete(c.waiting, id)
	}
	close(c.done)
	c.mu.Unlock()
}

func (c *Child) answer(m message) {
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		return // not an id the shim gave
	}
	c.mu.Lock()
	wait, ok := c.waiting[id]
	delete(c.waiting, id)
	c.mu.Unlock()
	if ok {
		wait <- m.Response
	}
}

func (c *Child) exitErr() error {
	if c.err != nil {
		return fmt.Errorf("%w: %w", ErrExited, c.err)
	}
	return ErrExited
}

var noClient = json.RawMessage(`{"code":-32601,"message":"no client is connected to answer requests from the server"}`)
