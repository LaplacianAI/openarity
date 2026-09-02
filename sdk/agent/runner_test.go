package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakePattern records what it was handed. Everything the Runner decides — which
// pattern, what reaches it, how a clash is reported — happens before any pattern runs,
// so a fake that only records is enough.
type fakePattern struct {
	name PatternName
	err  error

	mu   sync.Mutex
	sawn []Input
}

func (l *fakePattern) Name() PatternName { return l.name }

func (l *fakePattern) Run(_ context.Context, in Input) (Result, error) {
	l.mu.Lock()
	l.sawn = append(l.sawn, in)
	l.mu.Unlock()
	if l.err != nil {
		return Result{}, l.err
	}
	return Result{Output: string(l.name), Steps: 1}, nil
}

func (l *fakePattern) calls() []Input {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Input(nil), l.sawn...)
}

type nilNamePattern struct{}

func (nilNamePattern) Name() PatternName                          { return "" }
func (nilNamePattern) Run(context.Context, Input) (Result, error) { return Result{}, nil }

// fakeFactory records the endpoints it was asked for and hands back one client.
type fakeFactory struct {
	mu   sync.Mutex
	seen []Endpoint
	err  error
}

func (f *fakeFactory) build(e Endpoint) (ModelClient, error) {
	f.mu.Lock()
	f.seen = append(f.seen, e)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return fakeClient{}, nil
}

func (f *fakeFactory) endpoints() []Endpoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Endpoint(nil), f.seen...)
}

// clients is a factory for tests that do not care which endpoint was asked for.
func clients() ClientFactory {
	return func(Endpoint) (ModelClient, error) { return fakeClient{}, nil }
}

type fakeClient struct{}

func (fakeClient) Complete(context.Context, Request) (Response, error) { return Response{}, nil }
func (fakeClient) Stream(context.Context, Request) (Stream, error)     { return nil, nil }

// A Runner with nothing registered refuses every run, which presents as a
// healthy process that answers no request. The refusal belongs at wiring.
func TestARunnerWithNoLoopsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(clients())
	if err == nil {
		t.Fatal("New accepted a runner with no patterns")
	}
	if !strings.Contains(err.Error(), "no patterns") {
		t.Errorf("err = %v, want it to say no patterns were registered", err)
	}
}

func TestANilLoopIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := New(clients(), &fakePattern{name: "react"}, nil); err == nil {
		t.Fatal("New accepted a nil Pattern")
	}
}

// An empty name is silent otherwise: it registers under "", and no Spec can
// ever name it.
func TestALoopWithNoNameIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(clients(), nilNamePattern{})
	if err == nil {
		t.Fatal("New accepted a pattern with no name")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("err = %v, want it to say the name was empty", err)
	}
}

// The last registration wins in a map, so without this the loser is
// constructed, looks wired, and simply never runs. Two authored patterns both
// named "custom" are the case this catches.
func TestTwoLoopsCannotClaimTheSameName(t *testing.T) {
	t.Parallel()

	_, err := New(clients(), &fakePattern{name: "custom"}, &fakePattern{name: "custom"})
	if err == nil {
		t.Fatal("New accepted two patterns claiming one name")
	}
	if !strings.Contains(err.Error(), `"custom"`) {
		t.Errorf("the clash error does not name the pattern: %v", err)
	}
	// Both claimants, because knowing there is a clash without knowing who is
	// in it means reading every registration to find out.
	if strings.Count(err.Error(), "fakePattern") != 2 {
		t.Errorf("the clash error does not name both claimants: %v", err)
	}
}

