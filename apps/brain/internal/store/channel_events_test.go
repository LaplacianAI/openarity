package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func showingEvents(t *testing.T, s *Store, c conversation, mode string) {
	t.Helper()
	if _, err := s.pool.Exec(t.Context(), `UPDATE channels SET reply_mode = $1 WHERE id = $2`, mode, c.channel.ID); err != nil {
		t.Fatalf("setting the reply mode: %v", err)
	}
}

// working is a top-level run in c, started by a message and still running.
func (c conversation) working(t *testing.T, s *Store, externalID string) db.Run {
	t.Helper()
	m := c.says(t, s, externalID, "why did the deploy fail?")
	r, err := c.startRun(t, s.Queries, &m, "why did the deploy fail?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)
	return r
}

func tell(t *testing.T, s *Store, run uuid.UUID, seq int32, text string) int64 {
	t.Helper()
	n, err := s.InsertEventReply(t.Context(), db.InsertEventReplyParams{RunID: run, EventSeq: seq, Text: text})
	if err != nil {
		t.Fatalf("InsertEventReply: %v", err)
	}
	return n
}

func showsEvents(t *testing.T, s *Store, run uuid.UUID) bool {
	t.Helper()
	shows, err := s.RunShowsEvents(t.Context(), run)
	if err != nil {
		t.Fatalf("RunShowsEvents: %v", err)
	}
	return shows
}

// A channel sends its answer alone unless it is set to send each step: a run
// on it neither shows nor writes them.
func TestAChannelSendsItsAnswerAloneByDefault(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "alone")

	r := c.working(t, s, "m1")
	if showsEvents(t, s, r.ID) {
		t.Error("a run on a channel left as it was shows its steps")
	}
	if n := tell(t, s, r.ID, 3, "Using `read_logs`…"); n != 0 {
		t.Errorf("told %d steps, want none", n)
	}
	if n := count(t, s, `SELECT count(*) FROM replies WHERE run_id = $1`, r.ID); n != 0 {
		t.Errorf("%d replies, want none", n)
	}
}

// Set to send each step, a run's steps go out before its answer, in the order
// they were told, each quoting nothing and named by its run event; telling
// one twice is still one.
func TestAChannelSendingStepsSendsThemBeforeTheAnswer(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "steps")
	showingEvents(t, s, c, "events")

	r := c.working(t, s, "m1")
	if !showsEvents(t, s, r.ID) {
		t.Fatal("the run does not show its steps")
	}
	tell(t, s, r.ID, 3, "Let me check the logs.")
	tell(t, s, r.ID, 4, "Using `read_logs`…")
	if n := tell(t, s, r.ID, 4, "Using `read_logs`…"); n != 0 {
		t.Errorf("a step told twice wrote %d more", n)
	}
	endChannelRun(t, s, r.ID, "succeeded", "It timed out.", "")

	var got []string
	for range 3 {
		rows := claimReplies(t, s.Queries, 60)
		if len(rows) != 1 {
			t.Fatalf("claimed %d at once, want one per conversation", len(rows))
		}
		row := rows[0]
		switch {
		case row.Kind == "event" && (row.EventSeq == nil || row.InReplyTo != nil):
			t.Errorf("a step names %v and quotes %v; want its seq and nothing", row.EventSeq, row.InReplyTo)
		case row.Kind == "answer" && (row.EventSeq != nil || row.InReplyTo == nil):
			t.Errorf("the answer names %v and quotes %v; want no seq and the message", row.EventSeq, row.InReplyTo)
		}
		got = append(got, row.Kind+":"+row.Text)
		if _, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: row.ID, Attempts: row.Attempts}); err != nil {
			t.Fatalf("ReplySent: %v", err)
		}
	}
	want := []string{"event:Let me check the logs.", "event:Using `read_logs`…", "answer:It timed out."}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("sent %q, want %q", got, want)
	}

	// What became of the run's reply is its answer's, not a step's.
	found, err := s.ListRunReplies(t.Context(), []uuid.UUID{r.ID})
	if err != nil {
		t.Fatalf("ListRunReplies: %v", err)
	}
	if len(found) != 1 || found[0].Kind != "answer" {
		t.Errorf("the run's replies are %+v, want its answer alone", found)
	}
}

// A run another agent was handed tells nothing: its parent says it asked.
func TestADelegatedRunTellsNoSteps(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "child")
	showingEvents(t, s, c, "events")

	parent := c.working(t, s, "m1")
	child := uuid.New()
	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, parent_run_id, root_run_id, depth, parent_tool_call, agent_name, input, reply_to_channel)
		VALUES ($1, $2, $3, $4, $4, 1, 'c1', 'billing', 'look', false)`, child, c.team, c.session.ID, parent.ID); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if showsEvents(t, s, child) {
		t.Error("a delegated run shows its steps")
	}
	if n := tell(t, s, child, 2, "Using `ledger`…"); n != 0 {
		t.Errorf("a delegated run told %d steps", n)
	}
}

// A channel switched back to its answer alone while a run works is told no
// more of it.
func TestAChannelSwitchedBackIsToldNoMore(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "switched")
	showingEvents(t, s, c, "events")

	r := c.working(t, s, "m1")
	tell(t, s, r.ID, 3, "Using `read_logs`…")
	showingEvents(t, s, c, "final")
	if n := tell(t, s, r.ID, 5, "Asking `billing`…"); n != 0 {
		t.Errorf("told %d steps after the switch", n)
	}
}

// A mode is one of two.
func TestAReplyModeIsFinalOrEvents(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "mode")

	_, err := s.pool.Exec(t.Context(), `UPDATE channels SET reply_mode = 'tokens' WHERE id = $1`, c.channel.ID)
	if !isCheckViolation(err, "channels_reply_mode_known") {
		t.Errorf("err = %v, want channels_reply_mode_known", err)
	}
	mode := "events"
	got, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: c.channel.ID, TeamID: c.team, ReplyMode: &mode})
	if err != nil || got.ReplyMode != "events" || got.NotifyStrangers != c.channel.NotifyStrangers {
		t.Errorf("UpdateChannel = %+v, %v; want the mode alone changed", got, err)
	}
}

// beforeChannelEvents is 20261114120000_builder_sessions.sql, the last
// migration before a channel could send a run's steps.
const beforeChannelEvents = 20261114120000

// Rolled back, the steps waiting to be sent go and the answers stay; a run
// has one answer again.
func TestChannelEventsRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	c := newConversation(t, s, "rollback")
	showingEvents(t, s, c, "events")
	r := c.working(t, s, "m1")
	tell(t, s, r.ID, 3, "Using `read_logs`…")
	endChannelRun(t, s, r.ID, "succeeded", "It timed out.", "")

	downTo(t, s, beforeChannelEvents)
	if n := count(t, s, `SELECT count(*) FROM replies WHERE run_id = $1`, r.ID); n != 1 {
		t.Errorf("%d replies after the rollback, want the answer alone", n)
	}
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'channels' AND column_name = 'reply_mode'`); n != 0 {
		t.Error("channels.reply_mode is still there after the rollback")
	}
	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text)
		SELECT team_id, run_id, session_id, channel_id, 'answer', 'again' FROM replies WHERE run_id = $1`, r.ID)
	if !isUniqueViolation(err) {
		t.Errorf("a second answer for one run: err = %v, want a unique violation", err)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate again: %v", err)
	}
}

func isCheckViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" && pg.ConstraintName == constraint
}
