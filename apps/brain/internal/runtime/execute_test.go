package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

// --- a scripted model ---

// turn is one answer the scripted model gives, in order.
type turn struct {
	resp agent.Response
	err  error
	// block waits for the run's context to end, as a model call in flight
	// does when the run is stopped under it.
	block bool
	// delay holds the answer back, so a run lasts long enough to heartbeat.
	delay time.Duration
}

type scripted struct {
	mu     sync.Mutex
	turns  []turn
	calls  []agent.Request
	called chan struct{}
}

func newScripted(turns ...turn) *scripted {
	return &scripted{turns: turns, called: make(chan struct{}, 16)}
}

func (m *scripted) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	if len(m.turns) == 0 {
		m.mu.Unlock()
		return agent.Response{}, errors.New("the run asked for more turns than the script has")
	}
	t := m.turns[0]
	m.turns = m.turns[1:]
	m.mu.Unlock()

	select {
	case m.called <- struct{}{}:
	default:
	}

	if t.block {
		<-ctx.Done()
		return agent.Response{}, ctx.Err()
	}
	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		}
	}
	return t.resp, t.err
}

func (*scripted) Stream(context.Context, agent.Request) (agent.Stream, error) {
	return nil, errors.New("these runs do not stream")
}

func (m *scripted) requests() []agent.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// waitForACall reports whether the run called the model within ten seconds.
// It runs beside Execute, off the test goroutine, so it cannot fail the test.
func (m *scripted) waitForACall() bool {
	select {
	case <-m.called:
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

func answer(text string, in, out int) turn {
	return turn{resp: agent.Response{
		Message: agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: text}}},
		Finish:  agent.FinishStop,
		Usage:   agent.Usage{InputTokens: in, OutputTokens: out},
	}}
}

func callTool(id, name, args string) turn {
	return turn{resp: agent.Response{
		Message: agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(args)}}},
		Finish:  agent.FinishToolCalls,
		Usage:   agent.Usage{InputTokens: 10, OutputTokens: 5},
	}}
}

func blocked() turn { return turn{block: true} }

// --- the fixture ---

const runInput = "triage the bug reports"

