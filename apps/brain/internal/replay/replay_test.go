package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// fakeRunner is the runtime's Replayer, answering with what a test sets and
// keeping what each try was given.
type fakeRunner struct {
	mu    sync.Mutex
	given []runtime.ReplayInput
	agent []db.Agent
	out   runtime.Replayed
	err   error
}

func (f *fakeRunner) Replay(ctx context.Context, in runtime.ReplayInput) (runtime.Replayed, error) {
	a, err := in.Store.GetAgent(ctx, *in.Run.AgentID)
	if err != nil {
		return runtime.Replayed{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.given, f.agent = append(f.given, in), append(f.agent, a)
	return f.out, f.err
}

func (f *fakeRunner) tries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.given)
}

type fakeJudge struct {
	verdict string
	err     error
	asked   []improve.Replay
}

func (f *fakeJudge) JudgeReplay(_ context.Context, _ uuid.UUID, r improve.Replay) (improve.ReplayJudgement, error) {
	f.asked = append(f.asked, r)
	return improve.ReplayJudgement{Verdict: f.verdict, Why: "it did", Usage: agent.Usage{InputTokens: 50, OutputTokens: 10}}, f.err
}

type world struct {
	store    *store.Store
	raw      *pgx.Conn
	sessions *postgres.Store
	team     uuid.UUID
	user     uuid.UUID
	channel  uuid.UUID
	agent    uuid.UUID
	runner   *fakeRunner
	judge    *fakeJudge
	objects  objectsByKey
	replays  *Replays
}

// objectsByKey is a store of objects by key, for skill files.
type objectsByKey map[string]string

func (o objectsByKey) Get(_ context.Context, _ uuid.UUID, key string) ([]byte, error) {
	v, ok := o[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return []byte(v), nil
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

	w := &world{
		store: s, raw: raw, sessions: postgres.New(s.Pool()),
		runner: &fakeRunner{out: runtime.Replayed{Output: "Refunded.", Steps: 2, Usage: agent.Usage{InputTokens: 900, OutputTokens: 100}, Elapsed: 3 * time.Second}},
		judge:  &fakeJudge{verdict: "done"},
	}
	w.scan(t, &w.team, `INSERT INTO teams (name) VALUES ('support') RETURNING id`)
	w.scan(t, &w.user, `INSERT INTO users (issuer, subject) VALUES ('dev', 'someone') RETURNING id`)
	w.exec(t, `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`, w.team, w.user)
	w.scan(t, &w.channel, `SELECT id FROM channels WHERE team_id = $1 LIMIT 1`, w.team)
	w.scan(t, &w.agent, `INSERT INTO agents (team_id, name, instructions, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'support-bot', 'Be brief.', 'test/model', 1024, 'react', 5) RETURNING id`, w.team)
	w.exec(t, `INSERT INTO team_settings (team_id, model_name, model_max_tokens, improvement, improvement_since,
		improvement_replay)
		VALUES ($1, 'team/model', 2000, true, now() - interval '30 days', true)`, w.team)
	w.objects = objectsByKey{"objects/kept": "kept"}
	w.replays = New(s, w.objects, w.sessions, w.runner, w.judge)
	w.replays.pause = 0
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

// past is a finished top-level run of support-bot, created ago, judged with
// verdict, in session, its transcript an earlier turn and its own first
// message, and one call to orders__lookup it made.
func (w *world) past(t *testing.T, session uuid.UUID, ago, verdict string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	key := "session:" + session.String()
	msgs, err := w.sessions.Messages(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	from := len(msgs)
	msgs = append(msgs,
		agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "refund order 42"}}, Pinned: true},
		agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "Done."}}},
	)
	if err := w.sessions.Save(t.Context(), key, msgs); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, input, output, status,
		transcript, transcript_from, created_at, started_at, finished_at)
		VALUES ($1, $2, $3, $1, $4, 'support-bot', 'refund order 42', 'Done.', 'succeeded', $5, $6,
		now() - $7::interval, now() - $7::interval, now() - $7::interval + interval '1 minute')`,
		id, w.team, session, w.agent, key, from, ago)
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload, session_id) VALUES
		($1, 1, 'started', '{}', $2),
		($1, 2, 'tool_call', '{"id":"c1","name":"orders__lookup","arguments":"{\"id\":42}"}', $2),
		($1, 3, 'tool_result', '{"id":"c1","name":"orders__lookup","output":"order 42: paid"}', $2),
		($1, 4, 'finished', '{"status":"succeeded"}', $2)`, id, session)
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest) VALUES ($1, $2, $3, 'm', false, 'skipped')`,
		id, w.team, verdict)
	return id
}

func (w *world) thumbsDown(t *testing.T, run uuid.UUID) {
	t.Helper()
	w.exec(t, `INSERT INTO run_feedback (run_id, team_id, user_id, value, via) VALUES ($1, $2, $3, 'down', 'dashboard')`, run, w.team, w.user)
}

// proposal is a waiting instructions change of support-bot, citing runs.
func (w *world) proposal(t *testing.T, runs ...uuid.UUID) uuid.UUID {
	t.Helper()
	after := proposals.Snapshot{Agent: &proposals.AgentState{
		Agent: harness.Request{
			Name: "support-bot", Kind: harness.KindCustom, Instructions: "Look the order up once.",
			Model: harness.ModelRef{Name: "test/model", MaxTokens: 1024}, Pattern: "react", MaxSteps: 5,
		},
		Notes: []proposals.ToolNote{},
	}}
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	w.scan(t, &id, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, run_ids, before, after)
		VALUES ($1, $2, 'instructions', 'w', 'h', 'domain', $3, '{}', $4) RETURNING id`, w.team, w.agent, runs, raw)
	return id
}

