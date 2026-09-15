package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
)

// Steering — saying something to a run that has already started.
//
// The model is not listening while a pattern works. It is called, it answers,
// and between those two moments it has no ears: a run is a loop the caller's
// program drives, and the only place new words can enter is the request being
// assembled for the next call.
//
// So a steer is not delivered. It waits, and rides the next request out.
//
// None of this lives in a pattern. Patterns reach the model through
// ModelClient, so a client that wraps another sees every request any pattern
// makes — the same seam countingClient already uses to total usage without a
// pattern knowing to try. ReAct, ReWOO, Plan, Reflection and any pattern
// written outside this module get steering without a line of change, which is
// the whole reason it is here rather than in the loop.

// ErrRunFinished is returned by Steer once there will be no further request to
// carry it. Silence would be worse: a steer that vanishes looks exactly like
// one the model read and ignored.
var ErrRunFinished = errors.New("the run has finished, so there is no request left to carry a steer")

// SteerEvent records that a steer reached a request. Without it a replayed
// transcript shows a run changing course for no visible reason.
type SteerEvent struct{ Text string }

func (SteerEvent) event() {}

// steerBox is the waiting list. Steer is called from whatever goroutine is
// watching the events; drain is called from the one running the pattern.
type steerBox struct {
	mu       sync.Mutex
	pending  []string
	finished bool
}

func (b *steerBox) add(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.finished {
		return ErrRunFinished
	}
	b.pending = append(b.pending, text)
	return nil
}

func (b *steerBox) drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := b.pending
	b.pending = nil
	return out
}

// close stops accepting steers and hands back any that never made it onto a
// request. A run that ends while something is still waiting has to say so.
func (b *steerBox) close() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.finished = true
	out := b.pending
	b.pending = nil
	return out
}

type steeringClient struct {
	inner ModelClient
	box   *steerBox
	emit  func(Event)
}

func (c *steeringClient) Complete(ctx context.Context, req Request) (Response, error) {
	req.Messages = c.apply(req.Messages)
	return c.inner.Complete(ctx, req)
}

func (c *steeringClient) Stream(ctx context.Context, req Request) (Stream, error) {
	req.Messages = c.apply(req.Messages)
	return c.inner.Stream(ctx, req)
}

// apply puts whatever is waiting onto this request.
//
// Where it goes is decided by what the pattern has already built, and there is
// only one safe answer in each case:
//
// After a tool call, the provider requires the very next message to be that
// call's result — a user message wedged in between is a 400 before the model
// sees anything. But a tool result is free-form text, so the steer is added to
// the end of it. Nothing is inserted and no ordering rule is touched.
//
// Anywhere else, a user message is exactly what it should be.
//
// The messages are copied rather than edited. The slice belongs to the
// pattern, which keeps appending to it and returns it as Result.Messages; a
// steer written into that slice would be in the transcript twice.
func (c *steeringClient) apply(msgs []Message) []Message {
	steers := c.box.drain()
	if len(steers) == 0 {
		return msgs
	}

	for _, text := range steers {
		if c.emit != nil {
			c.emit(SteerEvent{Text: text})
		}
	}
	block := steerBlock(steers)

	out := slices.Clone(msgs)
	if last := len(out) - 1; last >= 0 && out[last].Role == RoleTool {
		out[last] = withText(out[last], block)
		return out
	}

	return append(out, Message{
		Role:    RoleUser,
		Content: []Content{{Type: ContentText, Text: block}},
	})
}

// steerBlock says where the words came from. Unlabelled, a steer reads as
// something that was always in the conversation — and "look at vault.go"
// arriving as ordinary history is advice the model may weigh against its
// current plan rather than an instruction that just arrived from the person
// watching it work.
func steerBlock(steers []string) string {
	var b strings.Builder
	b.WriteString("The user sent this while you were working:\n")
	for i, text := range steers {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
	}
	return b.String()
}

// withText returns a copy of m with text appended as a further block. The
// content slice is cloned for the same reason the message slice is.
func withText(m Message, text string) Message {
	m.Content = append(slices.Clone(m.Content), Content{Type: ContentText, Text: "\n\n" + text})
	return m
}

var _ ModelClient = (*steeringClient)(nil)