type execFixture struct {
	store    *store.Store
	raw      *pgx.Conn
	sessions *postgres.Store
	pool     *pgxpool.Pool
	dsn      string
	model    *scripted
	exec     *Executor
	team     uuid.UUID
	agent    uuid.UUID
	run      uuid.UUID
	graph    *fakeSearcher
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newExecFixture is a queued run of an agent named triage, on the react loop,
// with an executor whose runner reaches the scripted model.
func newExecFixture(t *testing.T, model *scripted) *execFixture {
	t.Helper()

	s, raw, dsn := liveSchema(t)
	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	f := &execFixture{store: s, raw: raw, model: model, team: team.ID, graph: &fakeSearcher{}}
	if err := raw.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'triage', 'test/model', 1024, 'react', 5) RETURNING id`, team.ID).Scan(&f.agent); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	var session uuid.UUID
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, team.ID).Scan(&session); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	run, err := s.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: team.ID, SessionID: session, AgentID: f.agent, Input: runInput,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	f.run = run.ID

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	f.pool, f.sessions, f.dsn = pool, postgres.New(pool), dsn

	f.exec = f.executor(t, func(agent.Endpoint) (agent.ModelClient, error) { return model, nil },
		agent.Endpoint{BaseURL: "http://model.test"})
	return f
}

func (f *execFixture) executor(t *testing.T, clients agent.ClientFactory, endpoint agent.Endpoint) *Executor {
	t.Helper()

	runner, err := agent.New(clients, Patterns()...)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	builder := NewBuilder(f.store, &fakeObjects{data: map[string]string{}}, &fakeClients{client: &http.Client{}},
		&GraphDeps{Search: f.graph, Embed: &fakeEmbedder{}, Store: f.store})
	e := NewExecutor(f.store, builder, runner, endpoint, f.sessions, quietLogger())
	e.every = 20 * time.Millisecond
	return e
}

// execute runs Execute with a watchdog: a run that never ends fails the test
// rather than the package's timeout.
func (f *execFixture) execute(t *testing.T, ctx context.Context) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- f.exec.Execute(ctx, f.run, worker) }()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("Execute had not returned after 20s")
		return nil
	}
}

// onFirstCall runs sql on the run once the model is first called, from beside
// Execute, and waits for it before the test cleans up.
func (f *execFixture) onFirstCall(t *testing.T, sql string) {
	t.Helper()

	var failed atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !f.model.waitForACall() {
			failed.Store("the run never called the model")
			return
		}
		if _, err := f.pool.Exec(context.Background(), sql, f.run); err != nil {
			failed.Store(err.Error())
		}
	}()
	t.Cleanup(func() {
		<-done
		if msg, ok := failed.Load().(string); ok {
			t.Errorf("%s: %s", sql, msg)
		}
	})
}

func (f *execFixture) mustExecute(t *testing.T) {
	t.Helper()

	if err := f.execute(t, t.Context()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func (f *execFixture) row(t *testing.T) db.Run {
	t.Helper()

	r, err := f.store.GetRun(t.Context(), f.run)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return r
}

func (f *execFixture) events(t *testing.T) []db.RunEvent {
	t.Helper()

	got, err := f.store.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: f.run, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	return got
}

func (f *execFixture) mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()

	if _, err := f.raw.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func wantStatus(t *testing.T, r db.Run, status, errorSays string) {
	t.Helper()

	if r.Status != status {
		t.Fatalf("status = %s (error %v), want %s", r.Status, deref(r.Error), status)
	}
	if errorSays == "" {
		if r.Error != nil {
			t.Errorf("error = %q, want none", *r.Error)
		}
		return
	}
	if r.Error == nil || !strings.Contains(*r.Error, errorSays) {
		t.Errorf("error = %q, want it to say %q", deref(r.Error), errorSays)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func textOf(msgs []agent.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}

// --- a run to its end ---

func TestARunThatAnswersSucceedsWithItsOutput(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 100, 20)))

	f.mustExecute(t)

	r := f.row(t)
	wantStatus(t, r, "succeeded", "")
	if deref(r.Output) != "42" || r.InputTokens != 100 || r.OutputTokens != 20 || r.FinishedAt == nil {
		t.Errorf("output %q, tokens %d/%d, finished %v", deref(r.Output), r.InputTokens, r.OutputTokens, r.FinishedAt)
	}

	events := f.events(t)
	if got, want := kinds(events), []string{"started", "step", "usage", "finished"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	started := payload[startedPayload](t, events[0])
	if started.Agent != "triage" || started.Model != "test/model" || started.Pattern != "react" ||
		started.Tools == nil || started.Skills == nil {
		t.Errorf("started = %+v", started)
	}
	if fin := payload[finishedPayload](t, events[3]); fin.Status != "succeeded" || fin.Error != "" {
		t.Errorf("finished = %+v", fin)
	}

	first := f.model.requests()[0]
	if len(first.Messages) != 1 || first.Messages[0].Text() != runInput {
		t.Errorf("the first request carried %q, want the run's input alone", textOf(first.Messages))
	}
}

func TestARunThatCallsAToolRecordsTheCallAndTheResult(t *testing.T) {
	f := newExecFixture(t, newScripted(callTool("c1", "fetch", `{}`), answer("done", 10, 5)))
	server := serve(t, "fetch")
	var id uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`INSERT INTO mcp_servers (team_id, name, url, bare) VALUES ($1, 'web', $2, true) RETURNING id`,
		f.team, server.url).Scan(&id); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	f.mustExec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, f.agent, id, f.team)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	events := f.events(t)
	want := []string{"started", "step", "usage", "tool_call", "tool_result", "step", "usage", "finished"}
	if got := kinds(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if tools := payload[startedPayload](t, events[0]).Tools; !slices.Equal(tools, []string{"fetch", "graph"}) {
		t.Errorf("started lists tools %v, want [fetch graph]", tools)
	}
	if result := payload[toolResult](t, events[4]); result.ID != "c1" || result.Output != "fetch ran" {
		t.Errorf("tool_result = %+v", result)
	}
	server.waitClosed(t, 1)
}

func TestACommandServerIsReportedAsUnavailable(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("ok", 1, 1)))
	var id uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`INSERT INTO mcp_servers (team_id, name, command) VALUES ($1, 'files', ARRAY['npx', 'files']) RETURNING id`,
		f.team).Scan(&id); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	f.mustExec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, f.agent, id, f.team)

	f.mustExecute(t)

	events := f.events(t)
	if got := kinds(events); len(got) < 2 || got[1] != "tool_unavailable" {
		t.Fatalf("events = %v, want tool_unavailable after started", got)
	}
	if u := payload[unavailablePayload](t, events[1]); u.Name != "files" || !strings.Contains(u.Reason, "workspace") {
		t.Errorf("tool_unavailable = %+v", u)
	}
}

func TestAGrantedSkillCanBeLoaded(t *testing.T) {
	f := newExecFixture(t, newScripted(callTool("c1", agent.SkillToolName, `{"name":"writing"}`), answer("ok", 1, 1)))
	var id uuid.UUID
	if err := f.raw.QueryRow(t.Context(), `
		INSERT INTO skills (team_id, name, description, body)
		VALUES ($1, 'writing', 'House style.', 'Write plainly.') RETURNING id`, f.team).Scan(&id); err != nil {
		t.Fatalf("insert skill: %v", err)
	}
	f.mustExec(t, `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, f.agent, id, f.team)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	requests := f.model.requests()
	if len(requests) != 2 || !strings.Contains(textOf(requests[1].Messages), "Write plainly.") {
		t.Errorf("the request after loading the skill did not carry its body: %d requests", len(requests))
	}
	if skills := payload[startedPayload](t, f.events(t)[0]).Skills; !slices.Equal(skills, []string{"writing"}) {
		t.Errorf("started lists skills %v", skills)
	}
}

func TestTheTranscriptIsSavedUnderTheRunID(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 1, 1)))

	f.mustExecute(t)

	msgs, err := f.sessions.Messages(t.Context(), f.run.String())
	if err != nil || len(msgs) != 2 || msgs[0].Text() != runInput || msgs[1].Text() != "42" {
		t.Errorf("transcript = %q, %v; want the input and the answer", textOf(msgs), err)
	}
}

// A run whose worker died part-way is picked up by another, which has only
// the database: what was said so far is the transcript, not the input.
func TestAResumedRunContinuesFromItsTranscript(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("carried on", 1, 1)))
	earlier := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "the question asked before"}}},
	}
	if err := f.sessions.Save(t.Context(), f.run.String(), earlier); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	f.mustExecute(t)

	first := textOf(f.model.requests()[0].Messages)
	if !strings.Contains(first, "the question asked before") || strings.Contains(first, runInput) {
		t.Errorf("the first request carried %q, want the transcript and not the input", first)
	}
	events := f.events(t)
	if got := kinds(events); got[0] != "resumed" || slices.Contains(got, "started") {
		t.Errorf("events = %v, want resumed and no started", got)
	}
	if p := payload[resumedPayload](t, events[0]); p.Messages != 1 {
		t.Errorf("resumed = %+v, want 1 message", p)
	}
}

// --- stopping ---

