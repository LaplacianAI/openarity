package agent

import (
	"context"
	"fmt"
)

type savingClient struct {
	inner   ModelClient
	session Session
}

func (c *savingClient) Complete(ctx context.Context, req Request) (Response, error) {
	if err := c.session.Save(ctx, req.Messages); err != nil {
		return Response{}, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Complete(ctx, req)
}

func (c *savingClient) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := c.session.Save(ctx, req.Messages); err != nil {
		return nil, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Stream(ctx, req)
}

var _ ModelClient = (*savingClient)(nil)