type tried struct {
	run      uuid.UUID
	chosen   string
	status   string
	skipWhy  *string
	original string
	replayed *string
}

func (w *world) tried(t *testing.T, proposal uuid.UUID) []tried {
	t.Helper()
	rows, err := w.raw.Query(t.Context(), `SELECT source_run_id, chosen, status, skip_why, original, replayed
		FROM improvement_replays WHERE proposal_id = $1 ORDER BY position`, proposal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []tried
	for rows.Next() {
		var r tried
		if err := rows.Scan(&r.run, &r.chosen, &r.status, &r.skipWhy, &r.original, &r.replayed); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func runsOf(ts []tried) []uuid.UUID {
	out := make([]uuid.UUID, len(ts))
	for i, r := range ts {
		out[i] = r.run
	}
	return out
}

// Half the runs a proposal cites, newest first, and half the agent's recent
// good runs it does not cite, one a conversation; a cited run that cannot be
// tried is noted with why; a run with a 👎, or past fourteen days, is no
// good run.
func TestTheRunsAProposalIsTriedOn(t *testing.T) {
	w := newWorld(t)
	oldBad := w.past(t, w.session(t), "3 hours", "not_done")
	newBad := w.past(t, w.session(t), "1 hour", "not_done")
	clipped := w.past(t, w.session(t), "2 hours", "wrong")
	w.exec(t, `UPDATE run_events SET payload = payload || '{"truncated":true}' WHERE run_id = $1 AND kind = 'tool_result'`, clipped)
	unsure := w.past(t, w.session(t), "2 hours", "cant_tell")

	shared := w.session(t)
	sharedOld := w.past(t, shared, "5 hours", "done")
	sharedNew := w.past(t, shared, "4 hours", "done")
	other := w.past(t, w.session(t), "6 hours", "done")
	disliked := w.past(t, w.session(t), "1 hour", "done")
	w.thumbsDown(t, disliked)
	w.past(t, w.session(t), "20 days", "done")

	p := w.proposal(t, oldBad, newBad, clipped, unsure)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	got := w.tried(t, p)
	if want := []uuid.UUID{newBad, oldBad, sharedNew, other, clipped}; !slices.Equal(runsOf(got), want) {
		t.Fatalf("tried %v, want %v", runsOf(got), want)
	}
	for i, chosen := range []string{chosenCited, chosenCited, chosenGood, chosenGood, chosenCited} {
		if got[i].chosen != chosen {
			t.Errorf("run %d chosen %s, want %s", i, got[i].chosen, chosen)
		}
	}
	if got[0].original != outcomeBad || got[2].original != outcomeGood || got[0].status != statusQueued {
		t.Errorf("tried %+v", got)
	}
	if got[4].status != "skipped" || got[4].skipWhy == nil || !strings.Contains(*got[4].skipWhy, "too long to keep whole") {
		t.Errorf("the clipped run = %+v, want skipped saying why", got[4])
	}
	if slices.Contains(runsOf(got), sharedOld) {
		t.Error("two runs of one conversation were tried")
	}
}

// Either half fills what the other lacks.
func TestAShortHalfIsFilledByTheOther(t *testing.T) {
	w := newWorld(t)
	bad := w.past(t, w.session(t), "1 hour", "not_done")
	for range 4 {
		w.past(t, w.session(t), "2 hours", "done")
	}
	p := w.proposal(t, bad)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	if got := w.tried(t, p); len(got) != 4 || got[0].run != bad {
		t.Errorf("tried %v, want the one cited run and three good ones", runsOf(got))
	}
}

// Nothing is chosen with replay off, or for a proposal already decided.
func TestNothingIsChosenWithReplayOffOrDecided(t *testing.T) {
	w := newWorld(t)
	bad := w.past(t, w.session(t), "1 hour", "not_done")
	w.exec(t, `UPDATE team_settings SET improvement_replay = false WHERE team_id = $1`, w.team)
	p := w.proposal(t, bad)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil || len(w.tried(t, p)) != 0 {
		t.Errorf("with replay off: %v, %v", err, w.tried(t, p))
	}
	if w.chosen(t, p) {
		t.Error("a proposal chosen for with replay off reads as one none could be tried")
	}
	w.exec(t, `UPDATE team_settings SET improvement_replay = true WHERE team_id = $1`, w.team)
	w.exec(t, `UPDATE improvement_proposals SET status = 'rejected', decided_at = now() WHERE id = $1`, p)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil || len(w.tried(t, p)) != 0 {
		t.Errorf("decided: %v, %v", err, w.tried(t, p))
	}
	if !w.chosen(t, p) {
		t.Error("a proposal decided before its runs were chosen is not told from one proposed with replay off")
	}
}

func (w *world) chosen(t *testing.T, proposal uuid.UUID) bool {
	t.Helper()
	var at *time.Time
	w.scan(t, &at, `SELECT replay_chosen_at FROM improvement_proposals WHERE id = $1`, proposal)
	return at != nil
}

// A proposal none of whose runs fit is marked chosen, with no tries: told
// apart from one proposed with replay off.
func TestAProposalWithNoRunToTryIsMarked(t *testing.T) {
	w := newWorld(t)
	// Its one run's outcome could not be told, so there is nothing to
	// compare a try with, and the agent has no good run.
	p := w.proposal(t, w.past(t, w.session(t), "1 hour", "cant_tell"))
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil || len(w.tried(t, p)) != 0 {
		t.Fatalf("choose: %v, %v", err, w.tried(t, p))
	}
	if !w.chosen(t, p) {
		t.Error("not marked chosen")
	}
}

// A try is given the run's transcript to its first message, its recorded
// calls, the team's limits, and the proposal's harness; the stored agent is
// untouched. What came of it is written with the judge's verdict.
func TestATryRunsTheProposalAndKeepsWhatCameOfIt(t *testing.T) {
	w := newWorld(t)
	s := w.session(t)
	w.past(t, s, "3 hours", "done") // an earlier turn of the conversation
	bad := w.past(t, s, "1 hour", "not_done")
	w.exec(t, `UPDATE team_settings SET improvement_replay_tokens = 50000, improvement_replay_minutes = 7 WHERE team_id = $1`, w.team)
	w.runner.out.Calls = []runtime.Served{
		{Name: "orders__lookup", How: runtime.ServedRecorded, Arguments: `{"id":42}`, Output: "order 42: paid"},
		{Name: "orders__refund", How: runtime.ServedNotMade, Arguments: `{"id":42}`},
	}
	p := w.proposal(t, bad)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	if err := w.replays.Try(t.Context(), p, w.team, bad); err != nil {
		t.Fatal(err)
	}

	in := w.runner.given[0]
	if len(in.Messages) != 3 || in.Messages[2].Content[0].Text != "refund order 42" || !in.Messages[2].Pinned {
		t.Errorf("began from %d messages, want the earlier turn and its own first message", len(in.Messages))
	}
	if len(in.Recorded) != 1 || in.Recorded[0] != (runtime.Recorded{Name: "orders__lookup", Arguments: `{"id":42}`, Output: "order 42: paid"}) {
		t.Errorf("recorded %+v", in.Recorded)
	}
	if in.Limits != (runtime.ReplayLimits{Tokens: 50000, Time: 7 * time.Minute}) {
		t.Errorf("limits %+v", in.Limits)
	}
	if w.runner.agent[0].Instructions != "Look the order up once." {
		t.Errorf("tried with %q, want the proposal's instructions", w.runner.agent[0].Instructions)
	}
	var stored string
	w.scan(t, &stored, `SELECT instructions FROM agents WHERE id = $1`, w.agent)
	if stored != "Be brief." {
		t.Errorf("the stored agent's instructions are %q", stored)
	}
	if lines := w.judge.asked[0].Trace; !slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, "orders__refund") && strings.Contains(l, "not made in the replay")
	}) {
		t.Errorf("the judge read %q, want the call not made said so", lines)
	}

	var row struct {
		status, replayed, verdict string
		diverged                  bool
		recorded, notMade         int16
		spent, workMS             int64
		trace                     []byte
	}
	w.scan(t, &row.status, `SELECT status FROM improvement_replays WHERE proposal_id = $1 AND source_run_id = $2`, p, bad)
	if err := w.raw.QueryRow(t.Context(), `SELECT replayed, verdict, diverged, recorded, not_made,
		run_spent(input_tokens, cached_input_tokens, output_tokens), work_ms, trace
		FROM improvement_replays WHERE proposal_id = $1 AND source_run_id = $2`, p, bad).
		Scan(&row.replayed, &row.verdict, &row.diverged, &row.recorded, &row.notMade, &row.spent, &row.workMS, &row.trace); err != nil {
		t.Fatal(err)
	}
	if row.status != "done" || row.replayed != outcomeGood || row.verdict != "done" || !row.diverged ||
		row.recorded != 1 || row.notMade != 1 || row.spent != 1000 || row.workMS != 3000 {
		t.Errorf("kept %+v", row)
	}
	var calls []runtime.Served
	if err := json.Unmarshal(row.trace, &calls); err != nil || len(calls) != 2 {
		t.Errorf("trace %s: %v", row.trace, err)
	}
}

