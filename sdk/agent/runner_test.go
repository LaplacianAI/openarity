package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeLoop records what it was handed. Everything the Runner decides — which
// loop, what reaches it, how a clash is reported — happens before any loop runs,
// so a fake that only records is enough.
type fakeLoop struct {
	name LoopType
	err  error

	mu   sync.Mutex
	sawn []Input
}

func (l *fakeLoop) Name() LoopType { return l.name }

func (l *fakeLoop) Run(_ context.Context, in Input) (Result, error) {
	l.mu.Lock()
	l.sawn = append(l.sawn, in)
	l.mu.Unlock()
	if l.err != nil {
		return Result{}, l.err
	}
	return Result{Output: string(l.name), Steps: 1}, nil
}

func (l *fakeLoop) calls() []Input {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Input(nil), l.sawn...)
}

type nilNameLoop struct{}

func (nilNameLoop) Name() LoopType                             { return "" }
func (nilNameLoop) Run(context.Context, Input) (Result, error) { return Result{}, nil }

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
		t.Fatal("New accepted a runner with no loops")
	}
	if !strings.Contains(err.Error(), "no loops") {
		t.Errorf("err = %v, want it to say no loops were registered", err)
	}
}

func TestANilLoopIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := New(clients(), &fakeLoop{name: "react"}, nil); err == nil {
		t.Fatal("New accepted a nil Loop")
	}
}

// An empty name is silent otherwise: it registers under "", and no Spec can
// ever name it.
func TestALoopWithNoNameIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(clients(), nilNameLoop{})
	if err == nil {
		t.Fatal("New accepted a loop with no name")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("err = %v, want it to say the name was empty", err)
	}
}

// The last registration wins in a map, so without this the loser is
// constructed, looks wired, and simply never runs. Two authored loops both
// named "custom" are the case this catches.
func TestTwoLoopsCannotClaimTheSameName(t *testing.T) {
	t.Parallel()

	_, err := New(clients(), &fakeLoop{name: "custom"}, &fakeLoop{name: "custom"})
	if err == nil {
		t.Fatal("New accepted two loops claiming one name")
	}
	if !strings.Contains(err.Error(), `"custom"`) {
		t.Errorf("the clash error does not name the loop: %v", err)
	}
	// Both claimants, because knowing there is a clash without knowing who is
	// in it means reading every registration to find out.
	if strings.Count(err.Error(), "fakeLoop") != 2 {
		t.Errorf("the clash error does not name both claimants: %v", err)
	}
}

func TestRunDispatchesToTheNamedLoop(t *testing.T) {
	t.Parallel()

	react := &fakeLoop{name: LoopReAct}
	code := &fakeLoop{name: LoopCode}
	r, err := New(clients(), react, code)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := r.Run(t.Context(), Spec{Loop: LoopCode, MaxSteps: 1}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != string(LoopCode) {
		t.Errorf("Output = %q, want the code loop to have run", got.Output)
	}
	if len(react.calls()) != 0 {
		t.Error("the react loop ran for a spec that named code")
	}
}

// An unregistered loop must fail loudly rather than fall through to whatever is
// registered — a config naming a loop nobody built should not look like it
// worked.
func TestAnUnregisteredLoopIsRefusedAndSaysWhatExists(t *testing.T) {
	t.Parallel()

	r, err := New(clients(), &fakeLoop{name: LoopReAct}, &fakeLoop{name: LoopCode})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = r.Run(t.Context(), Spec{Loop: "reflexion", MaxSteps: 1}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil)
	if err == nil {
		t.Fatal("Run accepted a loop nobody registered")
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

	r, err := New(clients(), &fakeLoop{name: "zebra"}, &fakeLoop{name: "alpha"}, &fakeLoop{name: "mike"})
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

	loop := &fakeLoop{name: LoopReAct}
	factory := &fakeFactory{}
	r, err := New(factory.build, loop)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events := make(chan Event, 1)
	spec := Spec{Loop: LoopReAct, Model: ModelRef{Name: "opus"}, MaxSteps: 4}
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

	calls := loop.calls()
	if len(calls) != 1 {
		t.Fatalf("the loop ran %d times, want 1", len(calls))
	}
	in := calls[0]
	if in.Model == nil {
		t.Error("the loop was given no client")
	}
	if in.Events == nil {
		t.Error("the loop was not given the events channel")
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
	r, err := New(clients(), &fakeLoop{name: LoopReAct, err: boom})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.Run(t.Context(), Spec{Loop: LoopReAct}, nil, Endpoint{BaseURL: "http://gateway/v1"}, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the loop's own error", err)
	}
}

// One Runner serves every agent in the process, and loops hold no per-run
// state. Under -race this is what catches a loop that quietly grows a field.
func TestOneRunnerServesManyAgentsAtOnce(t *testing.T) {
	t.Parallel()

	react := &fakeLoop{name: LoopReAct}
	code := &fakeLoop{name: LoopCode}
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

			// Alternating loops and distinct specs: the same loop value serves
			// two different agents while a third agent switches loops.
			loop := LoopReAct
			if i%3 == 0 {
				loop = LoopCode
			}
			spec := Spec{
				Loop:     loop,
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
		t.Errorf("%d runs reached a loop, want %d", total, runs)
	}

	// Every run kept its own spec: a loop sharing state would show up as two
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

	_, err := New(nil, &fakeLoop{name: LoopReAct})
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
	r, err := New(factory.build, &fakeLoop{name: LoopReAct})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = r.Run(t.Context(), Spec{Loop: LoopReAct},
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

// An unregistered loop must be refused before a connection is opened — there is
// nothing to connect for.
func TestAnUnregisteredLoopNeverReachesTheFactory(t *testing.T) {
	t.Parallel()

	factory := &fakeFactory{}
	r, err := New(factory.build, &fakeLoop{name: LoopReAct})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := r.Run(t.Context(), Spec{Loop: "reflexion"},
		nil, Endpoint{BaseURL: "http://gateway/v1"}, nil); err == nil {
		t.Fatal("Run accepted a loop nobody registered")
	}
	if n := len(factory.endpoints()); n != 0 {
		t.Errorf("the factory was called %d times for a run that could not happen", n)
	}
}
