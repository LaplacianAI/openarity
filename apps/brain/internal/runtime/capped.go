package runtime

import (
	"fmt"
	"io"
	"net/http"
	"sync"
)

const (
	// maxAnswerBytes is the most of one answer an MCP server may send: a
	// file kept at mcp.MaxKeptBytes, in base64, with room for its JSON and
	// text. Decoding one costs about nine times its size, measured, so the
	// cap is what bounds a call's memory.
	maxAnswerBytes = 40 << 20
	// largeAnswer is where an answer takes the worker's one slot for large
	// answers before it is read on.
	largeAnswer = 1 << 20
)

// capped is an MCP client's transport, bounded: an answer past
// maxAnswerBytes fails its read, and a POST's answer past largeAnswer waits
// for large, the worker's one slot, so a worker reads one large answer at a
// time. The slot is given back when the answer is read to its end or closed,
// so it bounds the read and the decode; the decoded files live on while the
// keeper stores them, so a worker holds a little more than one answer's
// worth. A GET's answer is a server's standing stream of notifications, so
// it never takes the slot.
type capped struct {
	next  http.RoundTripper
	large chan struct{}
}

// cappedClient is c with its transport capped, sharing large.
func cappedClient(c *http.Client, large chan struct{}) *http.Client {
	out := *c
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	out.Transport = &capped{next: next, large: large}
	return &out
}

func (c *capped) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Body = &cappedBody{
		body: resp.Body, req: req, large: c.large,
		// No slot (a Builder not made by NewBuilder) is no wait, not a
		// read that blocks for ever.
		post: req.Method == http.MethodPost && c.large != nil,
	}
	return resp, nil
}

type cappedBody struct {
	body  io.ReadCloser
	req   *http.Request
	large chan struct{}
	post  bool

	read int // only Read touches it

	// mu guards held: the client may close a body while it is being read.
	mu     sync.Mutex
	held   bool
	closed bool
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.post && !b.holding() {
		if b.read < largeAnswer {
			// Read up to the line, so an answer that ends before it never
			// waits for the slot.
			p = p[:min(len(p), largeAnswer-b.read)]
		} else {
			return b.past(p)
		}
	}
	// One byte past the cap is enough to know it was passed.
	p = p[:min(len(p), maxAnswerBytes+1-b.read)]
	n, err := b.body.Read(p)
	b.read += n
	if b.read > maxAnswerBytes {
		return n, fmt.Errorf("the server's answer is over %d MiB", maxAnswerBytes>>20)
	}
	if err == io.EOF {
		// Read to its end: the next large answer need not wait for Close.
		// A read after it peeks its EOF in past and takes no slot.
		b.release()
	}
	return n, err
}

// past reads the first byte beyond largeAnswer, taking the slot only when
// there is one: an answer that ends at the line never waits for it.
func (b *cappedBody) past(p []byte) (int, error) {
	n, err := b.body.Read(p[:1])
	if n == 0 {
		return 0, err
	}
	if err := b.take(); err != nil {
		return 0, err
	}
	b.read += n
	if err == io.EOF {
		b.release()
	}
	return n, err
}

// release gives the slot back, once.
func (b *cappedBody) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseLocked()
}

// releaseLocked is release with b.mu held.
func (b *cappedBody) releaseLocked() {
	if b.held {
		<-b.large
		b.held = false
	}
}

func (b *cappedBody) holding() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}

// take waits for the worker's slot, or for the request to end.
func (b *cappedBody) take() error {
	select {
	case b.large <- struct{}{}:
	case <-b.req.Context().Done():
		return b.req.Context().Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// Closed while it waited: nothing more is read, so give it back.
		<-b.large
		return io.ErrClosedPipe
	}
	b.held = true
	return nil
}

// Close gives the slot back once, however often it is called.
func (b *cappedBody) Close() error {
	err := b.body.Close()
	// One critical section: a take between a release and closed would hold
	// the slot with nothing left to give it back.
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.releaseLocked()
	return err
}
