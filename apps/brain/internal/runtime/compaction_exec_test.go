package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const summaryText = "Goal: triage the bug reports.\nFindings: three are duplicates.\nNext step: label them."

// longTranscript seeds a run that already did fifteen graph calls, so a
// resume starts past a team's message count of ten.
func (f *execFixture) longTranscript(t *testing.T) {
	t.Helper()
	msgs := []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: runInput}}}}
	for i := range 15 {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs,
			agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: id, Name: GraphToolName, Arguments: []byte(`{"query":"bugs"}`)}}},
			agent.Message{Role: agent.RoleTool, ToolCallID: id, Content: []agent.Content{{Type: agent.ContentText, Text: "no matches"}}})
	}
	f.resumes(t, msgs)
	if _, err := f.store.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: f.team, SearchProvider: "none", CompactionMessages: 10,
	}); err != nil {
		t.Fatal(err)
	}
}

func lastOf(events []db.RunEvent, kind string) (db.RunEvent, bool) {
	for j := len(events) - 1; j >= 0; j-- {
		if events[j].Kind == kind {
			return events[j], true
		}
	}
	return db.RunEvent{}, false
}

func TestALongRunCompactsAndRecordsWhatItLeft(t *testing.T) {
	f := newExecFixture(t, newScripted(answer(summaryText, 300, 40), answer("three duplicates, labelled", 50, 10)))
	f.longTranscript(t)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	reqs := f.model.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model calls, want the summary and the answer", len(reqs))
	}
	if sent := textOf(reqs[1].Messages); !strings.Contains(sent, "three are duplicates") || len(reqs[1].Messages) > 10 {
		t.Fatalf("the answer's request carried %d messages and %q; want the summary in place of the old turns",
			len(reqs[1].Messages), sent)
	}

	e, ok := lastOf(f.events(t), "compacted")
	if !ok {
		t.Fatalf("events = %v, want compacted", kinds(f.events(t)))
	}
	p := payload[compactedPayload](t, e)
	if p.Reason != "messages" || p.Folded == 0 || p.Summary != summaryText {
		t.Fatalf("compacted = %+v; want the reason, what was folded, and the summary", p)
	}
	// The transcript keeps the summary, so a resume carries on from it.
	saved, err := f.sessions.Messages(t.Context(), transcriptOf(f.row(t)))
	if err != nil {
		t.Fatal(err)
	}
	var kept int
	for _, m := range saved {
		if m.Compaction != nil && m.Compaction.Summary == summaryText {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("the saved transcript holds %d summaries; want the one the run wrote", kept)
	}
	// The summary's tokens are the run's, so its budget counts them.
	if r := f.row(t); r.InputTokens < 300 {
		t.Fatalf("the run counted %d input tokens; the summary call read 300", r.InputTokens)
	}
}

func TestAResumedRunCarriesOnFromTheSummaryInItsTranscript(t *testing.T) {
	f := newExecFixture(t, newScripted(answer(summaryText, 300, 40), turn{err: fmt.Errorf("the gateway went away")}))
	f.longTranscript(t)
	f.mustExecute(t)
	wantStatus(t, f.row(t), "failed", "gateway went away")

	// Taken up again, as a worker would after the first one died.
	f.mustExec(t, `UPDATE runs SET status = 'queued', worker = NULL, heartbeat_at = NULL, started_at = NULL,
		finished_at = NULL, error = NULL, output = NULL WHERE id = $1`, f.run)
	again := newScripted(answer("three duplicates, labelled", 50, 10))
	f.exec = f.executor(t, func(agent.Endpoint) (agent.ModelClient, error) { return again, nil },
		agent.Endpoint{BaseURL: "http://model.test"})

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	reqs := again.requests()
	if len(reqs) != 1 || !strings.Contains(textOf(reqs[0].Messages), "three are duplicates") {
		t.Fatalf("the resumed run made %d calls; want one, carrying the stored summary", len(reqs))
	}
}

func TestARunNearItsBudgetIsAskedToWrapUpAndAnswers(t *testing.T) {
	f := newExecFixture(t, newScripted(
		turn{resp: agent.Response{
			Message: agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: GraphToolName, Arguments: []byte(`{"query":"bugs"}`)}}},
			Finish:  agent.FinishToolCalls,
			Usage:   agent.Usage{InputTokens: 4_000, OutputTokens: 5},
		}},
		answer("what I found before the budget ran out", 100, 10),
	))
	// 10,000 left 5,995 after the first call: two more like it and a reply would not fit.
	f.mustExec(t, `UPDATE runs SET token_budget = 10000 WHERE id = $1`, f.run)

	f.mustExecute(t)

	r := f.row(t)
	wantStatus(t, r, "succeeded", "")
	if r.Output == nil || *r.Output != "what I found before the budget ran out" {
		t.Fatalf("output = %v, want the wrapped-up answer", r.Output)
	}
	e, ok := lastOf(f.events(t), "wrap_up")
	if !ok {
		t.Fatalf("events = %v, want wrap_up", kinds(f.events(t)))
	}
	if p := payload[wrapUpPayload](t, e); p.Reason != "budget" || p.Left != 5_995 {
		t.Fatalf("wrap_up = %+v, want the budget and what was left", p)
	}
	if last := f.model.requests()[1].Messages; !strings.Contains(textOf(last), budgetWrapUp) {
		t.Fatal("the request after the wrap-up does not carry it")
	}
}

func TestARunStoppedAtItsBudgetKeepsItsSummary(t *testing.T) {
	f := newExecFixture(t, newScripted(answer(summaryText, 300, 40), answer("too expensive", 50_000, 10)))
	f.longTranscript(t)
	f.mustExec(t, `UPDATE runs SET token_budget = 20000 WHERE id = $1`, f.run)

	f.mustExecute(t)

	r := f.row(t)
	wantStatus(t, r, "failed", "token budget")
	if r.Output == nil || *r.Output != summaryText {
		t.Fatalf("output = %v; a run stopped at its budget keeps its latest summary", r.Output)
	}
}

func TestARunThatFailsOnItsOwnKeepsNoOutput(t *testing.T) {
	f := newExecFixture(t, newScripted(answer(summaryText, 300, 40), turn{err: fmt.Errorf("the gateway went away")}))
	f.longTranscript(t)

	f.mustExecute(t)

	if r := f.row(t); r.Output != nil {
		t.Fatalf("output = %q; only a run stopped at a limit keeps what it had", *r.Output)
	}
}
