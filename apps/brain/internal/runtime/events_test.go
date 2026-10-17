package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	worker         = "w1"
	cleanupTimeout = 30 * time.Second
)

// liveSchema is a migrated schema of its own for one test, and a plain
// connection into it for the statements no query is generated for, and its DSN.
func liveSchema(t *testing.T) (*store.Store, *pgx.Conn, string) {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}

	// Postgres cuts an identifier at 63 bytes without failing, and two tests
	// cut to the same name would share their tables.
	schema := "runtime_test_" + strings.ToLower(t.Name())
	if len(schema) > 63 {
		t.Fatalf("the schema name %s is %d bytes, and Postgres keeps 63", schema, len(schema))
	}

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, statement := range []string{"DROP SCHEMA IF EXISTS " + schema + " CASCADE", "CREATE SCHEMA " + schema} {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	_ = admin.Close(t.Context())

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("BRAIN_TEST_POSTGRES_DSN is not a URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := store.New(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to the schema: %v", err)
	}
	raw, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to the schema: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()

		s.Close()
		if _, err := raw.Exec(ctx, "SET lock_timeout = '5s'"); err != nil {
			t.Errorf("set lock_timeout: %v", err)
		}
		if _, err := raw.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		_ = raw.Close(ctx)
	})

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, raw, u.String()
}

// heldRun is a run this worker has claimed, in a schema of its own.
type heldRun struct {
	store *store.Store
	raw   *pgx.Conn
	id    uuid.UUID
}

func newHeldRun(t *testing.T) heldRun {
	t.Helper()

	s, raw, _ := liveSchema(t)
	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	var agentID, sessionID uuid.UUID
	if err := raw.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'triage', 'test/model', 1024, 'react', 5) RETURNING id`, team.ID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, team.ID).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	run, err := s.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: team.ID, SessionID: sessionID, AgentID: agentID, Input: "triage the bug reports",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: run.ID, Worker: worker, WallClockSeconds: 3600}); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	return heldRun{store: s, raw: raw, id: run.ID}
}

func (h heldRun) events(t *testing.T) []db.RunEvent {
	t.Helper()

	got, err := h.store.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: h.id, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	return got
}

func kinds(events []db.RunEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func payload[T any](t *testing.T, e db.RunEvent) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		t.Fatalf("decode %s payload %s: %v", e.Kind, e.Payload, err)
	}
	return out
}

// stops records every cause record hands to stop.
type stops struct {
	mu     sync.Mutex
	causes []error
}

func (s *stops) stop(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.causes = append(s.causes, cause)
}

func (s *stops) all() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.causes)
}

// feed sends events through record one by one, as the SDK does, and waits
// until record says it has drained them. A send or the wait that takes longer
// than five seconds is a recorder that stopped reading.
func feed(t *testing.T, s EventStore, runID uuid.UUID, events ...agent.Event) *stops {
	t.Helper()

	st := &stops{}
	ch := make(chan agent.Event)
	done := record(t.Context(), s, runID, worker, ch, st.stop)
	for _, e := range events {
		select {
		case ch <- e:
		case <-time.After(5 * time.Second):
			t.Fatalf("the recorder stopped reading at %T", e)
		}
	}
	close(ch)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorder never said it had drained")
	}
	return st
}

func wantNoStop(t *testing.T, st *stops) {
	t.Helper()

	if got := st.all(); len(got) != 0 {
		t.Errorf("the run was stopped: %v", got)
	}
}

// --- what is stored ---

type toolResult struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	Error      string `json:"error"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

