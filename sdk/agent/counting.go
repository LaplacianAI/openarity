package agent

import (
	"context"
	"sync"
)

type countingClient struct {
	inner ModelClient

	mu    sync.Mutex
	total Usage
}

func (c *countingClient) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := c.inner.Complete(ctx, req)
	c.add(resp.Usage)
	return resp, err
}

func (c *countingClient) Stream(ctx context.Context, req Request) (Stream, error) {
	s, err := c.inner.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &countingStream{inner: s, count: c.add}, nil
}

func (c *countingClient) add(u Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total.Add(u)
}

func (c *countingClient) spent() Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

type countingStream struct {
	inner   Stream
	count   func(Usage)
	counted bool
}

func (s *countingStream) Next() bool {
	if !s.inner.Next() {
		return false
	}
	if ev := s.inner.Event(); ev.Final != nil && !s.counted {
		s.counted = true
		s.count(ev.Final.Usage)
	}
	return true
}

func (s *countingStream) Event() StreamEvent { return s.inner.Event() }
func (s *countingStream) Err() error         { return s.inner.Err() }
func (s *countingStream) Close() error       { return s.inner.Close() }

var (
	_ ModelClient = (*countingClient)(nil)
	_ Stream      = (*countingStream)(nil)
)
