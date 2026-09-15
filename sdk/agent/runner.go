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
	patterns  map[PatternName]Pattern
}

func New(clientFor ClientFactory, patterns ...Pattern) (*Runner, error) {
	if clientFor == nil {
		return nil, errors.New("no ClientFactory was given, so no run could ever reach a model")
	}
	if len(patterns) == 0 {
		return nil, errors.New("no patterns were registered, so every run would be refused")
	}

	byName := make(map[PatternName]Pattern, len(patterns))
	for _, l := range patterns {
		if l == nil {
			return nil, errors.New("a nil Pattern was registered")
		}
		name := l.Name()
		if name == "" {
			return nil, fmt.Errorf("a %T registered under an empty name", l)
		}
		if existing, taken := byName[name]; taken {
			return nil, fmt.Errorf("%T and %T both claim the pattern name %q", existing, l, name)
		}
		byName[name] = l
	}

	return &Runner{clientFor: clientFor, patterns: byName}, nil
}

// Start runs the same thing Run does, and hands back a handle while it works.
//
// Run is the whole API for a run you only wait for; this is for one you want
// to say something to. It is additive on purpose — Run's signature is released
// as v0.1.0 and already takes five arguments, so steering arrives as a second
// entry point rather than a sixth parameter.
//
// The caller still owns events and still closes it. Wait blocks until the run
// is over and may be called more than once.
func (r *Runner) Start(ctx context.Context, spec Spec, msgs []Message,
	endpoint Endpoint, events chan<- Event,
) *Run {
	run := &Run{box: &steerBox{}, done: make(chan struct{})}

	go func() {
		defer close(run.done)
		result, err := r.run(ctx, spec, msgs, endpoint, events, run.box)

		// Drained after the pattern has returned, so anything still waiting is
		// something no request will ever carry. Reported rather than dropped.
		result.UnappliedSteers = run.box.close()
		run.result, run.err = result, err
	}()

	return run
}

// A Run is a run in progress. Steer may be called from any goroutine —
// typically the one reading events, which is how a caller notices a run going
// the wrong way in the first place.
type Run struct {
	box  *steerBox
	done chan struct{}

	result Result
	err    error
}

// Steer says something to a run that has already started.
//
// It does not interrupt anything. The text waits, and is added to the next
// request the pattern makes — onto the end of a tool result when one is being
// sent, and as a user message otherwise.
//
// ErrRunFinished means there is no next request: the run is over, or over by
// the time this was called. A steer sent during the model's last call is not
// lost, but it is not applied either — it comes back on Result.UnappliedSteers.
func (run *Run) Steer(text string) error { return run.box.add(text) }

// Wait blocks until the run finishes and returns what it returned.
func (run *Run) Wait() (Result, error) {
	<-run.done
	return run.result, run.err
}

func (r *Runner) Run(ctx context.Context, spec Spec, msgs []Message,
	endpoint Endpoint, events chan<- Event,
) (Result, error) {
	return r.Start(ctx, spec, msgs, endpoint, events).Wait()
}

func (r *Runner) run(ctx context.Context, spec Spec, msgs []Message,
	endpoint Endpoint, events chan<- Event, box *steerBox,
) (Result, error) {
	if spec.Pattern == "" {
		return Result{}, fmt.Errorf("the spec names no pattern; set Spec.Pattern to one of: %s",
			strings.Join(r.registered(), ", "))
	}

	pattern, ok := r.patterns[spec.Pattern]
	if !ok {
		return Result{}, fmt.Errorf("no pattern named %q is registered; this runner has %s",
			spec.Pattern, strings.Join(r.registered(), ", "))
	}

	spec, err := withSkills(spec)
	if err != nil {
		return Result{}, err
	}

	client, err := r.clientFor(endpoint)
	if err != nil {
		return Result{}, fmt.Errorf("connecting to %s: %w", endpoint.BaseURL, err)
	}

	// Two wrappers, for the same reason: a pattern reaches the model through
	// this interface and nothing else, so anything that must happen on every
	// call belongs here rather than in a loop somebody has to remember to
	// write. Steering goes inside the counter — it edits the request on its
	// way out, and what the call costs is still measured at the call.
	steered := &steeringClient{
		inner: client,
		box:   box,
		emit:  func(e Event) { emit(ctx, events, e) },
	}

	// Wrapped so no pattern has to do its own accounting. A pattern written
	// outside this module gets the right number without knowing to try.
	counter := &countingClient{inner: steered}

	result, err := pattern.Run(ctx, Input{
		Spec:     spec,
		Messages: msgs,
		Model:    counter,
		Events:   events,
	})

	// Set after the pattern returns and on the error path too: what a run that
	// failed half way spent is still owed. This is the authoritative figure —
	// a pattern may total its own for anyone calling it directly, but the
	// count taken at the client is the one that cannot miss a call.
	result.Usage = counter.spent()
	return result, err
}

func withSkills(spec Spec) (Spec, error) {
	if len(spec.Skills) == 0 {
		return spec, nil
	}

	for _, t := range spec.Tools {
		if t.Name == SkillToolName {
			return Spec{}, fmt.Errorf("a tool is already named %q, which is the name skills arrive under", SkillToolName)
		}
	}

	tool, listing, err := skillTool(spec.Skills)
	if err != nil {
		return Spec{}, err
	}

	spec.Tools = append(slices.Clone(spec.Tools), tool)
	spec.System = append(slices.Clone(spec.System), listing)

	return spec, nil
}

func (r *Runner) registered() []string {
	names := make([]string, 0, len(r.patterns))
	for name := range r.patterns {
		names = append(names, string(name))
	}
	slices.Sort(names)
	return names
}
