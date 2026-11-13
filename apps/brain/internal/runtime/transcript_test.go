package runtime

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

func userSaid(text string) agent.Message {
	return agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: text}}}
}

func asked(ids ...string) agent.Message {
	m := agent.Message{Role: agent.RoleAssistant}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, agent.ToolCall{ID: id, Name: GraphToolName, Arguments: []byte(`{}`)})
	}
	return m
}

func answered(id string) agent.Message {
	return agent.Message{Role: agent.RoleTool, ToolCallID: id, Content: []agent.Content{{Type: agent.ContentText, Text: "ok"}}}
}

func TestACallLeftWithoutAResultIsClosed(t *testing.T) {
	t.Parallel()

	got := closeCalls([]agent.Message{userSaid("go"), asked("c1", "c2"), answered("c1")})
	if len(got) != 4 || got[3].ToolCallID != "c2" || got[3].Text() != endedMidCall {
		t.Fatalf("closeCalls gave %d messages, the last %+v; want c2 closed as ended mid-call", len(got), got[len(got)-1])
	}

	whole := []agent.Message{userSaid("go"), asked("c1"), answered("c1"), {Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "done"}}}}
	if got := closeCalls(whole); len(got) != len(whole) {
		t.Errorf("a finished transcript gained %d messages", len(got)-len(whole))
	}
	if got := closeCalls(nil); len(got) != 0 {
		t.Errorf("an empty transcript gained %d messages", len(got))
	}
}

func TestATopLevelRunKeepsItsSessionsTranscriptAndADelegateItsOwn(t *testing.T) {
	t.Parallel()

	shared := "session:" + uuid.NewString()
	if got := transcriptOf(db.Run{ID: uuid.New(), Transcript: &shared}); got != shared {
		t.Errorf("transcriptOf = %q, want %q", got, shared)
	}
	legacy := uuid.New()
	if got := transcriptOf(db.Run{ID: legacy}); got != legacy.String() {
		t.Errorf("a run from before shared transcripts keeps %q, want its own id", got)
	}
}

// --- end to end, with real Postgres ---

// next makes a second top-level run in the fixture's session the one the
// fixture executes, as the next message of a conversation would.
func (f *execFixture) next(t *testing.T, input string) uuid.UUID {
	t.Helper()
	row := f.row(t)
	r, err := f.store.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: row.TeamID, SessionID: row.SessionID, AgentID: f.agent, Input: input,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	f.run = r.ID
	return r.ID
}

func TestASessionsNextRunSeesWhatTheLastOneDid(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("g1", GraphToolName, `{"query":"who reconciles invoices","kinds":["agents"],"hops":1}`),
		answer("the billing agent does", 1, 1),
		answer("yes, nightly", 1, 1),
	))
	f.mustExecute(t)
	wantStatus(t, f.row(t), "succeeded", "")

	f.next(t, "does it run every night?")
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	sent := f.model.requests()[2].Messages
	var sawResult bool
	for _, m := range sent {
		if m.Role == agent.RoleTool && m.ToolCallID == "g1" {
			sawResult = true
		}
	}
	text := textOf(sent)
	if !sawResult || !strings.Contains(text, "the billing agent does") || !strings.HasSuffix(strings.TrimSpace(sent[len(sent)-1].Text()), "does it run every night?") {
		t.Fatalf("the second run was sent %q; want the first run's tool result and answer, then its own message", text)
	}
	if row := f.row(t); row.TranscriptFrom == nil || *row.TranscriptFrom != 4 {
		t.Errorf("the second run's place is %v, want 4: after the first run's four messages", row.TranscriptFrom)
	}
}

func TestARunWhoseFirstMessageWasLostWritesItOnce(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 1, 1)))
	// Placed, then the worker died before the first save.
	f.mustExec(t, `UPDATE runs SET transcript_from = 0 WHERE id = $1`, f.run)

	f.mustExecute(t)

	msgs, err := f.sessions.Messages(t.Context(), transcriptOf(f.row(t)))
	if err != nil {
		t.Fatal(err)
	}
	var inputs int
	for _, m := range msgs {
		if m.Text() == runInput {
			inputs++
		}
	}
	if inputs != 1 || kinds(f.events(t))[0] != "resumed" {
		t.Fatalf("the transcript holds the input %d times, events %v; want it once, and the run resumed", inputs, kinds(f.events(t)))
	}
}

func TestARunFromBeforeSharedTranscriptsResumesFromItsOwn(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("carried on", 1, 1)))
	f.mustExec(t, `UPDATE runs SET transcript = NULL WHERE id = $1`, f.run)
	if err := f.sessions.Save(t.Context(), f.run.String(), []agent.Message{userSaid("the question asked before")}); err != nil {
		t.Fatal(err)
	}

	f.mustExecute(t)

	first := textOf(f.model.requests()[0].Messages)
	if !strings.Contains(first, "the question asked before") || kinds(f.events(t))[0] != "resumed" {
		t.Fatalf("a run from before resumed with %q; want its own transcript", first)
	}
}

func TestANewRunClosesACallTheLastRunLeftOpen(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("fine", 1, 1)))
	if err := f.sessions.Save(t.Context(), transcriptOf(f.row(t)), []agent.Message{userSaid("earlier"), asked("c9")}); err != nil {
		t.Fatal(err)
	}

	f.mustExecute(t)

	sent := f.model.requests()[0].Messages
	at := slices.IndexFunc(sent, func(m agent.Message) bool { return m.ToolCallID == "c9" })
	if at != 2 || sent[at].Text() != endedMidCall || sent[3].Text() != runInput {
		t.Fatalf("the request was %q; want c9 closed, then the run's own message", textOf(sent))
	}
}

func TestADelegateKeepsATranscriptOfItsOwn(t *testing.T) {
	f := newExecFixture(t, newScripted())
	child := f.startUnder(t, f.run, "call_1#a", worker)

	if got := transcriptOf(child); got != child.ID.String() {
		t.Fatalf("a delegate's transcript is %q, want its own id", got)
	}
}