func TestEachSDKEventBecomesItsRow(t *testing.T) {
	h := newHeldRun(t)

	st := feed(t, h.store, h.id,
		agent.StepEvent{Step: 1},
		agent.ToolCallEvent{ID: "call-1", Name: "web__fetch", Arguments: json.RawMessage(`{"url":"https://example.com"}`)},
		agent.ToolResultEvent{ID: "call-1", Name: "web__fetch", Output: "<html>", Err: "status 503", Duration: 1500 * time.Millisecond},
		agent.UsageEvent{Model: "claude-sonnet", Usage: agent.Usage{InputTokens: 120, OutputTokens: 30, CachedInputTokens: 100}},
	)
	wantNoStop(t, st)

	got := h.events(t)
	if want := []string{"step", "tool_call", "tool_result", "usage"}; !slices.Equal(kinds(got), want) {
		t.Fatalf("kinds = %v, want %v", kinds(got), want)
	}

	if step := payload[map[string]int](t, got[0]); step["step"] != 1 {
		t.Errorf("step payload = %s", got[0].Payload)
	}

	call := payload[map[string]any](t, got[1])
	if call["id"] != "call-1" || call["name"] != "web__fetch" ||
		call["arguments"] != `{"url":"https://example.com"}` || call["truncated"] != false {
		t.Errorf("tool_call payload = %s", got[1].Payload)
	}

	result := payload[toolResult](t, got[2])
	want := toolResult{ID: "call-1", Name: "web__fetch", Output: "<html>", Error: "status 503", DurationMS: 1500}
	if result != want {
		t.Errorf("tool_result = %+v, want %+v", result, want)
	}

	usage := payload[map[string]any](t, got[3])
	if usage["model"] != "claude-sonnet" || usage["input_tokens"] != 120.0 || usage["output_tokens"] != 30.0 ||
		usage["cached_input_tokens"] != 100.0 || usage["spent"] != 150.0 {
		t.Errorf("usage payload = %s", got[3].Payload)
	}
}

// Text arrives a token at a time; storing it would be a row per token for
// what the transcript already holds whole.
func TestTextIsNotStored(t *testing.T) {
	h := newHeldRun(t)

	st := feed(t, h.store, h.id, agent.TextEvent{Delta: "Hel"}, agent.TextEvent{Delta: "lo"})
	wantNoStop(t, st)
	if got := h.events(t); len(got) != 0 {
		t.Errorf("events = %v, want none", kinds(got))
	}
}

func TestALargeToolResultIsCutAndSaysSo(t *testing.T) {
	h := newHeldRun(t)
	// One byte, then two-byte runes, so 64 KiB falls inside a rune.
	output := "a" + strings.Repeat("é", 35<<10)

	st := feed(t, h.store, h.id, agent.ToolResultEvent{ID: "c", Name: "read", Output: output})
	wantNoStop(t, st)

	got := payload[toolResult](t, h.events(t)[0])
	switch {
	case !got.Truncated:
		t.Error("truncated = false for 70 KiB of output")
	case len(got.Output) > maxEventText || len(got.Output) < maxEventText-utf8.UTFMax:
		t.Errorf("stored %d bytes, want just under %d", len(got.Output), maxEventText)
	case !utf8.ValidString(got.Output) || !strings.HasPrefix(output, got.Output):
		t.Error("the stored output is not a clean prefix of the original")
	}
}

func TestAnOutputAtTheLimitIsNotCut(t *testing.T) {
	h := newHeldRun(t)
	output := strings.Repeat("a", maxEventText)

	feed(t, h.store, h.id, agent.ToolResultEvent{ID: "c", Name: "read", Output: output})

	got := payload[toolResult](t, h.events(t)[0])
	if got.Truncated || got.Output != output {
		t.Errorf("truncated = %v, %d bytes; want the whole %d", got.Truncated, len(got.Output), maxEventText)
	}
}

func TestALargeErrorOrArgumentsAreCutToo(t *testing.T) {
	h := newHeldRun(t)
	big := strings.Repeat("x", maxEventText+10)

	st := feed(t, h.store, h.id,
		agent.ToolCallEvent{ID: "c", Name: "write", Arguments: json.RawMessage(`"` + big + `"`)},
		agent.ToolResultEvent{ID: "c", Name: "write", Output: "ok", Err: big},
	)
	wantNoStop(t, st)

	got := h.events(t)
	call := payload[struct {
		Arguments string `json:"arguments"`
		Truncated bool   `json:"truncated"`
	}](t, got[0])
	if !call.Truncated || len(call.Arguments) > maxEventText {
		t.Errorf("tool_call: truncated %v, %d bytes", call.Truncated, len(call.Arguments))
	}
	result := payload[toolResult](t, got[1])
	if !result.Truncated || len(result.Error) > maxEventText || result.Output != "ok" {
		t.Errorf("tool_result: truncated %v, error %d bytes, output %q", result.Truncated, len(result.Error), result.Output)
	}
}

