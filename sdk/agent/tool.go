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
