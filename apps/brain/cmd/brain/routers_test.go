package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/runs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type runWorld struct {
	store       runStore
	raw         *pgx.Conn
	team, user  uuid.UUID
	agent       uuid.UUID
	otherAgent  uuid.UUID
	teamSession uuid.UUID
}

func newRunWorld(t *testing.T) *runWorld {
	t.Helper()

	dsn := schemaDSN(t)
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	w := &runWorld{store: runStore{s}, raw: raw}
	var other uuid.UUID
	for _, q := range []struct {
		sql  string
		args []any
		into *uuid.UUID
	}{
		{`INSERT INTO teams (name) VALUES ('platform') RETURNING id`, nil, &w.team},
		{`INSERT INTO teams (name) VALUES ('elsewhere') RETURNING id`, nil, &other},
		{`INSERT INTO users (issuer, subject) VALUES ('https://id.test', 'ada') RETURNING id`, nil, &w.user},
	} {
		if err := raw.QueryRow(t.Context(), q.sql, q.args...).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	for _, q := range []struct {
		team uuid.UUID
		into *uuid.UUID
	}{{w.team, &w.agent}, {other, &w.otherAgent}} {
		if err := raw.QueryRow(t.Context(), `
			INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
			VALUES ($1, 'triage', 'test/model', 1024, 'react', 5) RETURNING id`, q.team).Scan(q.into); err != nil {
			t.Fatalf("insert agent: %v", err)
		}
	}
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, w.team).Scan(&w.teamSession); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return w
}

func (w *runWorld) count(t *testing.T, table string) int {
	t.Helper()

	var n int
	if err := w.raw.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestARunWithNoSessionIsGivenADirectSessionOfItsCallers(t *testing.T) {
	w := newRunWorld(t)

	r, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, Input: "hello",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var kind string
	var owner *uuid.UUID
	var team uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`SELECT kind, user_id, team_id FROM sessions WHERE id = $1`, r.SessionID).Scan(&kind, &owner, &team); err != nil {
		t.Fatalf("read the run's session: %v", err)
	}
	if kind != "direct" || owner == nil || *owner != w.user || team != w.team {
		t.Errorf("session is %s of %v in %s, want direct, of the caller, in the run's team", kind, owner, team)
	}
	if r.UserID == nil || *r.UserID != w.user || r.Status != "queued" || r.Input != "hello" {
		t.Errorf("run = %+v", r)
	}
}

func TestARunInANamedSessionMakesNoOtherSession(t *testing.T) {
	w := newRunWorld(t)
	session := w.teamSession

	r, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, SessionID: &session, Input: "hello",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if r.SessionID != w.teamSession {
		t.Errorf("session = %s, want the one named", r.SessionID)
	}
	if n := w.count(t, "sessions"); n != 1 {
		t.Errorf("%d sessions, want only the one named", n)
	}
}

// CreateRun inserts nothing for an agent outside the team, which is how an
// agent deleted or moved mid-request looks. The direct session made for it a
// statement earlier must go too, or the caller has an empty conversation they
// never started.
func TestARefusedRunLeavesNoSessionBehind(t *testing.T) {
	w := newRunWorld(t)
	before := w.count(t, "sessions")

	_, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.otherAgent, UserID: w.user, Input: "hello",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("StartRun = %v, want pgx.ErrNoRows for the handler's 404", err)
	}
	if after := w.count(t, "sessions"); after != before {
		t.Errorf("sessions went from %d to %d for a run that was refused", before, after)
	}
	if n := w.count(t, "runs"); n != 0 {
		t.Errorf("%d runs written", n)
	}
}

// A hub start reads the team's hub as it inserts, in the direct session made
// for it, and ignores any agent id it was handed.
func TestAHubStartIsTheTeamsHubInADirectSession(t *testing.T) {
	w := newRunWorld(t)

	r, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.otherAgent, UserID: w.user, Input: "hello", Hub: true,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !r.Hub || r.AgentID != nil || r.AgentName != "orchestrator" || r.TeamID != w.team {
		t.Errorf("run is hub %v, agent %v %q in %s; want the team's orchestrator", r.Hub, r.AgentID, r.AgentName, r.TeamID)
	}
	var kind string
	if err := w.raw.QueryRow(t.Context(), `SELECT kind FROM sessions WHERE id = $1`, r.SessionID).Scan(&kind); err != nil {
		t.Fatalf("read the run's session: %v", err)
	}
	if kind != "direct" {
		t.Errorf("session is %s, want direct", kind)
	}

	if _, err := w.raw.Exec(t.Context(),
		`INSERT INTO team_settings (team_id, search_provider, compaction_messages, hub_agent_id) VALUES ($1, 'none', 100, $2)`,
		w.team, w.agent); err != nil {
		t.Fatalf("choose the hub: %v", err)
	}
	r, err = w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, UserID: w.user, Input: "hello", Hub: true,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !r.Hub || r.AgentID == nil || *r.AgentID != w.agent || r.AgentName != "triage" {
		t.Errorf("run is hub %v, agent %v %q; want triage", r.Hub, r.AgentID, r.AgentName)
	}
}

