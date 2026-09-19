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

func (r *Runner) Start(ctx context.Context, spec Spec, msgs []Message,
	endpoint Endpoint, events chan<- Event,
) *Run {
	run := &Run{box: &steerBox{}, done: make(chan struct{})}

	go func() {
		defer close(run.done)
		result, err := r.run(ctx, spec, msgs, endpoint, events, run.box)
		result.UnappliedSteers = run.box.close()
		run.result, run.err = result, err
	}()

	return run
}

type Run struct {
	box  *steerBox
	done chan struct{}

	result Result
	err    error
}

func (run *Run) Steer(text string) error { return run.box.add(text) }

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

	if err := checkOutput(spec); err != nil {
		return Result{}, err
	}

	client, err := r.clientFor(endpoint)
	if err != nil {
		return Result{}, fmt.Errorf("connecting to %s: %w", endpoint.BaseURL, err)
	}

	sending := client
	if spec.Session != nil {
		sending = &savingClient{inner: client, session: spec.Session}
	}

	steered := &steeringClient{
		inner:   sending,
		box:     box,
		session: spec.Session,
		emit:    func(e Event) { emit(ctx, events, e) },
	}

	var beneath ModelClient = steered
	if spec.OutputSchema != nil && !spec.Parser {
		beneath = &outputClient{inner: steered, schema: spec.OutputSchema}
	}

	counter := &countingClient{inner: beneath}

	if spec.SteerContinuations < 0 {
		return Result{}, fmt.Errorf(
			"Spec.SteerContinuations is %d; a run cannot continue a negative number of times",
			spec.SteerContinuations)
	}

	var result Result
	for turn := 0; ; turn++ {
		this, err := pattern.Run(ctx, Input{
			Spec:     spec,
			Messages: msgs,
			Model:    counter,
			Events:   events,
		})

		result.Output = this.Output
		result.Steps += this.Steps
		result.Usage = counter.spent()
		result.Messages = recordSteers(this.Messages, box.applied())

		if spec.Session != nil {
			if err := spec.Session.Save(ctx, result.Messages); err != nil {
				return result, fmt.Errorf("saving the transcript: %w", err)
			}
		}
		if err != nil {
			return result, err
		}
		if turn >= spec.SteerContinuations {
			return r.finish(ctx, spec, counter, result)
		}

		pending := box.restart()
		if len(pending) == 0 {
			return r.finish(ctx, spec, counter, result)
		}
		msgs = append(slices.Clone(result.Messages), steerMessages(pending)...)
	}
}

func (r *Runner) finish(ctx context.Context, spec Spec, counter *countingClient,
	result Result,
) (Result, error) {
	if spec.OutputSchema == nil {
		return answered(result), nil
	}

	if !spec.Parser {
		result.Structured = structured(result.Output)
		return answered(result), nil
	}

	model := spec.ParserModel
	if model.Name == "" {
		model = spec.Model
	}

	raw, err := parseIntoSchema(ctx, counter, model, spec.OutputSchema, transcript(result.Messages))
	result.Usage = counter.spent()
	if err != nil {
		return answered(result), err
	}
	result.Structured = raw

	return answered(result), nil
}

func transcript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		said := strings.TrimSpace(m.Text())
		if said == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(said)
	}
	return b.String()
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
