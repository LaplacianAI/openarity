package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeStore is a test double, not a backend. The shipped stores live in their
// own packages under stores/ and are driven through stores/storetest; this one
// exists so the tests in this package can reach a Session without importing a
// package that imports this one.
type fakeStore struct {
	saved   map[string][]Message
	steers  map[string][]string
	takenBy []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{saved: map[string][]Message{}, steers: map[string][]string{}}
}

func (f *fakeStore) Save(_ context.Context, session string, msgs []Message) error {
	f.saved[session] = msgs
	return nil
}

func (f *fakeStore) Messages(_ context.Context, session string) ([]Message, error) {
	return f.saved[session], nil
}

func (f *fakeStore) AddSteer(_ context.Context, session, text string) error {
	f.steers[session] = append(f.steers[session], text)
	return nil
}

func (f *fakeStore) TakeSteers(_ context.Context, session string) ([]string, error) {
	f.takenBy = append(f.takenBy, session)
	out := f.steers[session]
	delete(f.steers, session)
	return out, nil
}

func TestASessionWithNoIDIsRefused(t *testing.T) {
	_, err := Open("", newFakeStore())
	if err == nil {
		t.Fatal("an empty id was accepted; every conversation would land in one bucket")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestASessionWithNoStoreIsRefused(t *testing.T) {
	if _, err := Open("s1", nil); err == nil {
		t.Fatal("a nil Store was accepted, so the first Save would panic mid-run")
	}
}

// The whole job of a Session is to be a Store with the id already filled in.
// Each of the four methods has to pass the id it was opened with, and getting
// one of them wrong would put a conversation's messages under "" while its
// steers went to the right place.
func TestEveryCallCarriesTheIDTheSessionWasOpenedWith(t *testing.T) {
	store := newFakeStore()
	session, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := session.Save(t.Context(), []Message{{Role: RoleUser}}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if len(store.saved["s1"]) != 1 {
		t.Errorf("Save() addressed %v, want s1", keys(store.saved))
	}

	back, err := session.Messages(t.Context())
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 1 {
		t.Errorf("Messages() = %+v, want what Save was given — it asked under another id", back)
	}

	if err := session.Steer(t.Context(), "look in vault.go"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}
	if got := store.steers["s1"]; len(got) != 1 || got[0] != "look in vault.go" {
		t.Errorf("Steer() left %v under s1", got)
	}

	taken, err := session.TakeSteers(t.Context())
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	if len(taken) != 1 {
		t.Errorf("TakeSteers() = %v, want the steer just left", taken)
	}
	if len(store.takenBy) != 1 || store.takenBy[0] != "s1" {
		t.Errorf("TakeSteers() asked for %v, want s1", store.takenBy)
	}
}

func TestTwoSessionsOnOneStoreDoNotSeeEachOther(t *testing.T) {
	store := newFakeStore()
	first, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	second, err := Open("s2", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := first.Steer(t.Context(), "for s1 only"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	got, err := second.TakeSteers(t.Context())
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("s2 took %v, which was left for s1", got)
	}
}

func keys(m map[string][]Message) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// failingStore fails whichever call is named, so a test can assert that a run
// stops rather than carrying on without the durability it asked for.
type failingStore struct {
	inner  Store
	onTake error
	onSave error
}

func (f *failingStore) Save(ctx context.Context, session string, msgs []Message) error {
	if f.onSave != nil {
		return f.onSave
	}
	return f.inner.Save(ctx, session, msgs)
}

func (f *failingStore) Messages(ctx context.Context, session string) ([]Message, error) {
	return f.inner.Messages(ctx, session)
}

func (f *failingStore) AddSteer(ctx context.Context, session, text string) error {
	return f.inner.AddSteer(ctx, session, text)
}

func (f *failingStore) TakeSteers(ctx context.Context, session string) ([]string, error) {
	if f.onTake != nil {
		return nil, f.onTake
	}
	return f.inner.TakeSteers(ctx, session)
}

func sessionOn(t *testing.T, store Store) Session {
	t.Helper()

	session, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	return session
}

// Nobody holds a *Run here. The steer is left in the store by something that
// never saw the run and never could — which is the case a method on a handle
// cannot serve at all.
func TestASteerLeftByAnotherReplicaReachesTheRun(t *testing.T) {
	session := sessionOn(t, newFakeStore())
	if err := session.Steer(t.Context(), "the bug is in vault.go"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := inner.requests()
	if len(requests) != 1 {
		t.Fatalf("the pattern made %d requests, want 1", len(requests))
	}
	var carried bool
	for _, m := range requests[0].Messages {
		if strings.Contains(text(m), "the bug is in vault.go") {
			carried = true
		}
	}
	if !carried {
		t.Errorf("a steer left in the store never reached the model:\n%+v", requests[0].Messages)
	}
}

// A steer collected from a store goes through the same box as one from
// Run.Steer, so it lands where the fix in #76 put it — pinned to the position
// the transcript was at — rather than appended last, which made the model
// re-read it as a fresh instruction every turn and never conclude.
func TestASteerFromAStoreLandsWhereARequestWasNotWhereItEnds(t *testing.T) {
	session := sessionOn(t, newFakeStore())
	if err := session.Steer(t.Context(), "check the lease"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{calls: 2})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "echo", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	second := inner.requests()[1].Messages
	at := -1
	for i, m := range second {
		if strings.Contains(text(m), "check the lease") {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the steer is not in the second request at all:\n%+v", second)
	}
	if at == len(second)-1 {
		t.Errorf("the steer is the last message of the second request. Pinned to where it\n" +
			"arrived it should sit before the turn that followed it, and a steer that stays\n" +
			"last is read as new on every turn")
	}
}

func TestAStoredSteerIsNotDeliveredTwice(t *testing.T) {
	session := sessionOn(t, newFakeStore())
	if err := session.Steer(t.Context(), "only once"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{calls: 3})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "echo", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := inner.requests()
	var carrying int
	for _, req := range requests {
		for _, m := range req.Messages {
			if strings.Contains(text(m), "only once") {
				carrying++
				break
			}
		}
	}
	// It is carried by every request from the one it arrived at onwards, which
	// is what "pinned" means — but the store must only hand it over once.
	if carrying == 0 {
		t.Fatal("the steer never reached the model")
	}
	if got := countSteerMessages(requests[len(requests)-1].Messages, "only once"); got != 1 {
		t.Errorf("the last request carries the steer %d times, want 1 — a store that hands\n"+
			"the same steer over again adds it to the transcript on every turn", got)
	}
}

func countSteerMessages(msgs []Message, want string) int {
	var n int
	for _, m := range msgs {
		if strings.Contains(text(m), want) {
			n++
		}
	}
	return n
}

func TestAStoreThatCannotBeReadStopsTheRun(t *testing.T) {
	session := sessionOn(t, &failingStore{
		inner:  newFakeStore(),
		onTake: errors.New("postgres is down"),
	})

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	_, err = runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the run succeeded with an unreadable steer store, so whoever left a steer\n" +
			"was told it was delivered")
	}
	if !strings.Contains(err.Error(), "postgres is down") {
		t.Errorf("the cause was swallowed: %v", err)
	}
}

// Spec.Session is nil on every run that has not asked for durability, and
// nothing below may call a store at all. A store that fails every call proves
// that better than counting calls would.
func TestWithoutASessionNoStoreIsEverTouched(t *testing.T) {
	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v — a run with no session must behave exactly as it did before", err)
	}
	if result.Output != "finished" {
		t.Errorf("Output = %q, want the answer the pattern produced", result.Output)
	}
}

// savedStore records every transcript handed to Save, so a test can say when
// the saving happened rather than only that it did.
type savedStore struct {
	Store
	saves [][]Message
}

func newSavedStore() *savedStore { return &savedStore{Store: newFakeStore()} }

func (s *savedStore) Save(ctx context.Context, session string, msgs []Message) error {
	s.saves = append(s.saves, slices.Clone(msgs))
	return s.Store.Save(ctx, session, msgs)
}

// Saving once per model request is what sets the granularity of a resume: a
// run that dies mid-step comes back from the step before and redoes the one it
// was in, tool calls included.
func TestTheTranscriptIsSavedBeforeEveryRequestAndOnceAtTheEnd(t *testing.T) {
	store := newSavedStore()
	session := sessionOn(t, store)

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{calls: 3})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "echo", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := len(inner.requests())
	if want := requests + 1; len(store.saves) != want {
		t.Errorf("Save was called %d times across %d requests, want %d — one before each\n"+
			"request, and one when the pattern returns", len(store.saves), requests, want)
	}
}

// The model's last reply only ever appears in the *next* request, so a save
// per request never writes the answer. Without the save when the pattern
// returns, every resumed conversation is missing its final message.
func TestTheAnswerReachesTheStoreAndNotOnlyTheRequests(t *testing.T) {
	store := newSavedStore()
	session := sessionOn(t, store)

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	kept, err := session.Messages(t.Context())
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(kept) != len(result.Messages) {
		t.Fatalf("the store holds %d messages and the result has %d", len(kept), len(result.Messages))
	}
	if last := text(kept[len(kept)-1]); last != "finished" {
		t.Errorf("the saved transcript ends with %q, want the answer", last)
	}
}

// The save has to happen inside the steering client, so the transcript that
// reaches the store already carries the steer. Saved outside it, a steer taken
// from the store and then lost to a crash would be gone from both places.
func TestASteerIsDurableTheMomentItIsTaken(t *testing.T) {
	store := newSavedStore()
	session := sessionOn(t, store)
	if err := session.Steer(t.Context(), "check the lease"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if len(store.saves) == 0 {
		t.Fatal("nothing was saved at all")
	}
	// The first save is the one made before the first request — the same
	// request the steer was collected for.
	if got := countSteerMessages(store.saves[0], "check the lease"); got != 1 {
		t.Errorf("the first save carries the steer %d times, want 1. Taking a steer from\n"+
			"the store and saving without it opens a window where a crash loses it from\n"+
			"the store and from memory at once", got)
	}
}

func TestAStoreThatCannotBeWrittenStopsTheRun(t *testing.T) {
	session := sessionOn(t, &failingStore{
		inner:  newFakeStore(),
		onSave: errors.New("disk is full"),
	})

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	_, err = runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the run succeeded without saving anything, so it was not durable and\n" +
			"nobody was told")
	}
	if !strings.Contains(err.Error(), "disk is full") {
		t.Errorf("the cause was swallowed: %v", err)
	}
}

// A run handed a transcript carries on from it. This is resume, and it needs
// no entry point of its own: Runner.Run has always taken messages.
func TestARunResumesFromWhatAnotherRunSaved(t *testing.T) {
	store := newFakeStore()
	session := sessionOn(t, store)

	// echoPattern, not obliviousPattern: the oblivious one builds its own
	// messages and ignores what it was given, so it could not carry a
	// transcript forward however the runner were wired.
	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	first, err := runner.Run(t.Context(),
		Spec{Pattern: "echo", MaxSteps: 1, Session: session}, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	kept, err := session.Messages(t.Context())
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(kept) != len(first.Messages) {
		t.Fatalf("the store holds %d of %d messages", len(kept), len(first.Messages))
	}

	// A second run, no session at all, handed what the first one left plus the
	// message that prompted it. The saved transcript ends with the assistant's
	// answer, and a provider refuses a conversation with nothing to respond to
	// — "this model does not support assistant message prefill". The fake
	// client here would accept it, so the test models what a real one needs.
	asked := append(slices.Clone(kept), Message{
		Role:    RoleUser,
		Content: []Content{{Type: ContentText, Text: "and the token path?"}},
	})
	second, err := runner.Run(t.Context(),
		Spec{Pattern: "echo", MaxSteps: 1}, asked, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("resuming = %v", err)
	}

	sent := inner.requests()[len(inner.requests())-1].Messages
	if len(sent) < len(kept) {
		t.Errorf("the resumed run sent %d messages having been given %d; it did not carry\n"+
			"the transcript forward", len(sent), len(kept))
	}
	if text(sent[0]) != text(kept[0]) {
		t.Errorf("the resumed run starts at %q, want the saved transcript's first message %q",
			text(sent[0]), text(kept[0]))
	}
	if second.Output == "" {
		t.Error("the resumed run produced no answer")
	}
}

// failNthSave fails one save and lets the rest through, so a test can say
// which save it is asserting about. failingStore cannot: it fails all of them,
// so the run dies at the runner's final save whether or not the client's
// per-request save reported anything.
type failNthSave struct {
	Store
	n    int
	seen int
	err  error
}

func (f *failNthSave) Save(ctx context.Context, session string, msgs []Message) error {
	f.seen++
	if f.seen == f.n {
		return f.err
	}
	return f.Store.Save(ctx, session, msgs)
}

// The first save is the one savingClient makes before the first request. If it
// reports nothing, a run carries on believing it is durable while the store
// holds nothing.
func TestAFailedSaveBeforeARequestStopsTheRun(t *testing.T) {
	session := sessionOn(t, &failNthSave{
		Store: newFakeStore(),
		n:     1,
		err:   errors.New("disk is full"),
	})

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	_, err = runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the save before the first request failed and the run reported success")
	}
	if !strings.Contains(err.Error(), "disk is full") {
		t.Errorf("the cause was swallowed: %v", err)
	}
	if len(inner.requests()) != 0 {
		t.Errorf("the model was called %d times after the transcript could not be saved;\n"+
			"the save comes first so a turn is never spent on a run that is not durable",
			len(inner.requests()))
	}
}

// streamingPattern reaches the model through Stream rather than Complete.
// ReActStreaming is the default pattern, so that is the path most runs take,
// and a client wrapper tested only through Complete is half tested.
type streamingPattern struct{}

func (*streamingPattern) Name() PatternName { return "streaming" }

func (*streamingPattern) Run(ctx context.Context, in Input) (Result, error) {
	msgs := slices.Clone(in.Messages)
	if _, err := in.Model.Stream(ctx, Request{Messages: msgs}); err != nil {
		return Result{}, err
	}
	msgs = append(msgs, Message{
		Role:    RoleAssistant,
		Content: []Content{{Type: ContentText, Text: "streamed"}},
	})
	return Result{Output: "streamed", Messages: msgs}, nil
}

func TestAStreamingRunSavesAndCollectsTheSameWay(t *testing.T) {
	store := newSavedStore()
	session := sessionOn(t, store)
	if err := session.Steer(t.Context(), "through the stream"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &streamingPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "streaming", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := inner.requests()
	if len(requests) != 1 {
		t.Fatalf("the pattern made %d requests, want 1", len(requests))
	}
	if countSteerMessages(requests[0].Messages, "through the stream") != 1 {
		t.Errorf("the steer did not reach a streaming request:\n%+v", requests[0].Messages)
	}
	if len(store.saves) == 0 {
		t.Fatal("a streaming run saved nothing")
	}
	if countSteerMessages(store.saves[0], "through the stream") != 1 {
		t.Errorf("the first save of a streaming run does not carry the steer")
	}
}

func TestAFailedSaveBeforeAStreamStopsTheRun(t *testing.T) {
	session := sessionOn(t, &failNthSave{
		Store: newFakeStore(),
		n:     1,
		err:   errors.New("disk is full"),
	})

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &streamingPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "streaming", MaxSteps: 1, Session: session}
	_, err = runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the save before the stream failed and the run reported success")
	}
	if !strings.Contains(err.Error(), "disk is full") {
		t.Errorf("the cause was swallowed: %v", err)
	}
	if len(inner.requests()) != 0 {
		t.Errorf("the model was streamed %d times after the transcript could not be saved",
			len(inner.requests()))
	}
}

func TestAStoreThatCannotBeReadStopsAStreamingRun(t *testing.T) {
	session := sessionOn(t, &failingStore{
		inner:  newFakeStore(),
		onTake: errors.New("postgres is down"),
	})

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &streamingPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "streaming", MaxSteps: 1, Session: session}
	_, err = runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("a streaming run succeeded with an unreadable steer store")
	}
	if !strings.Contains(err.Error(), "postgres is down") {
		t.Errorf("the cause was swallowed: %v", err)
	}
}
