package improve

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// fakeModel answers every call with the next reply in line, and records
// what it was asked.
type fakeModel struct {
	mu      sync.Mutex
	replies []reply
	asked   []agent.Request
	// onAsk runs inside a call, for a test that changes the world meanwhile.
	onAsk func()
}

type reply struct {
	text string
	err  error
}

func (f *fakeModel) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req)
	if f.onAsk != nil {
		f.onAsk()
	}
	if len(f.replies) == 0 {
		return agent.Response{}, errors.New("the fake model has no reply left")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.err != nil {
		return agent.Response{}, r.err
	}
	return agent.Response{
		Message: agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: r.text}}},
		Usage:   agent.Usage{InputTokens: 120, OutputTokens: 30},
	}, nil
}

func (*fakeModel) Stream(context.Context, agent.Request) (agent.Stream, error) {
	return nil, errors.New("not streamed")
}

type world struct {
	store   *store.Store
	raw     *pgx.Conn
	team    uuid.UUID
	user    uuid.UUID
	channel uuid.UUID
	agent   uuid.UUID
	model   *fakeModel
	checker *Checker
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dsn := storetest.Migrated(t, func(ctx context.Context, dsn string) error {
		s, err := store.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Migrate(ctx)
		return err
	})
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	w := &world{store: s, raw: raw, model: &fakeModel{}}
	w.scan(t, &w.team, `INSERT INTO teams (name) VALUES ('support') RETURNING id`)
	w.scan(t, &w.user, `INSERT INTO users (issuer, subject) VALUES ('dev', 'someone') RETURNING id`)
	w.exec(t, `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`, w.team, w.user)
	w.scan(t, &w.channel, `SELECT id FROM channels WHERE team_id = $1 LIMIT 1`, w.team)
	w.scan(t, &w.agent, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'support-bot', 'test/model', 1024, 'react', 5) RETURNING id`, w.team)
	w.exec(t, `INSERT INTO team_settings (team_id, model_name, model_max_tokens, improvement, improvement_since)
		VALUES ($1, 'team/model', 2000, true, now() - interval '1 day')`, w.team)

	judge := NewJudge(func(agent.Endpoint) (agent.ModelClient, error) { return w.model, nil }, agent.Endpoint{})
	w.checker = NewChecker(s, judge, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return w
}

func (w *world) scan(t *testing.T, dst any, sql string, args ...any) {
	t.Helper()
	if err := w.raw.QueryRow(t.Context(), sql, args...).Scan(dst); err != nil {
		t.Fatalf("%s: %v", strings.Fields(sql)[0:3], err)
	}
}

func (w *world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.raw.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (w *world) session(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	w.scan(t, &id, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, $3, 'group') RETURNING id`,
		w.team, w.channel, uuid.NewString())
	return id
}

func (w *world) say(t *testing.T, session uuid.UUID, text string) {
	t.Helper()
	w.exec(t, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, sender_name)
		VALUES ($1, $2, $3, $4, $5, 'Asha')`, w.channel, session, w.user, uuid.NewString(), text)
}

// finished is a top-level run of support-bot that ended ago, with its
// finished event in the session's stream.
func (w *world) finished(t *testing.T, session uuid.UUID, ago string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w.exec(t, `INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, input, output, status,
		started_at, finished_at)
		VALUES ($1, $2, $3, $1, $4, 'support-bot', 'refund order 42', 'Done, refunded.', 'succeeded',
		now() - $5::interval - interval '1 minute', now() - $5::interval)`, id, w.team, session, w.agent, ago)
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload, session_id) VALUES
		($1, 1, 'started', '{"tools":["orders__lookup"],"skills":["refunds"]}', $2),
		($1, 2, 'tool_call', '{"name":"orders__lookup","arguments":"{\"id\":42}"}', $2),
		($1, 3, 'tool_result', '{"name":"orders__lookup","output":"order 42: paid"}', $2),
		($1, 4, 'finished', '{"status":"succeeded"}', $2)`, id, session)
	return id
}