func TestRunDispatchesToTheNamedLoop(t *testing.T) {
	t.Parallel()

	react := &fakePattern{name: PatternReAct}
	code := &fakePattern{name: PatternCode}
	r, err := New(clients(), react, code)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := r.Run(t.Context(), Spec{Pattern: PatternCode, MaxSteps: 1}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != string(PatternCode) {
		t.Errorf("Output = %q, want the code pattern to have run", got.Output)
	}
	if len(react.calls()) != 0 {
		t.Error("the react pattern ran for a spec that named code")
	}
}

// An unregistered pattern must fail loudly rather than fall through to whatever is
// registered — a config naming a pattern nobody built should not look like it
// worked.
func TestAnUnregisteredLoopIsRefusedAndSaysWhatExists(t *testing.T) {
	t.Parallel()

	r, err := New(clients(), &fakePattern{name: PatternReAct}, &fakePattern{name: PatternCode})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = r.Run(t.Context(), Spec{Pattern: "reflexion", MaxSteps: 1}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil)
	if err == nil {
		t.Fatal("Run accepted a pattern nobody registered")
	}
	for _, want := range []string{"reflexion", "react", "code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// Go randomises map iteration, so an unsorted list makes a test asserting this
// message flap rather than fail.
func TestTheRegisteredListIsStable(t *testing.T) {
	t.Parallel()

	r, err := New(clients(), &fakePattern{name: "zebra"}, &fakePattern{name: "alpha"}, &fakePattern{name: "mike"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := strings.Join(r.registered(), ",")
	if first != "alpha,mike,zebra" {
		t.Fatalf("registered() = %q, want it sorted", first)
	}
	for range 20 {
		if got := strings.Join(r.registered(), ","); got != first {
			t.Fatalf("registered() returned %q then %q", first, got)
		}
	}
}

// The endpoint travels with the run, not with the Runner. A team with its own
// gateway and its own credential is a per-run fact, and storing one on the
// Runner would send every team through one endpoint.
func TestTheEndpointReachesTheFactoryAndTheClientReachesTheLoop(t *testing.T) {
	t.Parallel()

	pattern := &fakePattern{name: PatternReAct}
	factory := &fakeFactory{}
	r, err := New(factory.build, pattern)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events := make(chan Event, 1)
	spec := Spec{Pattern: PatternReAct, Model: ModelRef{Name: "opus"}, MaxSteps: 4}
	msgs := []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "hi"}}}}
	endpoint := Endpoint{BaseURL: "http://litellm:4000/v1", APIKey: "sk-team-a"}

	if _, err := r.Run(t.Context(), spec, msgs, endpoint, events); err != nil {
		t.Fatalf("Run: %v", err)
	}

	asked := factory.endpoints()
	if len(asked) != 1 {
		t.Fatalf("the factory was asked %d times, want 1", len(asked))
	}
	if asked[0] != endpoint {
		t.Errorf("the factory was given %+v, want %+v", asked[0], endpoint)
	}

	calls := pattern.calls()
	if len(calls) != 1 {
		t.Fatalf("the pattern ran %d times, want 1", len(calls))
	}
	in := calls[0]
	if in.Model == nil {
		t.Error("the pattern was given no client")
	}
	if in.Events == nil {
		t.Error("the pattern was not given the events channel")
	}
	if in.Spec.Model.Name != "opus" || in.Spec.MaxSteps != 4 {
		t.Errorf("the spec did not arrive intact: %+v", in.Spec)
	}
	if len(in.Messages) != 1 {
		t.Errorf("the messages did not arrive: %+v", in.Messages)
	}
}

func TestALoopsErrorIsReturnedUnwrapped(t *testing.T) {
	t.Parallel()

	boom := errors.New("the model refused")
	r, err := New(clients(), &fakePattern{name: PatternReAct, err: boom})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.Run(t.Context(), Spec{Pattern: PatternReAct}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the pattern's own error", err)
	}
}

// One Runner serves every agent in the process, and patterns hold no per-run
// state. Under -race this is what catches a pattern that quietly grows a field.
func TestOneRunnerServesManyAgentsAtOnce(t *testing.T) {
	t.Parallel()

	react := &fakePattern{name: PatternReAct}
	code := &fakePattern{name: PatternCode}
	r, err := New(clients(), react, code)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const runs = 50
	var wg sync.WaitGroup
	errs := make(chan error, runs)

	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Alternating patterns and distinct specs: the same pattern value serves
			// two different agents while a third agent switches patterns.
			pattern := PatternReAct
			if i%3 == 0 {
				pattern = PatternCode
			}
			spec := Spec{
				Pattern:  pattern,
				Model:    ModelRef{Name: fmt.Sprintf("model-%d", i)},
				MaxSteps: i + 1,
			}
			if _, err := r.Run(t.Context(), spec, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a concurrent run failed: %v", err)
	}
	if total := len(react.calls()) + len(code.calls()); total != runs {
		t.Errorf("%d runs reached a pattern, want %d", total, runs)
	}

	// Every run kept its own spec: a pattern sharing state would show up as two
	// runs seeing the same model name.
	seen := map[string]bool{}
	for _, in := range append(react.calls(), code.calls()...) {
		if seen[in.Spec.Model.Name] {
			t.Fatalf("two runs saw the same spec: %q", in.Spec.Model.Name)
		}
		seen[in.Spec.Model.Name] = true
	}
}

// A Runner with no way to build a client refuses every run. The refusal belongs
// at wiring, where somebody is reading a stack trace, not at 3am.
func TestARunnerWithNoFactoryIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(nil, &fakePattern{name: PatternReAct})
	if err == nil {
		t.Fatal("New accepted a runner with no ClientFactory")
	}
	if !strings.Contains(err.Error(), "ClientFactory") {
		t.Errorf("err = %v, want it to name what is missing", err)
	}
}