// Postgres refuses \u0000 in jsonb, so a tool that prints a NUL would fail
// the append and stop the run over a byte nobody needs.
func TestANULInAToolsOutputDoesNotStopTheRun(t *testing.T) {
	h := newHeldRun(t)

	st := feed(t, h.store, h.id,
		agent.ToolCallEvent{ID: "c", Name: "read", Arguments: json.RawMessage("{\"path\":\"a\x00b\"}")},
		agent.ToolResultEvent{ID: "c", Name: "read", Output: "before\x00after", Err: "x\x00y"},
	)
	wantNoStop(t, st)

	got := h.events(t)
	if len(got) != 2 {
		t.Fatalf("events = %v, want both", kinds(got))
	}
	if result := payload[toolResult](t, got[1]); result.Output != "before�after" || result.Error != "x�y" {
		t.Errorf("tool_result = %+v, want each NUL as U+FFFD", result)
	}
}

// A model's arguments are whatever it wrote, and a half-written object is
// still worth seeing when working out why its call failed.
func TestArgumentsThatAreNotJSONAreStoredAsWritten(t *testing.T) {
	h := newHeldRun(t)

	st := feed(t, h.store, h.id, agent.ToolCallEvent{ID: "c", Name: "read", Arguments: json.RawMessage(`{"path": "/tm`)})
	wantNoStop(t, st)

	if call := payload[map[string]any](t, h.events(t)[0]); call["arguments"] != `{"path": "/tm` {
		t.Errorf("arguments = %v", call["arguments"])
	}
}

// --- the budget ---

func usage(in, out int) agent.UsageEvent {
	return agent.UsageEvent{Model: "m", Usage: agent.Usage{InputTokens: in, OutputTokens: out}}
}

func TestUsagePastTheBudgetStopsTheRun(t *testing.T) {
	h := newHeldRun(t)

	st := feed(t, h.store, h.id, usage(400_000, 200_000), usage(300_000, 100_001))

	if got := st.all(); len(got) != 1 || !errors.Is(got[0], ErrBudget) {
		t.Fatalf("stops = %v, want one ErrBudget", got)
	}
	// The usage that went over is recorded, so the run's history shows why.
	got := h.events(t)
	if last := payload[map[string]any](t, got[len(got)-1]); last["spent"] != 1_000_001.0 {
		t.Errorf("the last usage says spent %v, want 1000001", last["spent"])
	}
}

func TestUsageExactlyAtTheBudgetDoesNotStopTheRun(t *testing.T) {
	h := newHeldRun(t)

	wantNoStop(t, feed(t, h.store, h.id, usage(600_000, 400_000)))
}

// A resumed run is a new process with nothing in memory; what it has spent
// is the row's.
func TestUsageCarriesOnFromWhatWasSpentBeforeAResume(t *testing.T) {
	h := newHeldRun(t)
	if _, err := h.raw.Exec(t.Context(),
		`UPDATE runs SET input_tokens = 900000, output_tokens = 99000 WHERE id = $1`, h.id); err != nil {
		t.Fatalf("spend before the resume: %v", err)
	}

	st := feed(t, h.store, h.id, usage(1500, 500))
	if got := st.all(); len(got) != 1 || !errors.Is(got[0], ErrBudget) {
		t.Errorf("stops = %v, want ErrBudget after 999000 + 2000", got)
	}
}

// Events still in flight when the budget runs out are recorded: the run is
// stopping, not lost.
func TestARunStoppedForItsBudgetStillRecordsWhatFollows(t *testing.T) {
	h := newHeldRun(t)

	feed(t, h.store, h.id, usage(1_000_001, 0), agent.ToolResultEvent{ID: "c", Name: "read", Output: "late"})

	if got := kinds(h.events(t)); !slices.Equal(got, []string{"usage", "tool_result"}) {
		t.Errorf("events = %v, want the late result recorded", got)
	}
}