// A try a model could not finish is failed with why; one stopped at its
// caps is could not tell, with no judge asked.
func TestAFailedOrStoppedTry(t *testing.T) {
	w := newWorld(t)
	a := w.past(t, w.session(t), "1 hour", "not_done")
	b := w.past(t, w.session(t), "2 hours", "not_done")
	p := w.proposal(t, a, b)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	w.runner.err = errors.New("the model endpoint could not be reached")
	if err := w.replays.Try(t.Context(), p, w.team, a); err != nil {
		t.Fatal(err)
	}
	w.runner.err, w.runner.out.Over = nil, runtime.ErrReplayTokens
	if err := w.replays.Try(t.Context(), p, w.team, b); err != nil {
		t.Fatal(err)
	}
	got := w.tried(t, p)
	if got[0].status != "failed" || got[1].status != "done" || *got[1].replayed != outcomeCantTell {
		t.Errorf("tried %+v %+v", got[0], got[1])
	}
	if len(w.judge.asked) != 0 {
		t.Error("the judge was asked about a try stopped short")
	}
}

// A proposal decided, replay turned off, or the team's tries for the day
// used: its tries still to run are skipped, saying why, and none is run.
func TestTriesStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string
		why  string
	}{
		{"decided", `UPDATE improvement_proposals SET status = 'rejected', decided_at = now() WHERE team_id = $1`, whyDecided},
		{"replay off", `UPDATE team_settings SET improvement_replay = false WHERE team_id = $1`, whyOff},
		{"improvement off", `UPDATE team_settings SET improvement = false WHERE team_id = $1`, whyOff},
		{"daily cap", `UPDATE team_settings SET improvement_replay_daily = 1 WHERE team_id = $1`, whyDailyCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			a := w.past(t, w.session(t), "1 hour", "not_done")
			b := w.past(t, w.session(t), "2 hours", "not_done")
			p := w.proposal(t, a, b)
			if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
				t.Fatal(err)
			}
			w.exec(t, tc.set, w.team)
			first := w.replays.Try(t.Context(), p, w.team, a)
			if tc.why == whyDailyCap {
				// The day's one try is this one; the next is past the cap.
				if first != nil {
					t.Fatalf("the day's first try: %v", first)
				}
				first = w.replays.Try(t.Context(), p, w.team, b)
			}
			if !Stopped(first) {
				t.Fatalf("try = %v, want the tries stopped", first)
			}
			for i, r := range w.tried(t, p) {
				if tc.why == whyDailyCap && i == 0 {
					if r.status != "done" {
						t.Errorf("the day's try %+v, want done", r)
					}
					continue
				}
				if r.status != "skipped" || r.skipWhy == nil || *r.skipWhy != tc.why {
					t.Errorf("tried %d %+v, want skipped: %s", i, r, tc.why)
				}
			}
			if queued, _ := w.replays.Queued(t.Context(), p); len(queued) != 0 {
				t.Errorf("still queued: %v", queued)
			}
			want := 0
			if tc.why == whyDailyCap {
				want = 1
			}
			if n := w.runner.tries(); n != want {
				t.Errorf("ran %d tries, want %d", n, want)
			}
		})
	}
}

