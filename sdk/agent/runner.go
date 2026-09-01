package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Runner struct {
	clientFor ClientFactory
	loops     map[LoopType]Loop
}

func New(clientFor ClientFactory, loops ...Loop) (*Runner, error) {
	if clientFor == nil {
		return nil, errors.New("no ClientFactory was given, so no run could ever reach a model")
	}
	if len(loops) == 0 {
		return nil, errors.New("no loops were registered, so every run would be refused")
	}

	byName := make(map[LoopType]Loop, len(loops))
	for _, l := range loops {
		if l == nil {
			return nil, errors.New("a nil Loop was registered")
		}
		name := l.Name()
		if name == "" {
			return nil, fmt.Errorf("a %T registered under an empty name", l)
		}
		if existing, taken := byName[name]; taken {
			return nil, fmt.Errorf("%T and %T both claim the loop name %q", existing, l, name)
		}
		byName[name] = l
	}

	return &Runner{clientFor: clientFor, loops: byName}, nil
}

func (r *Runner) Run(ctx context.Context, spec Spec, msgs []Message,
	endpoint Endpoint, events chan<- Event) (Result, error) {
	loop, ok := r.loops[spec.Loop]
	if !ok {
		return Result{}, fmt.Errorf("no loop named %q is registered; this runner has %s",
			spec.Loop, strings.Join(r.registered(), ", "))
	}

	client, err := r.clientFor(endpoint)
	if err != nil {
		return Result{}, fmt.Errorf("connecting to %s: %w", endpoint.BaseURL, err)
	}

	return loop.Run(ctx, Input{
		Spec:     spec,
		Messages: msgs,
		Model:    client,
		Events:   events,
	})
}

func (r *Runner) registered() []string {
	names := make([]string, 0, len(r.loops))
	for name := range r.loops {
		names = append(names, string(name))
	}
	slices.Sort(names)
	return names
}
