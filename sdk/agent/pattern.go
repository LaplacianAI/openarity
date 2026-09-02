package agent

import (
	"context"
	"errors"
)

var (
	ErrNoMaxSteps       = errors.New("the spec sets no MaxSteps, so a looping model would run until the context died")
	ErrMaxSteps         = errors.New("the run hit MaxSteps with the model still calling tools")
	ErrTruncated        = errors.New("the model output was truncated at MaxTokens")
	ErrIncompleteStream = errors.New("the stream ended without a final response")
)

type Pattern interface {
	Name() PatternName
	Run(ctx context.Context, in Input) (Result, error)
}

type Input struct {
	Spec     Spec
	Messages []Message
	Model    ModelClient
	Events   chan<- Event
}

func (in Input) Emit(ctx context.Context, e Event) {
	if in.Events == nil {
		return
	}
	select {
	case in.Events <- e:
	case <-ctx.Done():
	}
}
