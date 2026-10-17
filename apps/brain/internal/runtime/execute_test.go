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

	f := &execFixture{store: s, raw: raw, model: model, team: team.ID}
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
	builder := NewBuilder(f.store, &fakeObjects{data: map[string]string{}}, &fakeClients{client: &http.Client{}})
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
	if tools := payload[startedPayload](t, events[0]).Tools; !slices.Equal(tools, []string{"fetch"}) {
		t.Errorf("started lists tools %v, want [fetch]", tools)
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
