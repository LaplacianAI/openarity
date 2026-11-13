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
	// Preview is how a large result of this tool is shown on a compacting
	// run; zero fields take Spec.Compaction's.
	Preview Preview
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