// --- a lost run ---

// counting is an EventStore that counts what reaches it.
type counting struct {
	EventStore
	mu      sync.Mutex
	appends int
	usages  int
}

func (c *counting) AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error) {
	c.mu.Lock()
	c.appends++
	c.mu.Unlock()
	return c.EventStore.AppendRunEvent(ctx, arg)
}

func (c *counting) AddRunUsage(ctx context.Context, arg db.AddRunUsageParams) (int64, error) {
	c.mu.Lock()
	c.usages++
	c.mu.Unlock()
	return c.EventStore.AddRunUsage(ctx, arg)
}

func (h heldRun) takenOver(t *testing.T) {
	t.Helper()

	if _, err := h.raw.Exec(t.Context(), `UPDATE runs SET worker = 'w2' WHERE id = $1`, h.id); err != nil {
		t.Fatalf("take over: %v", err)
	}
}

func TestAnAppendForALostRunStopsWithErrLostRun(t *testing.T) {
	h := newHeldRun(t)
	h.takenOver(t)
	c := &counting{EventStore: h.store}

	st := feed(t, c, h.id, agent.StepEvent{Step: 1}, agent.StepEvent{Step: 2}, usage(10, 10))

	if got := st.all(); len(got) != 1 || !errors.Is(got[0], ErrLostRun) {
		t.Errorf("stops = %v, want one ErrLostRun", got)
	}
	// Once lost, every write would be refused; the recorder reads on so the
	// SDK never blocks, and asks nothing more of the database.
	if c.appends != 1 || c.usages != 0 {
		t.Errorf("after losing the run: %d appends, %d usage writes; want 1 and 0", c.appends, c.usages)
	}
	if got := h.events(t); len(got) != 0 {
		t.Errorf("the lost worker wrote %v", kinds(got))
	}
}

func TestUsageForALostRunStopsWithErrLostRun(t *testing.T) {
	h := newHeldRun(t)
	h.takenOver(t)

	st := feed(t, h.store, h.id, usage(10, 10))
	if got := st.all(); len(got) != 1 || !errors.Is(got[0], ErrLostRun) {
		t.Errorf("stops = %v, want ErrLostRun", got)
	}
	var spent int64
	if err := h.raw.QueryRow(t.Context(), `SELECT input_tokens + output_tokens FROM runs WHERE id = $1`, h.id).Scan(&spent); err != nil || spent != 0 {
		t.Errorf("spent = %d, %v; want nothing counted by the worker that lost it", spent, err)
	}
}

// --- failures of ours ---

type failing struct{ err error }

func (f failing) AppendRunEvent(context.Context, db.AppendRunEventParams) (int32, error) {
	return 0, f.err
}

func (f failing) AddRunUsage(context.Context, db.AddRunUsageParams) (int64, error) {
	return 0, f.err
}

// A run whose history cannot be written is stopped with the reason, which is
// not a lost run: nothing else holds it.
func TestAFailedWriteStopsTheRunWithItsReason(t *testing.T) {
	down := errors.New("connection reset by peer")

	st := feed(t, failing{err: down}, uuid.New(), agent.StepEvent{Step: 1}, usage(1, 1))

	got := st.all()
	if len(got) != 2 {
		t.Fatalf("stops = %v, want one per failed write", got)
	}
	for _, cause := range got {
		if !errors.Is(cause, down) || errors.Is(cause, ErrLostRun) {
			t.Errorf("stop(%v), want %v and not a lost run", cause, down)
		}
	}
}

func TestTheRecorderDrainsEverythingBeforeItSaysDone(t *testing.T) {
	h := newHeldRun(t)
	events := make([]agent.Event, 50)
	for i := range events {
		events[i] = agent.StepEvent{Step: i + 1}
	}

	feed(t, h.store, h.id, events...)

	if got := len(h.events(t)); got != 50 {
		t.Errorf("%d events stored when done closed, want 50", got)
	}
}
