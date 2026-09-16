package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
)

var ErrRunFinished = errors.New("the run has finished, so there is no request left to carry a steer")

type SteerEvent struct{ Text string }

func (SteerEvent) event() {}

type steer struct {
	text string
	at   int
}

type steerBox struct {
	mu       sync.Mutex
	pending  []string
	carried  []steer
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

func (b *steerBox) take(at int) (fresh []string, carried []steer) {
	b.mu.Lock()
	defer b.mu.Unlock()

	fresh = b.pending
	b.pending = nil
	for _, text := range fresh {
		b.carried = append(b.carried, steer{text: text, at: at})
	}
	return fresh, slices.Clone(b.carried)
}

func (b *steerBox) applied() []steer {
	b.mu.Lock()
	defer b.mu.Unlock()

	return slices.Clone(b.carried)
}

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

func (c *steeringClient) apply(msgs []Message) []Message {
	fresh, carried := c.box.take(len(msgs))

	for _, text := range fresh {
		if c.emit != nil {
			c.emit(SteerEvent{Text: text})
		}
	}
	return recordSteers(msgs, carried)
}

func steerMessage(text string) Message {
	return Message{Role: RoleUser, Content: []Content{{
		Type: ContentText,
		Text: "The user sent this while you were working:\n" + text,
	}}}
}

func recordSteers(msgs []Message, steers []steer) []Message {
	if len(steers) == 0 {
		return msgs
	}

	out := slices.Clone(msgs)
	for i, s := range steers {
		at := min(s.at+i, len(out))
		out = slices.Insert(out, at, steerMessage(s.text))
	}
	return out
}

var _ ModelClient = (*steeringClient)(nil)
