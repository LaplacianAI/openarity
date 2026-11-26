package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

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
		if m.Text() == dated(runInput) {
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
	if at != 2 || sent[at].Text() != endedMidCall || sent[3].Text() != dated(runInput) {
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

// 23:30 in New York is already the 17th in UTC: the date is UTC's, whatever
// the clock's zone.
var fixedNow = time.Date(2026, time.November, 16, 23, 30, 0, 0, time.FixedZone("EST", -5*3600))

const wantToday = "Now: Tuesday, 17 November 2026, 04:30 UTC."

// dated is a run's first message: the date it was asked, then its input.
func dated(input string) string { return wantToday + "\n\n" + input }

// A run is told the date, or the model takes its training cutoff for today and
// reads a date already past as one still to come. In the message, never the
// system prompt, so every system block stays cacheable. Each turn keeps the
// day it was asked on, and a delegate is told too.
func TestEveryRunIsToldTodaysDateInItsMessage(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("noted", 1, 1)))
	f.mustExecute(t)

	first := f.model.requests()[0]
	if got := first.Messages[len(first.Messages)-1]; got.Text() != dated(runInput) || !got.Pinned {
		t.Errorf("the run's message is %q, pinned %v; want %q, pinned so compaction keeps it", got.Text(), got.Pinned, dated(runInput))
	}
	for _, c := range first.System {
		if strings.Contains(c.Text, "Now: ") {
			t.Errorf("the system prompt holds the date, %q: a block that changes daily ends the cached prefix", c.Text)
		}
	}

	delegate, err := f.exec.firstMessage(t.Context(), db.Run{ID: uuid.New(), Input: "the brief"})
	if err != nil || delegate.Text() != dated("the brief") {
		t.Errorf("a delegate's first message is %q, %v; want it dated", delegate.Text(), err)
	}
}

// A run that starts in a conversation sends the earlier runs' large tool
// results as handles: the mark is in the transcript before the run's place,
// the place is the run's first message, and the answer before it is sent as
// it was.
func TestANewRunSendsEarlierResultsAsHandles(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("hi there", 1, 1)))
	session := transcriptOf(f.row(t))
	big := strings.Repeat("diff --git a/x b/x\n", 2000)
	earlier := []agent.Message{
		userSaid("review the PR"), asked("c1"),
		{Role: agent.RoleTool, ToolCallID: "c1", Content: []agent.Content{{Type: agent.ContentText, Text: big}}},
		{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "two bugs found"}}},
	}
	if err := f.sessions.Save(t.Context(), session, earlier); err != nil {
		t.Fatal(err)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	sent := f.model.requests()[0].Messages
	for _, m := range sent {
		if m.ToolCallID == "c1" && strings.Contains(m.Text(), "diff --git") {
			t.Fatalf("the earlier result went whole: %.80q", m.Text())
		}
	}
	if !strings.Contains(textOf(sent), "two bugs found") {
		t.Errorf("the earlier answer was not sent: %q", textOf(sent))
	}
	stored, err := f.sessions.Messages(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	from := f.row(t).TranscriptFrom
	if from == nil || *from != 5 || stored[4].Clearing == nil || stored[5].Text() != dated(runInput) {
		t.Fatalf("place %v; stored[4] %+v; want the mark at 4 and the run's first message at 5", from, stored[4])
	}
	if stored[2].Text() != big {
		t.Error("the transcript lost the earlier result; it keeps every result whole")
	}
}

// A cleared result is still the run's to read: read_output with its call id
// hands it back whole.
func TestAClearedEarlierResultIsReadBackByItsCallID(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("r1", agent.ReadOutputToolName, `{"call":"c1","match":"needle"}`),
		answer("found it", 1, 1),
	))
	session := transcriptOf(f.row(t))
	big := strings.Repeat("hay\n", 1000) + "the needle is on line 1001\n"
	if err := f.sessions.Save(t.Context(), session, []agent.Message{
		userSaid("look"), asked("c1"),
		{Role: agent.RoleTool, ToolCallID: "c1", Content: []agent.Content{{Type: agent.ContentText, Text: big}}},
		{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "looked"}}},
	}); err != nil {
		t.Fatal(err)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	sent := f.model.requests()[1].Messages
	last := sent[len(sent)-1]
	if last.ToolCallID != "r1" || !strings.Contains(last.Text(), "the needle is on line 1001") {
		t.Fatalf("read_output gave %.120q; want the cleared result's line", last.Text())
	}
}

// fromStore records where a save kept the stored messages to.
type fromStore struct {
	agent.Store
	from int
	msgs []agent.Message
}

func (s *fromStore) SaveFrom(_ context.Context, _ string, from int, msgs []agent.Message) error {
	s.from, s.msgs = from, msgs
	return nil
}

// The mark keeps every stored message but those closeCalls may have moved:
// it adds a result only before a trailing files message.
func TestTheMarkKeepsWhatIsStoredAsItIs(t *testing.T) {
	t.Parallel()

	mark := agent.Message{Role: agent.RoleUser, Clearing: &agent.ClearingMark{Prefix: "p"}}
	for name, tc := range map[string]struct{ stored, now, from int }{
		"nothing closed": {stored: 4, now: 4, from: 4},
		"a call closed":  {stored: 4, now: 5, from: 3},
	} {
		s := &fromStore{}
		e := &Executor{sessions: s}
		msgs := make([]agent.Message, tc.now)
		if err := e.saveMark(t.Context(), "session:x", tc.stored, msgs, mark); err != nil {
			t.Fatal(err)
		}
		if s.from != tc.from || len(s.msgs) != tc.now+1 || s.msgs[tc.now].Clearing == nil {
			t.Errorf("%s: kept %d, wrote %d with the mark last %v; want kept %d", name, s.from, len(s.msgs), s.msgs[len(s.msgs)-1].Clearing != nil, tc.from)
		}
	}
}

// The mark is stored before the run's place is written: a run whose worker
// stops before its first save still finds its first message at its place,
// and replay reads it there.
func TestTheMarkIsStoredBeforeTheRunsPlace(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("unused", 1, 1)))
	row := f.row(t)
	session := transcriptOf(row)
	earlier := []agent.Message{
		userSaid("look"), asked("c1"),
		{Role: agent.RoleTool, ToolCallID: "c1", Content: []agent.Content{{Type: agent.ContentText, Text: strings.Repeat("x", 4<<10)}}},
		{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "looked"}}},
	}
	if err := f.sessions.Save(t.Context(), session, earlier); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.exec.begin(t.Context(), row, slices.Clone(earlier)); err != nil {
		t.Fatal(err)
	}

	stored, err := f.sessions.Messages(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 5 || stored[4].Clearing == nil {
		t.Fatalf("stored %d messages before the run saved anything; want the mark at 4", len(stored))
	}
	if from := f.row(t).TranscriptFrom; from == nil || *from != 5 {
		t.Errorf("place %v, want 5, after the mark", from)
	}
}
