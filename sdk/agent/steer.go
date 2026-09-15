package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
)

var ErrRunFinished = errors.New("the run has finished, so there is no request left to carry a steer")

type SteerEvent struct{ Text string }

func (SteerEvent) event() {}

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

func withText(m Message, text string) Message {
	m.Content = append(slices.Clone(m.Content), Content{Type: ContentText, Text: "\n\n" + text})
	return m
}

var _ ModelClient = (*steeringClient)(nil)