// A gateway that cannot be reached must name itself. "connection refused" alone
// sends whoever is on call to check every team's configuration.
func TestAFactoryFailureNamesTheEndpoint(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")
	factory := &fakeFactory{err: boom}
	r, err := New(factory.build, &fakePattern{name: PatternReAct})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = r.Run(t.Context(), Spec{Pattern: PatternReAct},
		nil, Endpoint{BaseURL: "http://litellm:4000/v1", APIKey: "sk-x"}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the factory's own error", err)
	}
	if !strings.Contains(err.Error(), "http://litellm:4000/v1") {
		t.Errorf("the error does not name the endpoint: %v", err)
	}
	// The key is a credential and has no business in an error that reaches a log.
	if strings.Contains(err.Error(), "sk-x") {
		t.Errorf("the API key reached the error message: %v", err)
	}
}

// An unregistered pattern must be refused before a connection is opened — there is
// nothing to connect for.
func TestAnUnregisteredLoopNeverReachesTheFactory(t *testing.T) {
	t.Parallel()

	factory := &fakeFactory{}
	r, err := New(factory.build, &fakePattern{name: PatternReAct})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.Run(t.Context(), Spec{Pattern: "reflexion"},
		nil, Endpoint{BaseURL: "http://gateway/v1"}, nil); err == nil {
		t.Fatal("Run accepted a pattern nobody registered")
	}
	if n := len(factory.endpoints()); n != 0 {
		t.Errorf("the factory was called %d times for a run that could not happen", n)
	}
}

// The pattern must not have to know skills exist. By the time it sees the Spec,
// they are a tool and a block of system prompt like any other.
func TestSkillsReachTheLoopAsAToolAndAListing(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{
		System: System("You are a terse assistant."),
		Tools:  []Tool{{Name: "count_issues"}},
		Skills: []Skill{{
			Name:        "pdf-forms",
			Description: "Fill in the fields of a PDF form",
			Body:        func(context.Context) (string, error) { return "instructions", nil },
		}},
	})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}

	if len(spec.Tools) != 2 {
		t.Fatalf("the pattern sees %d tools, want the caller's plus the skill tool", len(spec.Tools))
	}
	if spec.Tools[0].Name != "count_issues" {
		t.Errorf("the caller's tool moved to %q", spec.Tools[0].Name)
	}
	if spec.Tools[1].Name != SkillToolName {
		t.Errorf("the skill tool is named %q, want %q", spec.Tools[1].Name, SkillToolName)
	}

	if len(spec.System) != 2 {
		t.Fatalf("the pattern sees %d system blocks, want the caller's plus the listing", len(spec.System))
	}
	if !strings.Contains(spec.System[1].Text, "pdf-forms") {
		t.Errorf("the listing does not name the skill:\n%s", spec.System[1].Text)
	}
}

