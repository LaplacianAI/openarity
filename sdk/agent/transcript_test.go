package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
)

// handingPattern sends what it was given to the model once and keeps it, so a
// test sees exactly what a pattern receives.
type handingPattern struct {
	mu    sync.Mutex
	got   [][]Message
	after func() // called once, after the first turn's call
}

func (*handingPattern) Name() PatternName { return "handing" }

func (p *handingPattern) Run(ctx context.Context, in Input) (Result, error) {
	p.mu.Lock()
	p.got = append(p.got, slices.Clone(in.Messages))
	p.mu.Unlock()
	resp, err := in.Model.Complete(ctx, Request{Model: in.Spec.Model, Messages: in.Messages})
	if err != nil {
		return Result{}, err
	}
	p.mu.Lock()
	after := p.after
	p.after = nil
	p.mu.Unlock()
	if after != nil {
		after()
	}
	return Result{Output: resp.Message.Text(), Messages: append(slices.Clone(in.Messages), resp.Message), Steps: 1}, nil
}

func (p *handingPattern) received() [][]Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.got)
}

func summariesIn(msgs []Message) []CompactionSummary {
	var out []CompactionSummary
	for _, m := range msgs {
		if m.Compaction != nil {
			out = append(out, *m.Compaction)
		}
	}
	return out
}

func textOf(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}

func withoutSummaries(msgs []Message) []Message {
	return slices.DeleteFunc(slices.Clone(msgs), func(m Message) bool { return m.Compaction != nil })
}

type transcriptRig struct {
	model   *scripted
	pattern *handingPattern
	runner  *Runner
}

func newTranscriptRig(t *testing.T) *transcriptRig {
	t.Helper()
	r := &transcriptRig{model: &scripted{}, pattern: &handingPattern{}}
	runner, err := New(func(Endpoint) (ModelClient, error) { return r.model, nil }, r.pattern)
	if err != nil {
		t.Fatal(err)
	}
	r.runner = runner
	return r
}

var compacting = Spec{Pattern: "handing", MaxSteps: 1, Compaction: &Compaction{AfterMessages: 10, KeepTokens: 10}}

func (r *transcriptRig) run(t *testing.T, spec Spec, msgs []Message) (Result, *Run) {
	t.Helper()
	run := r.runner.Start(t.Context(), spec, msgs, Endpoint{}, nil)
	result, err := run.Wait()
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return result, run
}

func TestASummaryIsWrittenIntoTheTranscriptWhereItsTurnsEnd(t *testing.T) {
	r := newTranscriptRig(t)
	msgs := conversation(10, func(int) string { return "x" })

	result, run := r.run(t, compacting, msgs)

	marks := summariesIn(result.Messages)
	if len(marks) != 1 || marks[0].Summary != "Goal: the task." || run.Summary() != "Goal: the task." {
		t.Fatalf("the transcript holds %+v and Summary() = %q; want the one summary", marks, run.Summary())
	}
	at := slices.IndexFunc(result.Messages, func(m Message) bool { return m.Compaction != nil })
	if at < 2 || result.Messages[at+1].Role == RoleTool || marks[0].Prefix != digest(msgs[:at]) {
		t.Fatalf("the summary sits at %d; want it where the turns it stands for end", at)
	}
	if plain := withoutSummaries(result.Messages); digest(plain[:len(msgs)]) != digest(msgs) || len(plain) != len(msgs)+1 {
		t.Fatalf("taking the summary out does not give back every message the run had")
	}
}

func TestARunCarriesOnFromTheSummaryInItsMessagesWithoutAStore(t *testing.T) {
	first := newTranscriptRig(t)
	done, _ := first.run(t, compacting, conversation(10, func(int) string { return "x" }))
	reqs := first.model.requests()
	before := reqs[len(reqs)-1].Messages

	again := newTranscriptRig(t)
	_, run := again.run(t, compacting, done.Messages)

	if again.model.summaries() != 0 || run.Summary() != "Goal: the task." {
		t.Fatalf("a run given its own transcript summarised %d time(s); want none", again.model.summaries())
	}
	sent := again.model.requests()[0].Messages
	if len(sent) != len(before)+1 || digest(sent[:len(before)]) != digest(before) {
		t.Fatalf("the resumed run sent %d messages that do not start with the %d the first one last sent",
			len(sent), len(before))
	}
	for _, got := range again.pattern.received() {
		if len(summariesIn(got)) != 0 {
			t.Fatalf("the pattern was handed a summary message; it works on the turns alone")
		}
	}
}

// keeping is the smallest Store: one session, kept as it was saved.
type keeping struct {
	mu   sync.Mutex
	msgs []Message
}

func (k *keeping) Save(_ context.Context, _ string, msgs []Message) error {
	k.mu.Lock()
	k.msgs = slices.Clone(msgs)
	k.mu.Unlock()
	return nil
}

func (k *keeping) Messages(context.Context, string) ([]Message, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.msgs), nil
}

func (*keeping) AddSteer(context.Context, string, string) error       { return nil }
func (*keeping) TakeSteers(context.Context, string) ([]string, error) { return nil, nil }

func TestARunCarriesOnFromTheSummaryItsSessionSaved(t *testing.T) {
	store := &keeping{}
	session, err := Open("s1", store)
	if err != nil {
		t.Fatal(err)
	}
	spec := compacting
	spec.Session = session

	newTranscriptRig(t).run(t, spec, conversation(10, func(int) string { return "x" }))
	saved, _ := session.Messages(t.Context())
	if len(summariesIn(saved)) != 1 {
		t.Fatalf("the session saved %d summaries; want the one the run wrote", len(summariesIn(saved)))
	}

	again := newTranscriptRig(t)
	again.run(t, spec, saved)
	if again.model.summaries() != 0 {
		t.Fatalf("a run resumed from its session summarised again")
	}
	if resaved, _ := session.Messages(t.Context()); len(summariesIn(resaved)) != 1 {
		t.Fatalf("the resumed run saved %d summaries; it keeps the one it carried on from", len(summariesIn(resaved)))
	}
}

