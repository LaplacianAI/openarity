package agent

import (
	"context"
	"errors"
	"fmt"
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

func (b *steerBox) fill(texts []string) {
	if len(texts) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pending = append(b.pending, texts...)
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

func (b *steerBox) restart() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := b.pending
	b.pending = nil
	b.carried = nil
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
	inner   ModelClient
	box     *steerBox
	session Session
	emit    func(Event)
}

func (c *steeringClient) Complete(ctx context.Context, req Request) (Response, error) {
	msgs, err := c.apply(ctx, req.Messages)
	if err != nil {
		return Response{}, err
	}
	req.Messages = msgs
	return c.inner.Complete(ctx, req)
}

func (c *steeringClient) Stream(ctx context.Context, req Request) (Stream, error) {
	msgs, err := c.apply(ctx, req.Messages)
	if err != nil {
		return nil, err
	}
	req.Messages = msgs
	return c.inner.Stream(ctx, req)
}

func (c *steeringClient) apply(ctx context.Context, msgs []Message) ([]Message, error) {
	if c.session != nil {
		elsewhere, err := c.session.TakeSteers(ctx)
		if err != nil {
			return nil, fmt.Errorf("collecting the steers left for this session: %w", err)
		}
		c.box.fill(elsewhere)
	}

	fresh, carried := c.box.take(len(msgs))

	for _, text := range fresh {
		if c.emit != nil {
			c.emit(SteerEvent{Text: text})
		}
	}
	return recordSteers(msgs, carried), nil
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

func steerMessages(steers []string) []Message {
	out := make([]Message, 0, len(steers))
	for _, text := range steers {
		out = append(out, Message{
			Role:    RoleUser,
			Content: []Content{{Type: ContentText, Text: text}},
		})
	}
	return out
}

var _ ModelClient = (*steeringClient)(nil)