// An applied proposal's tries go on: they help whoever approved it read the
// watch.
func TestAnAppliedProposalIsStillTried(t *testing.T) {
	w := newWorld(t)
	a := w.past(t, w.session(t), "1 hour", "not_done")
	p := w.proposal(t, a)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE improvement_proposals SET status = 'applied', decided_at = now() WHERE id = $1`, p)
	if err := w.replays.Try(t.Context(), p, w.team, a); err != nil || w.runner.tries() != 1 {
		t.Errorf("try = %v, %d tries", err, w.runner.tries())
	}
}

// A run's history in a try is what was said before it began.
func TestATryReadsOnlyThePast(t *testing.T) {
	w := newWorld(t)
	c := &candidate{BuildStore: w.store, before: 5}
	s := w.session(t)
	for i := range 8 {
		w.exec(t, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, sender_name)
			VALUES ($1, $2, $3, $4, $5, 'Asha')`, w.channel, s, w.user, uuid.NewString(), "said "+string(rune('a'+i)))
	}
	rows, err := c.ListSessionHistory(t.Context(), db.ListSessionHistoryParams{SessionID: s, Before: 1 << 62, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Seq >= 5 {
			t.Errorf("read message %d, said after the run began", r.Seq)
		}
	}
}

// Two workers starting a team's tries at once cannot both take the last of
// the day: the count and the claim hold the team's lock.
func TestTheDailyCapHoldsAcrossWorkers(t *testing.T) {
	w := newWorld(t)
	var runs []uuid.UUID
	for i := range 6 {
		runs = append(runs, w.past(t, w.session(t), fmt.Sprintf("%d hours", i+1), "not_done"))
	}
	var rows []db.StartReplayParams
	for _, run := range runs {
		p := w.proposal(t, run)
		if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, db.StartReplayParams{ProposalID: p, SourceRunID: run, TeamID: w.team, Since: midnight(time.Now()), Daily: 1})
	}
	var wg sync.WaitGroup
	started := make([]int64, len(rows))
	errs := make([]error, len(rows))
	for i, row := range rows {
		wg.Go(func() { started[i], errs[i] = w.store.ClaimReplay(t.Context(), row) })
	}
	wg.Wait()
	total := int64(0)
	for i := range rows {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		total += started[i]
	}
	if total != 1 {
		t.Errorf("%d tries began, want the day's one", total)
	}
}

