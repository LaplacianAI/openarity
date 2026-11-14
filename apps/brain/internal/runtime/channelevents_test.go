package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

func TestWhatAToolCallSaysInTheConversation(t *testing.T) {
	t.Parallel()

	call := func(name, args string) agent.ToolCallEvent {
		return agent.ToolCallEvent{ID: "c1", Name: name, Arguments: json.RawMessage(args)}
	}
	for _, c := range []struct {
		name string
		call agent.ToolCallEvent
		want string // "" says nothing
	}{
		{"a tool is named as code", call("read_logs", `{}`), "Using `read_logs`…"},
		{"a delegation names the agent asked", call(DelegateToolName, `{"agent":"billing","objective":"x"}`), "Asking `billing`…"},
		{"a delegation naming no agent says nothing", call(DelegateToolName, `{}`), ""},
		{"a tool found by search is named as itself", call(agent.CallToolName, `{"name":"read_logs","arguments":{}}`), "Using `read_logs`…"},
		{"call_tool with no name says nothing", call(agent.CallToolName, `nonsense`), ""},
		{"bookkeeping found by search is still bookkeeping", call(agent.CallToolName, `{"name":"graph"}`), ""},
		{"a backtick cannot break out of the code", call("a`b", `{}`), "Using `ab`…"},
		{"finding tools", call(GraphToolName, `{}`), ""},
		{"planning", call(PlanToolName, `{}`), ""},
		{"reading a held output", call(agent.ReadOutputToolName, `{}`), ""},
		{"listing files", call(AttachmentsToolName, `{}`), ""},
		{"reading a file", call(ReadAttachmentToolName, `{}`), ""},
		{"searching for tools", call(agent.DescribeToolsName, `{}`), ""},
		{"opening a skill", call(agent.SkillToolName, `{}`), ""},
	} {
		got, ok := spoken(c.call)
		if c.want == "" && ok {
			t.Errorf("%s: said %q, want nothing", c.name, got)
		}
		if c.want != "" && got != c.want {
			t.Errorf("%s: said %q (%v), want %q", c.name, got, ok, c.want)
		}
	}
}

type told struct {
	seq  int32
	text string
}

// telling records what a recorder tells, safe from its goroutine.
type telling struct {
	mu  sync.Mutex
	got []told
}

func (l *telling) tell(seq int32, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, told{seq, text})
}

