package patterns

import (
	"errors"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// cutTurns are n replies each saying s and each cut off at MaxTokens.
func cutTurns(s string, n int) []turn {
	out := make([]turn, n)
	for i := range out {
		out[i] = turn{resp: agent.Response{Message: text(s), Finish: agent.FinishLength}}
	}
	return out
}

// An answer longer than MaxTokens was a failed run, though the model was
// only part-way through it. It goes on, and the person reads it whole.
func TestACutOffAnswerGoesOnAndIsReadWhole(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{
		{resp: agent.Response{Message: text("Part one, "), Finish: agent.FinishLength, Usage: agent.Usage{OutputTokens: 10}}},
		{resp: agent.Response{Message: text("part two."), Finish: agent.FinishStop, Usage: agent.Usage{OutputTokens: 4}}},
	}}
	events := make(chan agent.Event, 16)

	got, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(), Messages: ask("write it"), Model: m, Events: events,
	})
	close(events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != "Part one, part two." {
		t.Errorf("Output = %q, want both parts joined", got.Output)
	}

	sent := m.calls[1].Messages
	if last := sent[len(sent)-1]; last.Role != agent.RoleUser || last.Text() != goOn {
		t.Errorf("the continuation ended with %+v, want the go-on turn", last)
	}
	if partial := sent[len(sent)-2]; partial.Text() != "Part one, " {
		t.Errorf("the continuation did not carry the part already written, got %q", partial.Text())
	}
	// Each reply keeps its own message: a provider's reasoning is signed per
	// reply, and merging two would send back fields neither one wrote.
	if n := len(got.Messages); n != 4 {
		t.Errorf("%d messages, want the question, the part, the go-on and the rest", n)
	}

	var spent int
	for e := range events {
		if u, ok := e.(agent.UsageEvent); ok {
			spent += u.Usage.OutputTokens
		}
	}
	if spent != 14 {
		t.Errorf("usage reported %d output tokens, want 14: a continuation is spent like any call", spent)
	}
}

func TestAnAnswerCutOffEveryTimeIsTruncated(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: cutTurns("more ", 1+continuations)}

	got, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(), Messages: ask("go"), Model: m})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(m.calls) != 1+continuations {
		t.Errorf("%d calls, want the reply and %d continuations", len(m.calls), continuations)
	}
	if want := strings.Repeat("more ", 1+continuations); got.Output != want {
		t.Errorf("Output = %q, want all of it, %q", got.Output, want)
	}
}

// A continuation that calls a tool is a step like any other: the tool runs.
func TestAContinuationMayCallATool(t *testing.T) {
	t.Parallel()

	var ran bool
	m := &fakeModel{turns: []turn{
		{resp: agent.Response{Message: text("Let me look"), Finish: agent.FinishLength}},
		answered(calling("lookup", `{"id":1}`)),
		answered(text("found it")),
	}}

	got, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("lookup", "1", nil, &ran)), Messages: ask("go"), Model: m,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran || got.Output != "found it" {
		t.Errorf("ran %v, Output %q; want the tool run and the answer after it", ran, got.Output)
	}
}

// The plan the acting phase follows is the whole plan, not its first part.
func TestACutOffPlanGoesOn(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{
		{resp: agent.Response{Message: text("1. Read. "), Finish: agent.FinishLength}},
		answered(text("2. Answer.")),
		answered(text("done")),
	}}

	got, err := Plan().Run(t.Context(), agent.Input{Spec: planSpec(), Messages: ask("go"), Model: m})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	acting := m.calls[2].Messages
	if n := len(acting); n != 4 || acting[1].Text() != "1. Read. " || acting[3].Text() != "2. Answer." {
		t.Errorf("the acting phase was sent %d messages, want the question and the plan in its two parts", n)
	}
	if got.Output != "done" {
		t.Errorf("Output = %q, want done", got.Output)
	}
}

func TestACutOffSolveGoesOn(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		{resp: agent.Response{Message: text("the count "), Finish: agent.FinishLength}},
		answered(text("is 3")),
	}}

	got, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: m,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != "the count is 3" {
		t.Errorf("Output = %q, want the answer joined", got.Output)
	}
}

// A draft that went on, then rewritten: the rewrite replaces the whole draft,
// both its parts and the go-on between them, or the history keeps half of
// an answer the person never sees.
func TestARewriteReplacesADraftThatWentOn(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{
		{resp: agent.Response{Message: text("draft, "), Finish: agent.FinishLength}},
		answered(text("continued")),
		critiqued(true, "shorter"),
		{resp: agent.Response{Message: text("short "), Finish: agent.FinishLength}},
		answered(text("one")),
	}}

	got, err := Reflection(1).Run(t.Context(), agent.Input{Spec: reflectSpec(), Messages: ask("write"), Model: m})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != "short one" {
		t.Errorf("Output = %q, want the rewrite joined", got.Output)
	}
	var texts []string
	for _, msg := range got.Messages {
		texts = append(texts, msg.Text())
	}
	if want := []string{"write", "short ", goOn, "one"}; strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Errorf("messages = %q, want %q: the draft gone whole", texts, want)
	}
}