// failingFinish fails its first FinishReplay.
type failingFinish struct {
	Store
	mu     sync.Mutex
	failed bool
}

func (f *failingFinish) FinishReplay(ctx context.Context, arg db.FinishReplayParams) error {
	f.mu.Lock()
	first := !f.failed
	f.failed = true
	f.mu.Unlock()
	if first {
		return errors.New("the store blinked")
	}
	return f.Store.FinishReplay(ctx, arg)
}

// A write that fails once is made again, and the model is not run again for
// it.
func TestAFailedWriteDoesNotRunTheModelAgain(t *testing.T) {
	w := newWorld(t)
	run := w.past(t, w.session(t), "1 hour", "not_done")
	p := w.proposal(t, run)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	w.replays.store = &failingFinish{Store: w.store}
	if err := w.replays.Try(t.Context(), p, w.team, run); err != nil {
		t.Fatalf("try: %v", err)
	}
	if n := w.runner.tries(); n != 1 {
		t.Errorf("the model ran %d times, want once", n)
	}
	if got := w.tried(t, p); len(got) != 1 || got[0].status != "done" {
		t.Errorf("tried %+v, want done", got)
	}
}

// A try whose error came back is failed, whatever it was at, so it is not
// left trying.
func TestAFailedTryIsNotLeftTrying(t *testing.T) {
	w := newWorld(t)
	run := w.past(t, w.session(t), "1 hour", "not_done")
	p := w.proposal(t, run)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE improvement_replays SET status = 'trying', started_at = now() WHERE proposal_id = $1`, p)
	if err := w.replays.Fail(t.Context(), p, run, errors.New("lost the store")); err != nil {
		t.Fatal(err)
	}
	if got := w.tried(t, p); len(got) != 1 || got[0].status != "failed" {
		t.Errorf("tried %+v, want failed", got)
	}
}

// A run's calls are read past one page of its events.
func TestEveryPageOfARunsEventsIsRead(t *testing.T) {
	w := newWorld(t)
	run := w.past(t, w.session(t), "1 hour", "done")
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload)
		SELECT $1, 1000 + g, 'step', '{"step":1}' FROM generate_series(1, $2::int) g`, run, eventsPage)
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload) VALUES
		($1, 5000, 'tool_call', '{"id":"late","name":"crm__lookup","arguments":"{}"}'),
		($1, 5001, 'tool_result', '{"id":"late","name":"crm__lookup","output":"found"}')`, run)
	got, err := w.replays.recorded(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[len(got)-1].Output != "found" {
		t.Errorf("recorded %+v; want the call past the first page", got)
	}
}

func TestClipKeepsWholeRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc"},
		{"aé", 2, "a"},            // é is two bytes: not cut inside
		{"a\xffbcd", 3, "a\xffb"}, // a bad byte mid-text keeps what follows
	} {
		if got := clip(tc.in, tc.n); got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestATraceKeepsHowALongTryBegan(t *testing.T) {
	calls := make([]runtime.Served, 400)
	for i := range calls {
		calls[i] = runtime.Served{Name: fmt.Sprintf("c%d", i), How: runtime.ServedRecorded, Output: strings.Repeat("x", 1000)}
	}
	b := traceOf(calls)
	var kept []runtime.Served
	if err := json.Unmarshal(b, &kept); err != nil || len(b) > traceBytes || len(kept) == 0 || kept[0].Name != "c0" {
		t.Errorf("kept %d calls in %d bytes (%v)", len(kept), len(b), err)
	}
	if string(traceOf(nil)) != "null" && string(traceOf(nil)) != "[]" {
		t.Errorf("no calls = %s", traceOf(nil))
	}
}

func TestAJudgeReadsHowEachCallWasServed(t *testing.T) {
	lines := judgeTrace(runtime.Replayed{Tools: []string{"crm__lookup", "mail__send"}, Calls: []runtime.Served{
		{Name: "crm__lookup", How: runtime.ServedRecorded, Arguments: "{}", Output: "Ada"},
		{Name: "mail__send", How: runtime.ServedNotMade, Arguments: "{}"},
	}})
	want := []string{
		"the agent started with tools [crm__lookup, mail__send]",
		"  the agent calls crm__lookup({}) [as in the past run]",
		"  crm__lookup returned: Ada",
		"  the agent calls mail__send({}) [not made in the replay]",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("trace %q, want %q", lines, want)
	}
}

func TestAReplayInputIsProposalAndTeam(t *testing.T) {
	p, team := uuid.New(), uuid.New()
	if gp, gt, err := parseInput(input(p, team)); err != nil || gp != p || gt != team {
		t.Errorf("parse = %v, %v, %v", gp, gt, err)
	}
	for _, bad := range []string{"", "nope", uuid.NewString(), "x:" + uuid.NewString()} {
		if _, _, err := parseInput(bad); err == nil {
			t.Errorf("parseInput(%q) took it", bad)
		}
	}
}