func TestACancelRequestStopsTheRunAtTheNextHeartbeat(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	f.onFirstCall(t, `UPDATE runs SET cancel_requested_at = now() WHERE id = $1`)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "cancelled", "")
	events := f.events(t)
	if fin := payload[finishedPayload](t, events[len(events)-1]); fin.Status != "cancelled" {
		t.Errorf("the last event is %s %+v, want finished cancelled", events[len(events)-1].Kind, fin)
	}
}

func TestADequeuedRunThatWasCancelledDoesNothing(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("never", 1, 1)))
	w := f.spy()
	f.mustExec(t, `UPDATE runs SET status = 'cancelled', cancel_requested_at = now(), finished_at = now() WHERE id = $1`, f.run)

	f.mustExecute(t)

	if r := f.row(t); r.Status != "cancelled" || r.Worker != nil {
		t.Errorf("status %s, worker %v; want it untouched", r.Status, r.Worker)
	}
	if n := len(f.model.requests()); n != 0 {
		t.Errorf("the model was called %d times", n)
	}
	if got := f.events(t); len(got) != 0 {
		t.Errorf("events = %v, want none", kinds(got))
	}
	if w.finishes.Load() != 0 || w.appends.Load() != 0 {
		t.Errorf("%d finishes and %d appends tried for a run that was not ours", w.finishes.Load(), w.appends.Load())
	}
}

func TestAWorkerThatLostItsRunStopsAndWritesNothing(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	w := f.spy()
	f.onFirstCall(t, `UPDATE runs SET worker = 'w2' WHERE id = $1`)

	f.mustExecute(t)

	r := f.row(t)
	if r.Status != "running" || deref(r.Worker) != "w2" || r.Error != nil || r.FinishedAt != nil {
		t.Errorf("status %s, worker %s, error %s; want it left running for w2", r.Status, deref(r.Worker), deref(r.Error))
	}
	if got := kinds(f.events(t)); slices.Contains(got, "finished") {
		t.Errorf("events = %v; the worker that lost the run wrote its end", got)
	}
	// FinishRun would refuse it anyway; not asking is what keeps a lost worker
	// out of the way if that guard ever changes.
	if n := w.finishes.Load(); n != 0 {
		t.Errorf("the worker that lost the run tried to finish it %d times", n)
	}
}

// A deploy stops every worker. The runs they held are not failures: they stay
// running, and the heartbeat going stale hands them to a worker still up.
func TestAWorkerShuttingDownLeavesItsRunToBeTakenOver(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	ctx, shutdown := context.WithCancel(t.Context())
	go func() {
		if !f.model.waitForACall() {
			return
		}
		shutdown()
	}()

	if err := f.execute(t, ctx); err == nil {
		t.Error("Execute = nil for a worker that stopped mid-run")
	}

	if r := f.row(t); r.Status != "running" || r.FinishedAt != nil {
		t.Errorf("status %s, finished %v; want it left running", r.Status, r.FinishedAt)
	}
	if got := kinds(f.events(t)); slices.Contains(got, "finished") {
		t.Errorf("events = %v, want no finished", got)
	}
}

// writeSpy counts the writes Execute tries, which the database's own guards
// would otherwise refuse without anyone seeing.
type writeSpy struct {
	Store
	finishes atomic.Int32
	appends  atomic.Int32
}

func (s *writeSpy) FinishRun(ctx context.Context, arg db.FinishRunParams) (int64, error) {
	s.finishes.Add(1)
	return s.Store.FinishRun(ctx, arg)
}

func (s *writeSpy) AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error) {
	s.appends.Add(1)
	return s.Store.AppendRunEvent(ctx, arg)
}

func (f *execFixture) spy() *writeSpy {
	w := &writeSpy{Store: f.exec.store}
	f.exec.store = w
	return w
}

// flakyBeat fails the first heartbeat, as a database blip would.
type flakyBeat struct {
	*store.Store
	failed atomic.Bool
}

func (f *flakyBeat) HeartbeatRun(ctx context.Context, arg db.HeartbeatRunParams) (*time.Time, error) {
	if f.failed.CompareAndSwap(false, true) {
		return nil, errors.New("connection reset by peer")
	}
	return f.Store.HeartbeatRun(ctx, arg)
}

func TestAHeartbeatThatFailsOnceDoesNotStopTheRun(t *testing.T) {
	ok := answer("42", 1, 1)
	ok.delay = 200 * time.Millisecond
	f := newExecFixture(t, newScripted(ok))
	flaky := &flakyBeat{Store: f.store}
	f.exec.store = flaky

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if !flaky.failed.Load() {
		t.Error("no heartbeat ran during a 200ms call at a 20ms period")
	}
}

// --- failing ---

func TestARunWhoseAgentWasDeletedFailsNamingIt(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("never", 1, 1)))
	f.mustExec(t, `DELETE FROM agents WHERE id = $1`, f.agent)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "failed", `the agent "triage" was deleted`)
	if n := len(f.model.requests()); n != 0 {
		t.Errorf("the model was called %d times", n)
	}
	if got := kinds(f.events(t)); !slices.Equal(got, []string{"finished"}) {
		t.Errorf("events = %v, want only finished", got)
	}
}

func TestAnUnknownLoopFailsNamingTheKnownOnes(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("never", 1, 1)))
	f.mustExec(t, `UPDATE agents SET pattern = 'critic-pass' WHERE id = $1`, f.agent)

	f.mustExecute(t)

	r := f.row(t)
	wantStatus(t, r, "failed", "critic-pass")
	for _, loop := range Loops() {
		if !strings.Contains(deref(r.Error), loop) {
			t.Errorf("error = %q, which does not name %q", deref(r.Error), loop)
		}
	}
}

func TestAnUnreachableModelFailsNamingTheSetting(t *testing.T) {
	f := newExecFixture(t, newScripted())
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	f.exec = f.executor(t, openaicompat.Factory(), agent.Endpoint{BaseURL: gone.URL + "/v1", APIKey: "test"})

	f.mustExecute(t)

	wantStatus(t, f.row(t), "failed", "OPENARITY_LLM_BASE_URL")
}