// A delete is refused while a run is queued or running, so a deleted team has
// nothing for a worker to claim. That holds only if a run cannot start in a
// team in the middle of being deleted.
func TestAStartedRunNeverLandsInADeletedTeam(t *testing.T) {
	w := newRunWorld(t)

	var started, refused int
	for i := range 200 {
		team, agent := w.freshTeam(t, i)

		var (
			wg       sync.WaitGroup
			startErr error
			deleted  int64
			delErr   error
		)
		gate := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-gate
			_, startErr = w.store.StartRun(t.Context(), runs.StartArgs{
				ID: uuid.New(), TeamID: team, AgentID: agent, UserID: w.user, Input: "hello",
			})
		}()
		go func() {
			defer wg.Done()
			<-gate
			deleted, delErr = w.store.SoftDeleteTeam(t.Context(), team)
		}()
		close(gate)
		wg.Wait()

		if delErr != nil {
			t.Fatalf("round %d: SoftDeleteTeam: %v", i, delErr)
		}
		switch {
		case startErr == nil && deleted == 0:
			started++
		case errors.Is(startErr, runs.ErrTeamDeleted) && deleted == 1:
			refused++
		default:
			t.Errorf("round %d: StartRun = %v and the delete updated %d rows", i, startErr, deleted)
		}

		var live int
		if err := w.raw.QueryRow(t.Context(), `
			SELECT count(*) FROM runs r JOIN teams tm ON tm.id = r.team_id
			WHERE tm.deleted_at IS NOT NULL AND r.status IN ('queued', 'running')`).Scan(&live); err != nil {
			t.Fatalf("round %d: count: %v", i, err)
		}
		if live != 0 {
			t.Fatalf("round %d: a deleted team holds %d live runs", i, live)
		}
	}
	t.Logf("%d runs started first, %d deletes won", started, refused)
}

// freshTeam is a live team with an agent in it, one per round, so no round
// inherits the last one's runs.
func (w *runWorld) freshTeam(t *testing.T, round int) (team, agent uuid.UUID) {
	t.Helper()

	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO teams (name) VALUES ('race-' || $1::int) RETURNING id`, round).Scan(&team); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	if err := w.raw.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'triage', 'test/model', 1024, 'react', 5) RETURNING id`, team).Scan(&agent); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return team, agent
}

// After the delete has committed, a start is refused before it writes
// anything: no direct session is left behind for a team being purged.
func TestARunInADeletedTeamIsRefusedAndWritesNothing(t *testing.T) {
	w := newRunWorld(t)
	if n, err := w.store.SoftDeleteTeam(t.Context(), w.team); err != nil || n != 1 {
		t.Fatalf("SoftDeleteTeam = %d, %v", n, err)
	}
	sessions := w.count(t, "sessions")

	for name, arg := range map[string]runs.StartArgs{
		"an agent's run": {ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, Input: "hello"},
		"a hub run":      {ID: uuid.New(), TeamID: w.team, UserID: w.user, Input: "hello", Hub: true},
	} {
		if _, err := w.store.StartRun(t.Context(), arg); !errors.Is(err, runs.ErrTeamDeleted) {
			t.Errorf("%s: StartRun = %v, want runs.ErrTeamDeleted", name, err)
		}
	}
	if n := w.count(t, "sessions"); n != sessions {
		t.Errorf("sessions went from %d to %d", sessions, n)
	}
	if n := w.count(t, "runs"); n != 0 {
		t.Errorf("%d runs written", n)
	}
}