func (l *telling) all() []told {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

func feedTelling(t *testing.T, h heldRun, events ...agent.Event) []told {
	t.Helper()

	l := &telling{}
	st := &stops{}
	ch := make(chan agent.Event)
	done := record(t.Context(), h.store, h.id, worker, newSoftLines(maxTokens, 0), true, l.tell, ch, nil, st.stop)
	for _, e := range events {
		select {
		case ch <- e:
		case <-time.After(5 * time.Second):
			t.Fatalf("the recorder stopped reading at %T", e)
		}
	}
	close(ch)
	<-done
	wantNoStop(t, st)
	return l.all()
}

// What a step says before it calls a tool is told, then the call, each named
// by the run event it came from. The step that ends the run says the answer,
// which goes as the answer, not as a step.
func TestAStepIsToldAfterTheEventItComesFrom(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	got := feedTelling(t, h,
		agent.StepEvent{Step: 1},
		agent.ReasoningEvent{Delta: "Need the logs."},
		agent.TextEvent{Delta: "Let me check "}, agent.TextEvent{Delta: "the logs."},
		agent.ToolCallEvent{ID: "c1", Name: "read_logs", Arguments: json.RawMessage(`{}`)},
		agent.ToolCallEvent{ID: "c2", Name: GraphToolName, Arguments: json.RawMessage(`{}`)},
		agent.ToolCallEvent{ID: "c3", Name: DelegateToolName, Arguments: json.RawMessage(`{"agent":"billing"}`)},
		agent.ToolResultEvent{ID: "c1", Name: "read_logs", Output: "secret: hunter2"},
		agent.StepEvent{Step: 2},
		agent.TextEvent{Delta: "It timed out."},
	)

	seqOf := map[string]int32{}
	for _, e := range h.events(t) {
		switch e.Kind {
		case "text":
			seqOf[payload[textPayload](t, e).Text] = e.Seq
		case "tool_call":
			seqOf[payload[toolCallPayload](t, e).ID] = e.Seq
		}
	}
	want := []told{
		{seqOf["Let me check the logs."], "Let me check the logs."},
		{seqOf["c1"], "Using `read_logs`…"},
		{seqOf["c3"], "Asking `billing`…"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("told %+v, want %+v", got, want)
	}
}

// A step's text with no call after it in the same step is not carried into
// the next step's call.
func TestANewStepForgetsWhatTheLastOneSaid(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	got := feedTelling(t, h,
		agent.StepEvent{Step: 1},
		agent.TextEvent{Delta: "Thinking aloud."},
		agent.StepEvent{Step: 2},
		agent.ToolCallEvent{ID: "c1", Name: "read_logs", Arguments: json.RawMessage(`{}`)},
	)
	if len(got) != 1 || got[0].text != "Using `read_logs`…" {
		t.Errorf("told %+v, want the call alone", got)
	}
}

// Without a teller, as for a run whose channel sends its answer alone, the
// recorder writes what it always has.
func TestARunNotTellingWritesTheSameEvents(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	st := feedStreaming(t, h.store, h.id,
		agent.TextEvent{Delta: "Let me look."},
		agent.ToolCallEvent{ID: "c1", Name: "read_logs", Arguments: json.RawMessage(`{}`)},
	)
	wantNoStop(t, st)
	if got := kinds(h.events(t)); !slices.Equal(got, []string{"text", "tool_call"}) {
		t.Errorf("kinds = %v", got)
	}
}

// channelRun is a top-level run answering a Telegram conversation whose
// channel's reply mode is mode.
func channelRun(t *testing.T, mode string) (*store.Store, db.Run) {
	t.Helper()

	s, raw, _ := liveSchema(t)
	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	var channelID, sessionID uuid.UUID
	if err := raw.QueryRow(t.Context(), `
		INSERT INTO channels (team_id, provider, name, reply_mode) VALUES ($1, 'telegram', 'bot', $2) RETURNING id`,
		team.ID, mode).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := raw.QueryRow(t.Context(), `
		INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, '42', 'direct') RETURNING id`,
		team.ID, channelID).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id := uuid.New()
	if _, err := raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, hub, reply_to_channel)
		VALUES ($1, $2, $3, $1, 'hub', 'why did the deploy fail?', true, true)`, id, team.ID, sessionID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	row, err := s.GetRun(t.Context(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return s, row
}

type stepKicks struct {
	mu   sync.Mutex
	seqs []int32
	err  error
}

func (k *stepKicks) kick(_ uuid.UUID, seq int32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seqs = append(k.seqs, seq)
	return k.err
}

// On a channel sending each step, a step told is written to the outbox and
// kicked, named by its run event; told again it is not kicked again.
func TestATellerWritesAStepAndKicksIt(t *testing.T) {
	t.Parallel()

	s, row := channelRun(t, "events")
	k := &stepKicks{}
	e := &Executor{store: s, logger: quietLogger(), kickEvent: k.kick}
	tell := e.teller(t.Context(), row)
	if tell == nil {
		t.Fatal("no teller for a channel sending each step")
	}
	tell(5, "Using `read_logs`…")
	tell(5, "Using `read_logs`…")

	var n int
	var text string
	if err := s.Pool().QueryRow(t.Context(),
		`SELECT count(*), max(text) FROM replies WHERE run_id = $1 AND kind = 'event' AND event_seq = 5`, row.ID).Scan(&n, &text); err != nil {
		t.Fatal(err)
	}
	if n != 1 || text != "Using `read_logs`…" {
		t.Errorf("%d rows holding %q, want the step once", n, text)
	}
	if !slices.Equal(k.seqs, []int32{5}) {
		t.Errorf("kicked %v, want step 5 once", k.seqs)
	}
}

// A kick that fails is only late: the step is written and the sweep sends it.
func TestATellerWhoseKickFailsStillWritesTheStep(t *testing.T) {
	t.Parallel()

	s, row := channelRun(t, "events")
	log := &bytes.Buffer{}
	e := &Executor{store: s, logger: slog.New(slog.NewTextHandler(log, nil)), kickEvent: (&stepKicks{err: errors.New("queue down")}).kick}
	e.teller(t.Context(), row)(3, "Asking `billing`…")

	var n int
	if err := s.Pool().QueryRow(t.Context(), `SELECT count(*) FROM replies WHERE run_id = $1`, row.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(log.String(), "the sweep will send it") {
		t.Errorf("%d rows, log %q; want the step written and the late kick logged", n, log)
	}
}

// A channel sending its answer alone, and a run answering no channel, have
// no teller: their recorder never touches the outbox.
func TestNoTellerWhereNoStepIsSent(t *testing.T) {
	t.Parallel()

	s, row := channelRun(t, "final")
	e := &Executor{store: s, logger: quietLogger()}
	if e.teller(t.Context(), row) != nil {
		t.Error("a teller for a channel sending its answer alone")
	}
	row.ReplyToChannel = false
	if e.teller(t.Context(), row) != nil {
		t.Error("a teller for a run answering no channel")
	}
}