func TestARunPastItsTokenBudgetFailsNamingIt(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("expensive", maxTokens, 1)))

	f.mustExecute(t)

	wantStatus(t, f.row(t), "failed", "token budget")
}

// The executor takes the budget from the run's row, not the default.
func TestARunStopsAtItsOwnBudget(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("modest", 40, 20)))
	f.mustExec(t, `UPDATE runs SET token_budget = 50 WHERE id = $1`, f.run)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "failed", "token budget")
}

// A run taken over five hours in has one hour left, not six. The clock is
// started_at's, read on the database.
func TestTheWallClockIsTheOneLeftNotAFreshSixHours(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	f.mustExec(t, `
		UPDATE runs SET status = 'running', worker = $2, heartbeat_at = now(),
		    started_at = now() - interval '6 hours' + interval '1 second'
		WHERE id = $1`, f.run, worker)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "failed", "wall clock")
}

// --- what the security review found ---

// beats fails or passes each heartbeat by its own rule, as a database that
// is down, or coming and going, would.
type beats struct {
	Store
	fail func(n int) bool
	n    atomic.Int32
}

func (b *beats) HeartbeatRun(ctx context.Context, arg db.HeartbeatRunParams) (*time.Time, error) {
	if b.fail(int(b.n.Add(1))) {
		return nil, errors.New("connection refused")
	}
	return b.Store.HeartbeatRun(ctx, arg)
}

// A worker that cannot reach the database for longer than a sweep waits
// before taking its run over has to assume it was: two workers on one run
// pay for every token twice and run every tool twice.
func TestHeartbeatsFailingTooLongStopTheRunAsLost(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	f.exec.store = &beats{Store: f.exec.store, fail: func(int) bool { return true }}
	f.exec.lostAfter = 100 * time.Millisecond
	w := f.spy()

	f.mustExecute(t)

	if r := f.row(t); r.Status != "running" || r.FinishedAt != nil {
		t.Errorf("status %s; want it left running for whoever takes it over", r.Status)
	}
	if n := w.finishes.Load(); n != 0 {
		t.Errorf("tried to finish a run it had lost contact with %d times", n)
	}
}

func TestHeartbeatsThatRecoverKeepTheRun(t *testing.T) {
	slow := answer("42", 1, 1)
	slow.delay = 800 * time.Millisecond
	f := newExecFixture(t, newScripted(slow))
	f.exec.store = &beats{Store: f.exec.store, fail: func(n int) bool { return n%2 == 1 }}
	f.exec.lostAfter = 300 * time.Millisecond

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
}

// The run's error is shown to everyone who can read its session, and some
// endpoints carry their key in the URL.
func TestAnUnreachableModelsURLIsNotStored(t *testing.T) {
	f := newExecFixture(t, newScripted())
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	f.exec = f.executor(t, openaicompat.Factory(), agent.Endpoint{BaseURL: gone.URL + "/key-abc123/v1", APIKey: "test"})

	f.mustExecute(t)

	r := f.row(t)
	wantStatus(t, r, "failed", "OPENARITY_LLM_BASE_URL")
	if strings.Contains(deref(r.Error), "key-abc123") {
		t.Errorf("error = %q, which carries the endpoint's URL", deref(r.Error))
	}
	for _, e := range f.events(t) {
		if strings.Contains(string(e.Payload), "key-abc123") {
			t.Errorf("the %s event carries the endpoint's URL: %s", e.Kind, e.Payload)
		}
	}
}

// A model that answers with an error status fails the run naming the setting
// to fix, with the status and the provider's code, and nothing of the URL or
// the body: the SDK hands the brain a ModelError and nothing more.
func TestAModelThatAnswersAnErrorFailsNamingTheSetting(t *testing.T) {
	f := newExecFixture(t, newScripted())
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: sk-live-HUNTER2","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(refusing.Close)
	f.exec = f.executor(t, openaicompat.Factory(), agent.Endpoint{BaseURL: refusing.URL + "/key-abc123/v1", APIKey: "test"})

	f.mustExecute(t)

	r := f.row(t)
	want := "OPENARITY_LLM_BASE_URL: the model endpoint answered 401 Unauthorized (invalid_api_key)"
	if r.Status != "failed" || deref(r.Error) != want {
		t.Errorf("run is %s, %q; want failed, %q", r.Status, deref(r.Error), want)
	}
	for _, e := range f.events(t) {
		for _, leak := range []string{"key-abc123", "HUNTER2"} {
			if strings.Contains(string(e.Payload), leak) {
				t.Errorf("the %s event carries %q: %s", e.Kind, leak, e.Payload)
			}
		}
	}
}

// The evaluation record the orchestrator spec asks for: a graph call and what
// came back, scores and paths included, read back from run_events.
func TestAGraphCallIsRecordedWithWhatItFound(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("g1", GraphToolName, `{"query":"who reconciles invoices","kinds":["agents"],"hops":1}`),
		answer("ok", 1, 1)))
	f.graph.hits = []graph.Hit{{
		Kind: graph.KindAgent, ID: uuid.New(), Name: "clerk", Description: "Reconciles invoices", Score: 0.91234,
		Path: []graph.Step{{Kind: graph.KindAgent, Name: "clerk"}, {Kind: graph.KindSkill, Name: "invoices"}},
	}}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	events := f.events(t)
	call := payload[toolCallPayload](t, events[3])
	if call.Name != "graph" || !strings.Contains(call.Arguments, "who reconciles invoices") {
		t.Errorf("tool_call = %+v, want the graph call and its query", call)
	}
	result := payload[toolResult](t, events[4])
	want := `{"results":[{"kind":"agent","name":"clerk","description":"Reconciles invoices","score":0.912,` +
		`"path":[{"kind":"agent","name":"clerk"},{"kind":"skill","name":"invoices"}]}]}`
	if result.Name != "graph" || result.Output != want {
		t.Errorf("tool_result = %+v, want the output %s", result, want)
	}
	if s := f.graph.asked; len(s) != 1 || s[0].Hops != 1 || s[0].Reach.Agents != nil {
		t.Errorf("searched %+v, want hops 1 and a reach with no other agents in this team", s)
	}
}