// The gateway's inbox against Postgres: a delivery that reaches a team
// deleted after its channel was read writes no message, and leaves a
// tombstone the reaper can claim at once for every object fetched for it.
func TestADeliveryToADeletedTeamLeavesClaimableTombstones(t *testing.T) {
	w := newRunWorld(t)
	ch, err := w.store.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: w.team, Provider: "custom", Name: "inbox",
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if n, err := w.store.SoftDeleteTeam(t.Context(), w.team); err != nil || n != 1 {
		t.Fatalf("SoftDeleteTeam = %d, %v", n, err)
	}
	messages := w.count(t, "messages")

	keys := []string{"teams/" + w.team.String() + "/a", "teams/" + w.team.String() + "/b"}
	d := gateway.Delivery{
		UserID: w.user,
		Inbound: gateway.Inbound{
			ExternalID: "m-1", Author: gateway.Author{Ref: "u-1"},
			Session: gateway.Session{Ref: "c-1", Kind: gateway.SessionGroup}, Text: "hi",
		},
	}
	for _, k := range keys {
		d.Files = append(d.Files, gateway.Stored{ObjectKey: k, MediaType: "image/png", SizeBytes: 1, Filename: "f.png"})
	}

	err = gateway.NewInbox(inbox{w.store.Store}, nil, discardLogger()).Deliver(t.Context(),
		gateway.Channel{ID: ch.ID, TeamID: w.team}, []gateway.Delivery{d})
	if !errors.Is(err, gateway.ErrTeamDeleted) {
		t.Fatalf("Deliver = %v, want gateway.ErrTeamDeleted", err)
	}

	rows, err := w.raw.Query(t.Context(), `
		SELECT object_key FROM deleted_objects
		WHERE team_id = $1 AND claimable_after <= now() ORDER BY object_key`, w.team)
	if err != nil {
		t.Fatalf("read tombstones: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !slices.Equal(got, keys) {
		t.Errorf("claimable tombstones = %v, want %v", got, keys)
	}
	if n := w.count(t, "messages"); n != messages {
		t.Errorf("messages went from %d to %d", messages, n)
	}
	if n := w.count(t, "attachments"); n != 0 {
		t.Errorf("%d attachments written", n)
	}
}

// A session has one transcript, so one run works in it: a run started while
// another is working there goes to that run as a steer, under its transcript,
// and no second run is made.
func TestARunStartedInABusySessionSteersTheOneWorkingThere(t *testing.T) {
	w := newRunWorld(t)
	session := w.teamSession

	first, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, SessionID: &session, Input: "audit the exports",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	asked := uuid.New()
	got, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: asked, TeamID: w.team, Hub: true, UserID: w.user, SessionID: &session, Input: "and the imports",
	})
	if err != nil {
		t.Fatalf("StartRun into the busy session: %v", err)
	}

	if got.ID != first.ID || got.ID == asked {
		t.Fatalf("StartRun returned %s; want the run already working, %s", got.ID, first.ID)
	}
	if n := w.count(t, "runs"); n != 1 {
		t.Errorf("%d runs, want the one: a steer makes none", n)
	}
	var key, text string
	if err := w.raw.QueryRow(t.Context(), `SELECT session, text FROM agent_steers`).Scan(&key, &text); err != nil {
		t.Fatalf("reading the steer: %v", err)
	}
	if key != "session:"+session.String() || text != "and the imports" {
		t.Errorf("steer = %q under %q; want the input under the session's transcript", text, key)
	}

	// Once it has finished, the session takes a new run.
	if _, err := w.raw.Exec(t.Context(), `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	next, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: asked, TeamID: w.team, Hub: true, UserID: w.user, SessionID: &session, Input: "now the reports",
	})
	if err != nil || next.ID != asked {
		t.Errorf("after the first finished, StartRun = %s, %v; want a new run %s", next.ID, err, asked)
	}
}

// A channel's session answers its chat; a run from the API would take the
// chat's messages as steers and answer them where the chat never sees.
func TestARunCannotStartInAChannelsSession(t *testing.T) {
	w := newRunWorld(t)
	ch, err := w.store.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: w.team, Provider: "custom", Name: "support"})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	chat, err := w.store.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: w.team, ChannelID: ch.ID, ProviderRef: "D01", Kind: "direct",
	})
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	_, err = w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, SessionID: &chat.ID, Input: "hello",
	})
	if !errors.Is(err, runs.ErrChannelSession) {
		t.Fatalf("StartRun in a channel's session = %v, want ErrChannelSession", err)
	}
	if n := w.count(t, "runs"); n != 0 {
		t.Errorf("%d runs after the refusal, want none", n)
	}
}
