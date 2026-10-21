package agent

import (
	"context"
	"encoding/json"
)

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Invoke      InvokeFunc
}

type InvokeFunc func(ctx context.Context, args json.RawMessage) (string, error)

type toolCallKey struct{}

func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallKey{}).(string)
	return id
}