// --- a child run ---

const childBrief = "Objective: refund the duplicate charge on order 4411\n\nReturn: the refund id"

type delegatedEvent struct {
	CallID string    `json:"call_id"`
	Agent  string    `json:"agent"`
	RunID  uuid.UUID `json:"run_id"`
	Depth  int32     `json:"depth"`
}

// delegation is the fixture's run as a worker holds it mid-step, handing a
// brief to a second agent, billing.
func (f *execFixture) delegation(t *testing.T, callID string) Delegation {
	t.Helper()

	f.mustExec(t, `UPDATE runs SET status = 'running', worker = $2, heartbeat_at = now(), started_at = now()
		WHERE id = $1`, f.run, worker)
	var billing uuid.UUID
	if err := f.raw.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'billing', 'test/model', 1024, 'react', 5) RETURNING id`, f.team).Scan(&billing); err != nil {
		t.Fatalf("insert billing: %v", err)
	}
	a, err := f.store.GetAgent(t.Context(), billing)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	return Delegation{
		Parent: f.row(t), Agent: a, Brief: childBrief, CallID: callID,
		Lineage: Lineage{Worker: worker, Chain: []uuid.UUID{f.agent}},
	}
}

// delegate runs Delegate with the same watchdog execute has.
func (f *execFixture) delegate(t *testing.T, ctx context.Context, d Delegation) (db.Run, error) {
	t.Helper()

	type result struct {
		row db.Run
		err error
	}
	done := make(chan result, 1)
	go func() {
		row, err := f.exec.Delegate(ctx, d)
		done <- result{row, err}
	}()
	select {
	case r := <-done:
		return r.row, r.err
	case <-time.After(20 * time.Second):
		t.Fatal("Delegate had not returned after 20s")
		return db.Run{}, nil
	}
}

func (f *execFixture) children(t *testing.T, parent uuid.UUID) []db.Run {
	t.Helper()

	rows, err := f.raw.Query(t.Context(), `SELECT id FROM runs WHERE parent_run_id = $1 ORDER BY created_at, id`, parent)
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	out := make([]db.Run, len(ids))
	for i, id := range ids {
		if out[i], err = f.store.GetRun(t.Context(), id); err != nil {
			t.Fatalf("GetRun %s: %v", id, err)
		}
	}
	return out
}

func (f *execFixture) eventsOf(t *testing.T, run uuid.UUID) []db.RunEvent {
	t.Helper()

	got, err := f.store.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: run, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	return got
}

// stopOnFirstCall ends ctx with cause once the model is first called, as the
// parent's heartbeat or clock would end the parent's run under the child.
func (f *execFixture) stopOnFirstCall(t *testing.T, cause error) context.Context {
	t.Helper()

	ctx, stop := context.WithCancelCause(t.Context())
	go func() {
		if f.model.waitForACall() {
			stop(cause)
		}
	}()
	return ctx
}

func TestAChildRunsAndItsRowIsFinished(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("refund re_118 issued", 3, 2)))
	d := f.delegation(t, "call_7")

	child, err := f.delegate(t, t.Context(), d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "succeeded", "")
	if deref(child.Output) != "refund re_118 issued" {
		t.Errorf("output = %q", deref(child.Output))
	}
	if child.ParentRunID == nil || *child.ParentRunID != f.run || child.RootRunID != d.Parent.RootRunID ||
		child.SessionID != d.Parent.SessionID || child.Depth != 1 {
		t.Errorf("child parent %v root %s session %s depth %d; want under the parent at depth 1",
			child.ParentRunID, child.RootRunID, child.SessionID, child.Depth)
	}
	if child.AgentID == nil || *child.AgentID != d.Agent.ID || child.AgentName != "billing" || child.Input != childBrief {
		t.Errorf("child agent %v %q input %q; want billing with the brief", child.AgentID, child.AgentName, child.Input)
	}
	// The call id alone repeats (ReWOO numbers its steps per plan, and some
	// providers reuse ids), so the child's own id makes the key unique.
	if want := "call_7#" + child.ID.String(); deref(child.ParentToolCall) != want {
		t.Errorf("parent_tool_call = %q, want %q", deref(child.ParentToolCall), want)
	}
	if child.InputTokens != 3 || child.OutputTokens != 2 {
		t.Errorf("tokens %d/%d, want 3/2 counted on the child", child.InputTokens, child.OutputTokens)
	}
	if got := f.row(t); got.Status != "running" || got.FinishedAt != nil {
		t.Errorf("the parent is %s; a child finishing must leave its parent running", got.Status)
	}

	events := f.eventsOf(t, child.ID)
	if got := kinds(events); got[0] != "started" || got[len(got)-1] != "finished" {
		t.Errorf("child events = %v, want started … finished, as a root's", got)
	}
	if p := payload[startedPayload](t, events[0]); p.Agent != "billing" {
		t.Errorf("started = %+v, want billing", p)
	}
	first := textOf(f.model.requests()[0].Messages)
	if !strings.Contains(first, childBrief) || strings.Contains(first, runInput) {
		t.Errorf("the child's first request carried %q; want the brief and nothing of the parent's", first)
	}
}

// The parent's event is what lets the dashboard open the child while it works,
// so it must be written before the child does anything.
func TestTheParentRecordsTheChildBeforeItStarts(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("done", 1, 1)))
	d := f.delegation(t, "call_7")

	child, err := f.delegate(t, t.Context(), d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	parent := f.eventsOf(t, f.run)
	if len(parent) != 1 || parent[0].Kind != "delegated" {
		t.Fatalf("parent events = %v, want one delegated", kinds(parent))
	}
	want := delegatedEvent{CallID: "call_7", Agent: "billing", RunID: child.ID, Depth: 1}
	if got := payload[delegatedEvent](t, parent[0]); got != want {
		t.Errorf("delegated = %+v, want %+v", got, want)
	}
	started := f.eventsOf(t, child.ID)[0]
	if parent[0].CreatedAt.After(started.CreatedAt) {
		t.Errorf("delegated at %s, after the child started at %s", parent[0].CreatedAt, started.CreatedAt)
	}
}

// The agent is chosen within the parent's team, so a row from another team
// writes nothing, records nothing and calls no model.
func TestAChildWhoseAgentIsNotInTheTeamNeverRuns(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("never", 1, 1)))
	d := f.delegation(t, "call_7")
	other, err := f.store.CreateTeam(t.Context(), "elsewhere")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	f.mustExec(t, `UPDATE agents SET team_id = $2 WHERE id = $1`, d.Agent.ID, other.ID)

	if _, err := f.delegate(t, t.Context(), d); err == nil {
		t.Error("Delegate = nil for an agent outside the team")
	}
	if got := f.children(t, f.run); len(got) != 0 {
		t.Errorf("%d children written", len(got))
	}
	if got := f.eventsOf(t, f.run); len(got) != 0 {
		t.Errorf("parent events = %v, want none", kinds(got))
	}
	if n := len(f.model.requests()); n != 0 {
		t.Errorf("the model was called %d times", n)
	}
}

// Cancelling the parent ends the parent's context, with ErrCancelled as its
// cause; the child's context is the parent's, so the child ends cancelled too.
func TestAParentsCancelReachesTheChild(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	ctx := f.stopOnFirstCall(t, ErrCancelled)

	child, err := f.delegate(t, ctx, d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "cancelled", "")
	if got := kinds(f.eventsOf(t, child.ID)); got[len(got)-1] != "finished" {
		t.Errorf("child events = %v, want it to end finished", got)
	}
}

// POST /runs/{child}/cancel flags the child alone; the child's own heartbeat
// sees it, and the parent is handed a cancelled report and carries on.
func TestCancellingOnlyTheChildLetsTheParentCarryOn(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	f.onFirstCall(t, `UPDATE runs SET cancel_requested_at = now() WHERE parent_run_id = $1`)
	ctx, stop := context.WithCancelCause(t.Context())
	defer stop(nil)

	child, err := f.delegate(t, ctx, d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "cancelled", "")
	if ctx.Err() != nil {
		t.Errorf("the parent's context ended: %v", context.Cause(ctx))
	}
	if r := f.row(t); r.Status != "running" || r.CancelRequestedAt != nil {
		t.Errorf("parent %s, cancel %v; want it running and unflagged", r.Status, r.CancelRequestedAt)
	}
}

// The root's six hours bound the whole tree: the child has no clock of its
// own, so the parent's deadline is the child's.
func TestAChildIsBoundByTheRootsClock(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	ctx, cancel := context.WithTimeoutCause(t.Context(), 200*time.Millisecond, errWallClock)
	defer cancel()

	child, err := f.delegate(t, ctx, d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "failed", "wall clock")
}

// A parent whose worker lost it will be resumed elsewhere, and the resumed
// parent reaps this child. Finishing it here would race that.
func TestALostParentLeavesItsChildUnfinished(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	w := f.spy()
	ctx := f.stopOnFirstCall(t, ErrLostRun)

	// The child did run, so this is not the error that means it never ran.
	if _, err := f.delegate(t, ctx, d); err != nil {
		t.Errorf("Delegate = %v for a child that ran; a lost parent is not a child that never started", err)
	}

	children := f.children(t, f.run)
	if len(children) != 1 {
		t.Fatalf("%d children, want 1", len(children))
	}
	if c := children[0]; c.Status != "running" || c.FinishedAt != nil || c.Error != nil {
		t.Errorf("child %s, error %s; want it left running for the resumed parent", c.Status, deref(c.Error))
	}
	if got := kinds(f.eventsOf(t, children[0].ID)); slices.Contains(got, "finished") {
		t.Errorf("child events = %v; a lost parent's child wrote its end", got)
	}
	if n := w.finishes.Load(); n != 0 {
		t.Errorf("FinishRun was tried %d times", n)
	}
}

// A child runs under its own agent's budget, copied onto its row when it was
// written, not the parent's and not the default.
func TestAChildIsHeldToItsOwnAgentsBudget(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("a long answer", 3, 2), answer("unreached", 1, 1)))
	d := f.delegation(t, "call_7")
	f.mustExec(t, `UPDATE agents SET token_budget = 4 WHERE id = $1`, d.Agent.ID)
	if d.Agent, _ = f.store.GetAgent(t.Context(), d.Agent.ID); d.Agent.TokenBudget == nil {
		t.Fatal("setup: billing has no budget")
	}

	child, err := f.delegate(t, t.Context(), d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "failed", "token budget")
}

// Under decision A a resumed parent asks again rather than finding what it
// asked before, so whatever was still running beneath it fails, at any depth;
// what had finished is left as it was.
func TestAResumedParentReapsItsChildren(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("carried on", 1, 1)))
	start := func(parent uuid.UUID, call string) db.Run {
		t.Helper()
		r, err := f.store.StartChildRun(t.Context(), db.StartChildRunParams{
			ID: uuid.New(), ParentID: parent, AgentID: f.agent, TeamID: f.team,
			ParentToolCall: call, Input: "the work asked for before", Worker: "w-dead",
		})
		if err != nil {
			t.Fatalf("StartChildRun: %v", err)
		}
		return r
	}
	running := start(f.run, "call_1#a")
	grandchild := start(running.ID, "call_9#b")
	done := start(f.run, "call_2#c")
	f.mustExec(t, `UPDATE runs SET status = 'succeeded', output = 'kept', finished_at = now() WHERE id = $1`, done.ID)
	if err := f.sessions.Save(t.Context(), f.run.String(), []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: runInput}}},
	}); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	f.mustExecute(t)

	const sentence = "its parent was resumed after the worker stopped; the work was asked for again"
	for _, id := range []uuid.UUID{running.ID, grandchild.ID} {
		r, err := f.store.GetRun(t.Context(), id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		wantStatus(t, r, "failed", sentence)
		if r.FinishedAt == nil {
			t.Errorf("reaped run %s has no finished_at", id)
		}
	}
	if r, _ := f.store.GetRun(t.Context(), done.ID); r.Status != "succeeded" || deref(r.Output) != "kept" {
		t.Errorf("the finished child became %s %q; want it left as it was", r.Status, deref(r.Output))
	}
	wantStatus(t, f.row(t), "succeeded", "")
}

// A fresh run has nothing beneath it to reap, and must not reach for a
// sibling's: the reap is the resumed run's own subtree.
func TestAFreshRunReapsNothing(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("first time", 1, 1)))
	mine := f.row(t)
	otherRun, err := f.store.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: mine.SessionID, AgentID: f.agent, Input: "elsewhere",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	other := otherRun.ID
	f.mustExec(t, `UPDATE runs SET status = 'running', worker = 'w-other', heartbeat_at = now(), started_at = now()
		WHERE id = $1`, other)
	sibling, err := f.store.StartChildRun(t.Context(), db.StartChildRunParams{
		ID: uuid.New(), ParentID: other, AgentID: f.agent, TeamID: f.team,
		ParentToolCall: "call_1#x", Input: "not ours", Worker: "w-other",
	})
	if err != nil {
		t.Fatalf("StartChildRun: %v", err)
	}

	f.mustExecute(t)

	if r, _ := f.store.GetRun(t.Context(), sibling.ID); r.Status != "running" {
		t.Errorf("another run's child became %s", r.Status)
	}
}

// The same call id twice, as a ReWOO plan's #E1 in two plans, or a provider
// reusing ids, starts two children and hands each its own report.
func TestTheSameCallTwiceMakesTwoChildren(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("first", 1, 1), answer("second", 1, 1)))
	d := f.delegation(t, "#E1")

	one, err := f.delegate(t, t.Context(), d)
	if err != nil {
		t.Fatalf("Delegate 1: %v", err)
	}
	two, err := f.delegate(t, t.Context(), d)
	if err != nil {
		t.Fatalf("Delegate 2: %v", err)
	}

	if one.ID == two.ID || deref(one.Output) != "first" || deref(two.Output) != "second" {
		t.Errorf("children %s %q and %s %q; want two, each with its own answer",
			one.ID, deref(one.Output), two.ID, deref(two.Output))
	}
}

// A deploy stops the worker under the parent and the child alike. DBOS ends
// the worker's context with a cause of its own, not context.Canceled, so the
// child must tell "the worker stopped" from the brain's own causes by those.
func TestAWorkerShuttingDownLeavesTheChildRunning(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	w := f.spy()
	ctx := f.stopOnFirstCall(t, errors.New("dbos: the executor is shutting down"))

	if _, err := f.delegate(t, ctx, d); err == nil {
		t.Error("Delegate = nil for a worker that stopped mid-child")
	}

	children := f.children(t, f.run)
	if len(children) != 1 || children[0].Status != "running" || children[0].FinishedAt != nil {
		t.Fatalf("children = %+v, want one left running", children)
	}
	if n := w.finishes.Load(); n != 0 {
		t.Errorf("FinishRun was tried %d times", n)
	}
}

// refusesDelegated fails the parent's delegated event, as a database that
// went away between writing the child and recording it would.
type refusesDelegated struct{ Store }

func (s *refusesDelegated) AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error) {
	if arg.Kind == "delegated" {
		return 0, errors.New("the database went away")
	}
	return s.Store.AppendRunEvent(ctx, arg)
}

// Children are never swept, so one written and then abandoned would stay
// running for as long as its parent lives. It is finished instead.
func TestAnUnrecordedChildIsNotLeftRunning(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("never", 1, 1)))
	d := f.delegation(t, "call_7")
	f.exec.store = &refusesDelegated{Store: f.exec.store}

	if _, err := f.delegate(t, t.Context(), d); err == nil {
		t.Error("Delegate = nil when the delegation could not be recorded")
	}

	children := f.children(t, f.run)
	if len(children) != 1 {
		t.Fatalf("%d children, want 1", len(children))
	}
	wantStatus(t, children[0], "failed", "could not record")
	if n := len(f.model.requests()); n != 0 {
		t.Errorf("the model was called %d times for a child nobody can see", n)
	}
}

// A parent that spends its budget ends its own context with ErrBudget; that
// is the brain's own stop, so the child ends and says why.
func TestAParentOutOfBudgetEndsTheChild(t *testing.T) {
	f := newExecFixture(t, newScripted(blocked()))
	d := f.delegation(t, "call_7")
	ctx := f.stopOnFirstCall(t, ErrBudget)

	child, err := f.delegate(t, ctx, d)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	wantStatus(t, child, "failed", "token budget")
}

// --- delegating, end to end ---

// teamLead sets the fixture's triage to delegate to its team and adds billing.
func (f *execFixture) teamLead(t *testing.T) uuid.UUID {
	t.Helper()

	f.mustExec(t, `UPDATE agents SET delegation = 'team' WHERE id = $1`, f.agent)
	var billing uuid.UUID
	if err := f.raw.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'billing', 'test/model', 1024, 'react', 5) RETURNING id`, f.team).Scan(&billing); err != nil {
		t.Fatalf("insert billing: %v", err)
	}
	return billing
}

