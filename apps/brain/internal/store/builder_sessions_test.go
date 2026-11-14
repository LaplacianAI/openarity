package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A message in a builder conversation starts the builder, on the dashboard
// channel, with nothing to post its answer to; in any other conversation it
// starts the hub. Who answers is the session's, read as the run is made.
func TestAMessageStartsTheAgentItsConversationIsWith(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform "+uuid.NewString()[:8])
	ch, err := s.DashboardChannel(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("DashboardChannel: %v", err)
	}
	user := mustUser(t, s)

	for _, builder := range []bool{true, false} {
		session, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
			TeamID: team.ID, ChannelID: ch.ID, ProviderRef: uuid.NewString(), Kind: "direct", UserID: &user, Builder: builder,
		})
		if err != nil {
			t.Fatalf("EnsureSession: %v", err)
		}
		msg, err := s.InsertMessage(t.Context(), db.InsertMessageParams{
			ChannelID: ch.ID, SessionID: session.ID, UserID: user, ExternalID: uuid.NewString(), Text: "an agent for support",
		})
		if err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		run, err := s.CreateChannelRun(t.Context(), db.CreateChannelRunParams{
			ID: uuid.New(), TeamID: team.ID, SessionID: session.ID, UserID: &user, Input: "an agent for support", MessageID: &msg,
		})
		if err != nil {
			t.Fatalf("CreateChannelRun: %v", err)
		}

		switch {
		case session.Builder != builder:
			t.Errorf("the session is builder %v, want %v", session.Builder, builder)
		case builder && (!run.Builder || run.Hub || run.AgentName != "builder" || run.AgentID != nil):
			t.Errorf("a builder conversation started %+v, want the builder", run)
		case !builder && (run.Builder || !run.Hub):
			t.Errorf("a hub conversation started builder %v, hub %v; want the hub", run.Builder, run.Hub)
		case run.ReplyToChannel:
			t.Error("a dashboard run replies to its channel; its stream is the answer")
		case run.MessageID == nil || *run.MessageID != msg:
			t.Errorf("the run answers %v, want the message", run.MessageID)
		}
	}
}

// Who answers is decided when a conversation starts: ensuring it again, as
// every later message does, does not change it.
func TestALaterMessageDoesNotChangeWhoAnswers(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform "+uuid.NewString()[:8])
	ch, err := s.DashboardChannel(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("DashboardChannel: %v", err)
	}
	ensure := func(builder bool) db.Session {
		t.Helper()
		got, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
			TeamID: team.ID, ChannelID: ch.ID, ProviderRef: "u:k1", Kind: "direct", Builder: builder,
		})
		if err != nil {
			t.Fatalf("EnsureSession: %v", err)
		}
		return got
	}
	first := ensure(true)
	if again := ensure(false); again.ID != first.ID || !again.Builder {
		t.Errorf("ensured again as %+v, want the same builder session", again)
	}
}

// The builder is an agent on the dashboard channel: a builder session with no
// channel is refused.
func TestABuilderSessionHasAChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform "+uuid.NewString()[:8])
	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO sessions (team_id, kind, builder) VALUES ($1, 'direct', true)`, team.ID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "sessions_builder_on_a_channel" {
		t.Errorf("err = %v, want sessions_builder_on_a_channel", err)
	}
}

// beforeBuilderSessions is 20261113200000_run_events_reasoning.sql, the last
// migration before builder conversations moved onto the dashboard channel.
const beforeBuilderSessions = 20261113200000

// A builder conversation from before moves onto its team's dashboard channel
// as the builder's. One that also held another agent's runs, and one that
// never held the builder's, stay as they were. Rolled back, the moved one
// stays on the channel, where it may now hold messages.
func TestOldBuilderConversationsMoveOntoTheDashboard(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), beforeBuilderSessions); err != nil {
		t.Fatalf("UpTo(%d): %v", beforeBuilderSessions, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	user := mustUser(t, s)
	session := func() uuid.UUID {
		return scalar[uuid.UUID](t, s,
			`INSERT INTO sessions (team_id, kind, user_id) VALUES ($1, 'direct', $2) RETURNING id`, team, user)
	}
	run := func(session uuid.UUID, builder bool) {
		t.Helper()
		id := uuid.New()
		if _, err := s.pool.Exec(t.Context(), `
			INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, builder, hub, reply_to_channel)
			VALUES ($1, $2, $3, $1, 'x', 'hi', $4, NOT $4, false)`, id, team, session, builder); err != nil {
			t.Fatalf("insert run: %v", err)
		}
	}
	builderOnly, mixed, hubOnly := session(), session(), session()
	run(builderOnly, true)
	run(builderOnly, true)
	run(mixed, true)
	run(mixed, false)
	run(hubOnly, false)

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	dashboard := scalar[uuid.UUID](t, s, `SELECT id FROM channels WHERE team_id = $1 AND provider = 'dashboard'`, team)
	var channel *uuid.UUID
	var builder bool
	var ref *string
	read := func(id uuid.UUID) {
		t.Helper()
		if err := s.pool.QueryRow(t.Context(),
			`SELECT channel_id, builder, provider_ref FROM sessions WHERE id = $1`, id).Scan(&channel, &builder, &ref); err != nil {
			t.Fatalf("read session: %v", err)
		}
	}
	read(builderOnly)
	if channel == nil || *channel != dashboard || !builder || ref == nil || *ref != user.String()+":builder-"+builderOnly.String() {
		t.Errorf("the builder's conversation is on %v, builder %v, ref %v; want the dashboard's, the builder's", channel, builder, ref)
	}
	for name, id := range map[string]uuid.UUID{"mixed": mixed, "the hub's": hubOnly} {
		read(id)
		if channel != nil || builder {
			t.Errorf("%s conversation moved: channel %v, builder %v", name, channel, builder)
		}
	}

	downTo(t, s, beforeBuilderSessions)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'sessions' AND column_name = 'builder'`); n != 0 {
		t.Error("sessions.builder is still there after the rollback")
	}
	if n := count(t, s, `SELECT count(*) FROM sessions WHERE id = $1 AND channel_id = $2`, builderOnly, dashboard); n != 1 {
		t.Error("the moved conversation left the dashboard channel on the rollback")
	}
}
