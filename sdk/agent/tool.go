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
	// Result is used instead of Invoke when set: for a tool that hands the
	// model files, such as an image it read, as well as text.
	Result ResultFunc
	// Preview is how a large result of this tool is shown on a compacting
	// run; zero fields take Spec.Compaction's.
	Preview Preview
}

type InvokeFunc func(ctx context.Context, args json.RawMessage) (string, error)

// ToolResult is what a tool returns with Result: its text, and files for the
// model to see.
//
// The text is the tool's answer to its call. The files reach the model in a
// user message right after the turn's tool results, since the chat
// completions API takes only text in a tool message; each is named by the
// call that attached it.
type ToolResult struct {
	Output string
	Attach []Content
}

type ResultFunc func(ctx context.Context, args json.RawMessage) (ToolResult, error)

// Call runs the tool: Result when it has one, else Invoke. False when it has
// neither, so cannot be called.
func (t Tool) Call(ctx context.Context, args json.RawMessage) (ToolResult, bool, error) {
	switch {
	case t.Result != nil:
		r, err := t.Result(ctx, args)
		return r, true, err
	case t.Invoke != nil:
		out, err := t.Invoke(ctx, args)
		return ToolResult{Output: out}, true, err
	}
	return ToolResult{}, false, nil
}

// Attached is the user message that carries what a turn's tools attached, or
// false when none attached anything.
func Attached(results map[string][]Content, order []ToolCall) (Message, bool) {
	var parts []Content
	for _, call := range order {
		files := results[call.ID]
		if len(files) == 0 {
			continue
		}
		parts = append(parts, Content{
			Type: ContentText,
			Text: "Attached by " + Inner(call).Name + " for call " + call.ID + ":",
		})
		parts = append(parts, files...)
	}
	if len(parts) == 0 {
		return Message{}, false
	}
	return Message{Role: RoleUser, Content: parts}, true
}

type toolCallKey struct{}

func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallKey{}).(string)
	return id
}