func (w *world) settled(t *testing.T) []uuid.UUID {
	t.Helper()
	rows, err := w.store.ListSettledRuns(t.Context(), db.ListSettledRunsParams{SettleSeconds: int32(settleAfter.Seconds()), PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func has(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

const (
	goodAnswer   = `{"asked":[{"part":"refund the order","delivered":"yes"}],"verdict":"done","missed":"","why":"it refunded","implicit":"none"}`
	partlyAnswer = `{"asked":[{"part":"refund","delivered":"yes"},{"part":"confirm","delivered":"no"}],"verdict":"partly","missed":"did not confirm the order id","why":"half","implicit":"correction"}`
)

// A run settles when a person writes again after it, or ten minutes pass;
// only top-level, finished, non-builder runs of teams with it on, since it
// went on, and only once.
func TestWhichRunsSettle(t *testing.T) {
	w := newWorld(t)
	s := w.session(t)

	old := w.finished(t, s, "11 minutes")
	fresh := w.finished(t, w.session(t), "1 minute")
	answered := w.finished(t, s, "1 minute")
	w.say(t, s, "no, the other order")

	builder := w.finished(t, w.session(t), "11 minutes")
	w.exec(t, `UPDATE runs SET builder = true WHERE id = $1`, builder)
	running := w.finished(t, w.session(t), "11 minutes")
	w.exec(t, `UPDATE runs SET status = 'running', finished_at = NULL, worker = 'w1', heartbeat_at = now() WHERE id = $1`, running)
	before := w.finished(t, w.session(t), "2 days")
	judged := w.finished(t, w.session(t), "11 minutes")
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest) VALUES ($1, $2, 'done', 'm', false, 'skipped')`, judged, w.team)
	child := uuid.New()
	w.exec(t, `INSERT INTO runs (id, team_id, session_id, root_run_id, parent_run_id, parent_tool_call, depth, agent_name, input, status, finished_at)
		VALUES ($1, $2, $3, $4, $4, 'call-1', 1, 'support-bot', 'x', 'succeeded', now() - interval '1 hour')`, child, w.team, s, old)

	got := w.settled(t)
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"ten minutes passed": {old, true}, "written to since": {answered, true}, "too fresh": {fresh, false},
		"a builder run": {builder, false}, "still running": {running, false}, "before it went on": {before, false},
		"already judged": {judged, false}, "a delegate": {child, false},
	} {
		if has(got, tc.id) != tc.want {
			t.Errorf("%s: settled = %v, want %v", name, has(got, tc.id), tc.want)
		}
	}

	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	if len(w.settled(t)) != 0 {
		t.Error("a team with improvement off had runs settled")
	}
}

// A check reads the run, the conversation around it and its trace, and
// stores the verdict; a bad one is queued for its digest.
func TestACheckStoresTheVerdict(t *testing.T) {
	w := newWorld(t)
	s := w.session(t)
	w.say(t, s, "please refund order 42")
	run := w.finished(t, s, "11 minutes")
	w.say(t, s, "you did not confirm the order id")
	w.model.replies = []reply{{text: partlyAnswer}}

	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	o, err := w.store.GetRunOutcome(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != "partly" || o.Implicit != "correction" || o.Missed != "did not confirm the order id" ||
		o.Digest != digestPending || o.Model != "team/model" || o.InputTokens != 120 {
		t.Errorf("outcome = %+v", o)
	}
	asked := w.model.asked[0]
	user := asked.Messages[0].Text()
	for _, want := range []string{"please refund order 42", "refund order 42", "orders__lookup", "Done, refunded.", "you did not confirm the order id"} {
		if !strings.Contains(user, want) {
			t.Errorf("the judge was not shown %q:\n%s", want, user)
		}
	}
	if asked.OutputSchema == nil || asked.OutputSchema.Name != "outcome" || asked.Model.Name != "team/model" {
		t.Errorf("asked = %+v", asked)
	}

	// Judged once: a second check of the same run writes nothing new.
	w.model.replies = []reply{{text: goodAnswer}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if o2, _ := w.store.GetRunOutcome(t.Context(), run); o2.Verdict != "partly" {
		t.Errorf("a second check replaced the verdict: %s", o2.Verdict)
	}
}

// The judge model wins over the team's when set.
func TestTheJudgeModelIsUsed(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `UPDATE team_settings SET judge_model_name = 'small/judge', judge_model_max_tokens = 500 WHERE team_id = $1`, w.team)
	run := w.finished(t, w.session(t), "11 minutes")
	w.model.replies = []reply{{text: goodAnswer}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if w.model.asked[0].Model.Name != "small/judge" {
		t.Errorf("judged on %s", w.model.asked[0].Model.Name)
	}
	if o, _ := w.store.GetRunOutcome(t.Context(), run); o.Digest != digestSkipped && !Sampled(run, 10) {
		t.Errorf("a good run outside the sample was queued: %s", o.Digest)
	}
}

// Nonsense, and a transport error after one retry, are stored as cant_tell
// with their reason, so the run is never judged again and again.
func TestAFailedCheckIsCantTell(t *testing.T) {
	for name, tc := range map[string]struct {
		replies []reply
		calls   int
	}{
		"not json":        {[]reply{{text: "I think it went fine"}}, 1},
		"unknown verdict": {[]reply{{text: `{"asked":[],"verdict":"great","missed":"","why":"","implicit":"none"}`}}, 1},
		"down twice":      {[]reply{{err: errors.New("503")}, {err: errors.New("503")}}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			run := w.finished(t, w.session(t), "11 minutes")
			w.model.replies = tc.replies
			if err := w.checker.Check(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			o, err := w.store.GetRunOutcome(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			if o.Verdict != verdictCantTell || o.Error == nil || len(w.model.asked) != tc.calls {
				t.Errorf("outcome = %+v after %d calls, want cant_tell with its reason after %d", o, len(w.model.asked), tc.calls)
			}
		})
	}

	t.Run("down once, then answered", func(t *testing.T) {
		w := newWorld(t)
		run := w.finished(t, w.session(t), "11 minutes")
		w.model.replies = []reply{{err: errors.New("503")}, {text: goodAnswer}}
		if err := w.checker.Check(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		if o, _ := w.store.GetRunOutcome(t.Context(), run); o.Verdict != verdictDone {
			t.Errorf("verdict = %s after a retry", o.Verdict)
		}
	})
}

func TestACheckWithNoModelSaysSo(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `UPDATE team_settings SET model_name = NULL, model_max_tokens = NULL WHERE team_id = $1`, w.team)
	run := w.finished(t, w.session(t), "11 minutes")
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	o, err := w.store.GetRunOutcome(t.Context(), run)
	if err != nil || o.Verdict != verdictCantTell || o.Error == nil || len(w.model.asked) != 0 {
		t.Errorf("outcome = %+v, err %v, calls %d", o, err, len(w.model.asked))
	}
}

// Turned off between the sweep and the check: nothing is called or stored.
func TestACheckAfterTurningOffDoesNothing(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "11 minutes")
	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	w.model.replies = []reply{{text: goodAnswer}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.GetRunOutcome(t.Context(), run); !errors.Is(err, pgx.ErrNoRows) || len(w.model.asked) != 0 {
		t.Errorf("a check ran for a team with improvement off: err %v, calls %d", err, len(w.model.asked))
	}
}

const digestAnswerText = `{"agents":[{"agent":"support-bot","summary":"Asked to refund an order; refunded without confirming which order.","went":"wrong",
 "signature":{"part":"instructions","target":"","cause":"skipped_step","behaviour":"refunded before confirming the order"}}]}`

// A pending digest writes one row per custom agent and is done; a second
// run of it writes nothing.
func TestADigestWritesOnce(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "11 minutes")
	w.model.replies = []reply{{text: partlyAnswer}, {text: digestAnswerText}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	pending, err := w.store.ListPendingDigests(t.Context(), 10)
	if err != nil || len(pending) != 1 || pending[0].RunID != run {
		t.Fatalf("pending = %+v, err %v", pending, err)
	}
	if err := w.checker.Digest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	rows, err := w.store.ListRunDigests(t.Context(), run)
	if err != nil || len(rows) != 1 || rows[0].AgentID != w.agent || rows[0].Cause != "skipped_step" || rows[0].Part != "instructions" {
		t.Fatalf("digests = %+v, err %v", rows, err)
	}
	if o, _ := w.store.GetRunOutcome(t.Context(), run); o.Digest != digestDone {
		t.Errorf("digest = %s, want done", o.Digest)
	}
	if !strings.Contains(w.model.asked[1].Messages[0].Text(), "support-bot") {
		t.Error("the digest was not told which agents to digest")
	}

	if err := w.checker.Digest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if rows, _ := w.store.ListRunDigests(t.Context(), run); len(rows) != 1 || len(w.model.asked) != 2 {
		t.Errorf("a second digest ran: %d rows, %d calls", len(rows), len(w.model.asked))
	}
}

// A digest the model fails is skipped, not retried for ever; a member's 👎
// reopens it, in a new round, and it runs again.
func TestAFailedDigestIsSkippedAndReopenedByADown(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "11 minutes")
	w.model.replies = []reply{{text: partlyAnswer}, {err: errors.New("503")}, {err: errors.New("503")}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := w.checker.Digest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	o, _ := w.store.GetRunOutcome(t.Context(), run)
	if o.Digest != digestSkipped {
		t.Fatalf("digest = %s after a failure, want skipped", o.Digest)
	}

	if err := w.store.ReopenRunDigest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	pending, _ := w.store.ListPendingDigests(t.Context(), 10)
	if len(pending) != 1 || pending[0].DigestRound != 1 {
		t.Fatalf("pending = %+v, want the run in round 1", pending)
	}
	w.model.replies = []reply{{text: digestAnswerText}}
	if err := w.checker.Digest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if rows, _ := w.store.ListRunDigests(t.Context(), run); len(rows) != 1 {
		t.Errorf("the reopened digest wrote %d rows", len(rows))
	}
}

// A digest reopened while one was in flight: the one in flight must not
// write, since the round it read is gone; the new round's digest will.
func TestADigestOfAnOldRoundWritesNothing(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "11 minutes")
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest, digest_round)
		VALUES ($1, $2, 'partly', 'm', false, 'pending', 0)`, run, w.team)

	w.model.replies = []reply{{text: digestAnswerText}}
	w.model.onAsk = func() {
		w.exec(t, `UPDATE run_outcomes SET digest_round = 1 WHERE run_id = $1`, run)
	}
	if err := w.checker.Digest(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if rows, _ := w.store.ListRunDigests(t.Context(), run); len(rows) != 0 {
		t.Errorf("a digest of round 0 wrote %d rows after round 1 began", len(rows))
	}
	if o, _ := w.store.GetRunOutcome(t.Context(), run); o.Digest != digestPending {
		t.Errorf("digest = %s, want still pending for round 1", o.Digest)
	}
}

// The sweep reads both queues: settled runs and pending digests.
func TestTheSweepFindsWork(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "11 minutes")
	if got := w.settled(t); !has(got, run) {
		t.Fatalf("settled = %v", got)
	}
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest) VALUES ($1, $2, 'wrong', 'm', false, 'pending')`, run, w.team)
	if got := w.settled(t); has(got, run) {
		t.Error("a judged run settled again")
	}
	if p, _ := w.store.ListPendingDigests(t.Context(), 10); len(p) != 1 {
		t.Errorf("pending = %+v", p)
	}
}
