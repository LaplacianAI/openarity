package agent

import "context"

type ModelClient interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

type Request struct {
	Model    ModelRef
	System   []Content
	Messages []Message
	Tools    []Tool
}

type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishFilter    FinishReason = "content_filter"
)

type Usage struct {
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
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

type StreamEvent struct {
	Text  string
	Final *Response
}

type Endpoint struct {
	BaseURL string
	APIKey  string
}

type ClientFactory func(Endpoint) (ModelClient, error)
