package agent

import (
	"context"
	"fmt"
)

// savingClient saves the transcript before each request, with any summaries
// a compaction wrote spliced back in.
type savingClient struct {
	inner   ModelClient
	session Session
	splice  func([]Message) []Message
}

func (c *savingClient) Complete(ctx context.Context, req Request) (Response, error) {
	if err := c.session.Save(ctx, c.splice(req.Messages)); err != nil {
		return Response{}, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Complete(ctx, req)
}

func (c *savingClient) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := c.session.Save(ctx, c.splice(req.Messages)); err != nil {
		return nil, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Stream(ctx, req)
}

var _ ModelClient = (*savingClient)(nil)