// The caller's system prompt is the same for every team in the deployment and
// caches once; the listing differs per team. Prepending the listing would put
// the per-team block at the front and no team would share a prefix again.
func TestTheListingGoesAfterTheCallersSystemPrompt(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{
		System: System("You are a terse assistant."),
		Skills: []Skill{{
			Name: "pdf-forms", Description: "Fill PDFs",
			Body: func(context.Context) (string, error) { return "x", nil },
		}},
	})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}
	if spec.System[0].Text != "You are a terse assistant." {
		t.Errorf("the first block is %q, want the caller's prompt", spec.System[0].Text)
	}
}

// append into spare capacity writes into the caller's array. A Spec reused for
// a second run would come back carrying the first run's skill tool.
func TestARunDoesNotEditTheSpecItWasGiven(t *testing.T) {
	t.Parallel()

	tools := make([]Tool, 1, 4)
	tools[0] = Tool{Name: "count_issues"}
	system := make([]Content, 1, 4)
	system[0] = Content{Type: ContentText, Text: "You are a terse assistant."}

	original := Spec{
		System: system,
		Tools:  tools,
		Skills: []Skill{{
			Name: "pdf-forms", Description: "Fill PDFs",
			Body: func(context.Context) (string, error) { return "x", nil },
		}},
	}

	if _, err := withSkills(original); err != nil {
		t.Fatalf("withSkills: %v", err)
	}

	if len(original.Tools) != 1 || len(original.System) != 1 {
		t.Fatal("the caller's slices grew")
	}
	// Reach past the length the caller holds: with a shared array the skill
	// tool is sitting there, waiting for the caller's own next append.
	if got := tools[:2][1].Name; got != "" {
		t.Errorf("the skill tool was written into the caller's array at index 1 as %q", got)
	}
	if got := system[:2][1].Text; got != "" {
		t.Errorf("the listing was written into the caller's array at index 1")
	}
}

// Nothing is added when nothing was offered. An empty listing would spend
// prompt on a heading with no skills under it, and the tool's enum would be
// empty, which some gateways reject outright.
func TestARunWithNoSkillsIsLeftUntouched(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{
		System: System("You are a terse assistant."),
		Tools:  []Tool{{Name: "count_issues"}},
	})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}
	if len(spec.Tools) != 1 || len(spec.System) != 1 {
		t.Errorf("a run with no skills got %d tools and %d system blocks, want 1 and 1",
			len(spec.Tools), len(spec.System))
	}
}

// Two tools under one name and the pattern dispatches by whichever it finds
// first, which is a map iteration away from being different next run.
func TestACallersToolNamedSkillIsRefused(t *testing.T) {
	t.Parallel()

	_, err := withSkills(Spec{
		Tools: []Tool{{Name: SkillToolName}},
		Skills: []Skill{{
			Name: "pdf-forms", Description: "Fill PDFs",
			Body: func(context.Context) (string, error) { return "x", nil },
		}},
	})
	if err == nil {
		t.Fatal("a tool clashing with the skill tool was accepted")
	}
	if !strings.Contains(err.Error(), SkillToolName) {
		t.Errorf("err = %v, want it to name the clash", err)
	}
}

// A malformed skill list is a wiring mistake, and saying so should not need a
// connection to a gateway first.
func TestABadSkillListIsRefusedBeforeTheClientIsBuilt(t *testing.T) {
	t.Parallel()

	dialled := false
	runner, err := New(func(Endpoint) (ModelClient, error) {
		dialled = true
		return nil, nil
	}, &fakePattern{name: PatternReAct})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = runner.Run(t.Context(), Spec{
		Pattern: PatternReAct,
		Skills: []Skill{
			{Name: "pdf-forms", Description: "Fill PDFs", Body: func(context.Context) (string, error) { return "x", nil }},
			{Name: "pdf-forms", Description: "Something else", Body: func(context.Context) (string, error) { return "y", nil }},
		},
	}, nil, Endpoint{BaseURL: "http://litellm:4000/v1"}, nil)

	if err == nil {
		t.Fatal("a duplicate skill name was accepted")
	}
	if dialled {
		t.Error("the runner opened a connection to report a mistake in the spec")
	}
}
