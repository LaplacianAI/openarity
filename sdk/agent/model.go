package agent

import (
	"context"
	"fmt"
	"net/http"
)

type ModelClient interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

type Request struct {
	Model        ModelRef
	System       []Content
	Messages     []Message
	Tools        []Tool
	OutputSchema *OutputSchema
	// MustCall names the one tool the model must call this turn, for a call
	// a pattern makes to read a tool call back, such as a critique. It is a
	// hint: a client drops it for a model that refuses to be told, as Claude
	// does while it thinks, so a pattern still reads whatever comes back.
	MustCall string
}

type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishFilter    FinishReason = "content_filter"
)

// Usage is what a call read and wrote. InputTokens is everything the model
// read, CachedInputTokens and CacheWriteTokens among it: a token read from
// the provider's cache, and one written to it on this call.
type Usage struct {
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	CacheWriteTokens  int
}

type Response struct {
	Message Message
	Finish  FinishReason
	Usage   Usage
}

type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Close() error
}

// StreamEvent is a piece of a turn as it arrives: some of the model's
// reasoning, some of its text, or both, and at the end the whole response.
type StreamEvent struct {
	Text      string
	Reasoning string
	Final     *Response
}

type Endpoint struct {
	BaseURL string
	APIKey  string
}

type ClientFactory func(Endpoint) (ModelClient, error)

type ModelError struct {
	Status int
	Code   string
}

func (e *ModelError) Error() string {
	answered := fmt.Sprintf("the model endpoint answered %d %s", e.Status, http.StatusText(e.Status))
	if e.Code == "" {
		return answered
	}
	return answered + " (" + e.Code + ")"
}

func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.CacheWriteTokens += other.CacheWriteTokens
}