func TestATranscriptEditedBeforeItsSummaryIsCompactedFromScratch(t *testing.T) {
	first := newTranscriptRig(t)
	done, _ := first.run(t, compacting, conversation(10, func(int) string { return "x" }))

	edited := slices.Clone(done.Messages)
	edited[0] = say(RoleUser, "a different task")
	again := newTranscriptRig(t)
	again.model.summary = "Goal: a different task."
	result, _ := again.run(t, compacting, edited)

	if again.model.summaries() != 1 {
		t.Fatalf("summarised %d time(s); a summary of another conversation must not stand in for this one",
			again.model.summaries())
	}
	if marks := summariesIn(result.Messages); len(marks) != 1 || marks[0].Summary != "Goal: a different task." {
		t.Fatalf("the transcript holds %+v; want only the new summary", marks)
	}
}

func TestAResumedRunsNextSummaryNeverTakesASummaryForTheUsersWords(t *testing.T) {
	first := newTranscriptRig(t)
	done, _ := first.run(t, compacting, conversation(10, func(int) string { return "x" }))

	more := slices.Clone(done.Messages)
	more = append(more, conversation(12, func(int) string { return "y" })[1:]...)
	again := newTranscriptRig(t)
	again.model.summary = "Goal: the second."
	result, _ := again.run(t, compacting, more)

	marks := summariesIn(result.Messages)
	if len(marks) != 2 {
		t.Fatalf("the transcript holds %d summaries; want the carried one and a new one", len(marks))
	}
	for _, u := range marks[1].User {
		if strings.Contains(u, "This run was compacted") {
			t.Fatalf("the user's words kept beside the summary include a summary: %q", u)
		}
	}
}

func TestARunThatDoesNotCompactSendsASummaryAsTheMessageItReadsAs(t *testing.T) {
	r := newTranscriptRig(t)
	marker := markerMessage(CompactionSummary{Summary: "Goal: the review.", Prefix: "abc"})
	msgs := []Message{say(RoleUser, "task"), marker, say(RoleUser, "go on")}

	result, _ := r.run(t, Spec{Pattern: "handing", MaxSteps: 1}, msgs)

	if sent := r.model.requests()[0].Messages; len(sent) != 3 || !strings.Contains(sent[1].Text(), "Goal: the review.") {
		t.Fatalf("a run without Compaction sent %d messages; want the summary passed through as it reads", len(sent))
	}
	if len(summariesIn(result.Messages)) != 1 {
		t.Fatalf("a run without Compaction dropped the summary from its transcript")
	}
}

func TestSpliceKeepsASummaryThatEndsTheTranscript(t *testing.T) {
	h := &compactHandle{}
	msgs := []Message{say(RoleUser, "task"), say(RoleAssistant, "x"), markerMessage(CompactionSummary{Summary: "Goal: last."})}

	plain := h.resume(msgs)

	if back := h.splice(plain); len(back) != 3 || back[2].Compaction == nil {
		t.Fatalf("splice gave back %d messages; a summary at the very end is kept too", len(back))
	}
}

func TestTheTranscriptSavedBeforeARequestCarriesItsSummaries(t *testing.T) {
	for name, send := range map[string]func(c *savingClient, req Request){
		"complete": func(c *savingClient, req Request) { _, _ = c.Complete(t.Context(), req) },
		"stream":   func(c *savingClient, req Request) { _, _ = c.Stream(t.Context(), req) },
	} {
		session, _ := Open("s1", &keeping{})
		h := &compactHandle{}
		plain := h.resume([]Message{say(RoleUser, "task"), markerMessage(CompactionSummary{Summary: "Goal: kept."}), say(RoleUser, "go on")})

		send(&savingClient{inner: &scripted{}, session: session, splice: h.splice}, Request{Messages: plain})

		if saved, _ := session.Messages(t.Context()); len(summariesIn(saved)) != 1 {
			t.Errorf("%s: saved %d summaries before the request; a run that dies now would summarise again",
				name, len(summariesIn(saved)))
		}
	}
}

func TestARunContinuedByASteerKeepsItsSummaryOutOfThePattern(t *testing.T) {
	r := newTranscriptRig(t)
	handed := make(chan *Run, 1)
	r.pattern.after = func() { _ = (<-handed).Steer("and the CLI") }
	spec := compacting
	spec.SteerContinuations = 1

	run := r.runner.Start(t.Context(), spec, conversation(10, func(int) string { return "x" }), Endpoint{}, nil)
	handed <- run
	result, err := run.Wait()
	if err != nil {
		t.Fatal(err)
	}

	got := r.pattern.received()
	if len(got) != 2 {
		t.Fatalf("the pattern ran %d time(s); the steer should have continued it", len(got))
	}
	for _, msgs := range got {
		if len(summariesIn(msgs)) != 0 {
			t.Fatalf("a continued turn was handed a summary message")
		}
	}
	if r.model.summaries() != 1 || len(summariesIn(result.Messages)) != 1 {
		t.Fatalf("summarised %d time(s), %d summaries kept; a continuation carries on from the one it has",
			r.model.summaries(), len(summariesIn(result.Messages)))
	}
}