const delegateArgs = `{"agent":"billing","objective":"Refund the duplicate charge on order 4411.","output":"The refund id."}`

// The model calls delegate; the executor the worker builds runs billing as a
// child, and the report comes back as the tool's result in the parent's next
// request.
func TestADelegateCallIsRecordedEndToEnd(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("call_1", "delegate", delegateArgs),
		answer("re_118", 2, 2),
		answer("refunded: re_118", 1, 1),
	))
	f.teamLead(t)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if got := deref(f.row(t).Output); got != "refunded: re_118" {
		t.Errorf("output = %q", got)
	}

	children := f.children(t, f.run)
	if len(children) != 1 {
		t.Fatalf("%d children, want 1", len(children))
	}
	child := children[0]
	wantStatus(t, child, "succeeded", "")
	if deref(child.Output) != "re_118" || child.Depth != 1 ||
		child.Input != "Objective: Refund the duplicate charge on order 4411.\n\nReturn: The refund id." {
		t.Errorf("child output %q depth %d input %q", deref(child.Output), child.Depth, child.Input)
	}
	if !strings.HasPrefix(deref(child.ParentToolCall), "call_1#") {
		t.Errorf("parent_tool_call = %q, want the call's id first", deref(child.ParentToolCall))
	}

	parent := f.events(t)
	at := func(kind string) int {
		return slices.IndexFunc(parent, func(e db.RunEvent) bool { return e.Kind == kind })
	}
	if c, d, r := at("tool_call"), at("delegated"), at("tool_result"); c < 0 || c >= d || d >= r {
		t.Fatalf("parent events = %v, want tool_call, delegated, tool_result in order", kinds(parent))
	}
	result := payload[toolResultPayload](t, parent[at("tool_result")])
	if result.Error != "" || !strings.HasPrefix(result.Output, `<report agent="billing" run="`+child.ID.String()+`" status="succeeded" end="`) ||
		!strings.Contains(result.Output, "\nre_118\n</report end=\"") {
		t.Errorf("tool_result = %+v, want billing's report in its envelope", result)
	}
	if got := kinds(f.eventsOf(t, child.ID)); got[0] != "started" || got[len(got)-1] != "finished" {
		t.Errorf("child events = %v, want started … finished", got)
	}

	requests := f.model.requests()
	if last := textOf(requests[len(requests)-1].Messages); !strings.Contains(last, "re_118\n</report end=") {
		t.Errorf("the parent's next request did not carry the report:\n%s", last)
	}
}

// A refusal is the tool's result, an error the model can act on; the run goes
// on and nothing is started.
func TestARefusedDelegationIsAToolErrorAndTheRunGoesOn(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("call_1", "delegate", `{"agent":"nobody","objective":"x","output":"y"}`),
		answer("I will do it myself", 1, 1),
	))
	f.teamLead(t)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if got := f.children(t, f.run); len(got) != 0 {
		t.Errorf("%d children for a refused call", len(got))
	}
	events := f.events(t)
	i := slices.IndexFunc(events, func(e db.RunEvent) bool { return e.Kind == "tool_result" })
	if i < 0 {
		t.Fatalf("events = %v, want a tool_result", kinds(events))
	}
	if r := payload[toolResultPayload](t, events[i]); r.Error != `no agent in this team is named "nobody"` {
		t.Errorf("tool_result error = %q", r.Error)
	}
}

// Review Focus 3: the root's cancel reaches the child at its next heartbeat
// through the child's context, and then ends the root.
func TestCancellingTheRootCancelsTheChildThenTheRoot(t *testing.T) {
	f := newExecFixture(t, newScripted(callTool("call_1", "delegate", delegateArgs), blocked()))
	f.teamLead(t)
	var failed atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The second call is the child's: by then it is running under the root.
		for range 2 {
			if !f.model.waitForACall() {
				failed.Store("the child never called the model")
				return
			}
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE runs SET cancel_requested_at = now() WHERE id = $1`, f.run); err != nil {
			failed.Store(err.Error())
		}
	}()
	t.Cleanup(func() {
		<-done
		if msg, ok := failed.Load().(string); ok {
			t.Error(msg)
		}
	})

	f.mustExecute(t)

	children := f.children(t, f.run)
	if len(children) != 1 {
		t.Fatalf("%d children, want 1", len(children))
	}
	wantStatus(t, children[0], "cancelled", "")
	wantStatus(t, f.row(t), "cancelled", "")
	if children[0].FinishedAt == nil || f.row(t).FinishedAt == nil || children[0].FinishedAt.After(*f.row(t).FinishedAt) {
		t.Errorf("child finished %v, root %v; want the child first", children[0].FinishedAt, f.row(t).FinishedAt)
	}
}

// A child built from its own row gets delegate only if its own setting allows
// it, whatever its parent's.
func TestAChildGetsDelegateOnlyIfItsOwnSettingAllows(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("call_1", "delegate", delegateArgs),
		answer("re_118", 1, 1),
		answer("done", 1, 1),
	))
	f.teamLead(t)

	f.mustExecute(t)

	requests := f.model.requests()
	has := func(r agent.Request) bool {
		return slices.ContainsFunc(r.Tools, func(d agent.Tool) bool { return d.Name == DelegateToolName })
	}
	if !has(requests[0]) {
		t.Error("the root, set to team, was not offered delegate")
	}
	if has(requests[1]) {
		t.Error("the child, set to none, was offered delegate")
	}
}
