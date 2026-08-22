package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func sessionFixture(t *testing.T, s *Store, name string) (db.Channel, uuid.UUID) {
	t.Helper()

	team := mustCreate(t, s, "platform-"+name)
	ch := mustCreateChannel(t, s, team.ID, "custom", name)
	return ch, team.ID
}

// The same conversation twice is one session, touched. This is what stops
// every message becoming a conversation of its own.
func TestEnsureSessionIsFindOrCreate(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	first := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	second := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")

	if first.ID != second.ID {
		t.Errorf("two messages in one conversation made two sessions: %s and %s", first.ID, second.ID)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM sessions WHERE channel_id = $1`, ch.ID); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}
}

// The same statement that finds the session moves it to the top of the list,
// so a channel's sessions are ordered by when somebody last spoke rather than
// by when the conversation started.
func TestEnsureSessionTouchesTheSession(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	first := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE sessions SET last_message_at = now() - interval '1 day' WHERE id = $1`,
		first.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	second := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	if !second.LastMessageAt.After(first.LastMessageAt.Add(-25 * 60 * 60 * 1e9)) {
		t.Errorf("last_message_at was not moved forward: %v", second.LastMessageAt)
	}
}

// The kind is what the adapter saw when the conversation started. A later
// message arriving differently does not turn a direct message into a thread.
func TestEnsureSessionDoesNotChangeTheKind(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	again := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "thread")

	if again.Kind != "direct" {
		t.Errorf("kind = %q, want the kind it started as", again.Kind)
	}
}

// Two conversations in one channel are two sessions. Anything else merges
// unrelated threads into one context for the agent.
func TestTwoRefsInOneChannelAreTwoSessions(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	first := mustEnsureSession(t, s, teamID, ch.ID, "c-1", "thread")
	second := mustEnsureSession(t, s, teamID, ch.ID, "c-2", "thread")

	if first.ID == second.ID {
		t.Error("two conversations share a session")
	}
}

// A provider's conversation id is unique within its channel, not globally —
// the same reasoning that keeps channel_senders keyed on the channel.
func TestTheSameRefInTwoChannelsIsTwoSessions(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	mine, teamID := sessionFixture(t, s, "one")
	theirs := mustCreateChannel(t, s, teamID, "custom", "two")

	first := mustEnsureSession(t, s, teamID, mine.ID, "D01ABC", "direct")
	second := mustEnsureSession(t, s, teamID, theirs.ID, "D01ABC", "direct")

	if first.ID == second.ID {
		t.Error("one conversation id was shared across two channels")
	}
}

// Nothing closes a session today. When something does, the partial index is
// what lets the next message open a fresh one — and this is the test that says
// so before the sweep is written.
func TestAClosedSessionIsReopenedAsANewOne(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	first := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE sessions SET status = 'closed' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := mustEnsureSession(t, s, teamID, ch.ID, "D01ABC", "direct")
	if second.ID == first.ID {
		t.Fatal("a closed session was reused")
	}
	if n := scalar[int](t, s,
		`SELECT count(*) FROM sessions WHERE channel_id = $1 AND provider_ref = 'D01ABC'`,
		ch.ID); n != 2 {
		t.Errorf("%d sessions for that conversation, want 2", n)
	}
}

// A session names the team that owns the channel, and the database keeps the
// two equal rather than trusting every writer to.
func TestASessionCannotClaimAnotherTeam(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, _ := sessionFixture(t, s, "support")
	other := mustCreate(t, s, "other")

	_, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: other.ID, ChannelID: ch.ID, ProviderRef: "D01ABC", Kind: "direct",
	})
	wantPGCode(t, err, foreignKeyViolation, "a session claiming a team its channel is not in")
}

func TestASessionRefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	_, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: teamID, ChannelID: ch.ID, ProviderRef: "D01ABC", Kind: "broadcast",
	})
	wantPGCode(t, err, checkViolation, "a session of an invented kind")
}

// The same bound the gateway refuses at, for the same reason: this arrives
// from an unauthenticated webhook and is stored exactly as sent.
func TestASessionRefusesAnOversizedRef(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	_, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: teamID, ChannelID: ch.ID, Kind: "direct",
		ProviderRef: strings.Repeat("c", 257),
	})
	wantPGCode(t, err, checkViolation, "a session ref over the column bound")
}

func TestSessionsComeBackMostRecentFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")

	quiet := mustEnsureSession(t, s, teamID, ch.ID, "quiet", "direct")
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE sessions SET last_message_at = now() - interval '1 day' WHERE id = $1`,
		quiet.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	busy := mustEnsureSession(t, s, teamID, ch.ID, "busy", "direct")

	rows, err := s.ListSessionsByChannel(t.Context(), db.ListSessionsByChannelParams{
		ChannelID: &ch.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListSessionsByChannel: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d sessions, want 2", len(rows))
	}
	if rows[0].ID != busy.ID {
		t.Error("the quietest session is listed first")
	}
}

// A session started from the dashboard or the API has no channel, and has to
// be reachable through its team.
func TestASessionCanExistWithoutAChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'direct') RETURNING id`,
		team.ID).Scan(&id); err != nil {
		t.Fatalf("a session with no channel was refused: %v", err)
	}

	rows, err := s.ListSessionsByTeam(t.Context(), db.ListSessionsByTeamParams{
		TeamID: team.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListSessionsByTeam: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Errorf("a channelless session is not listed under its team: %+v", rows)
	}
	if rows[0].ChannelID != nil {
		t.Errorf("ChannelID = %v, want nil", *rows[0].ChannelID)
	}
}

// A ref with no channel names nothing.
func TestARefWithoutAChannelIsRefused(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO sessions (team_id, provider_ref, kind) VALUES ($1, 'orphan', 'direct')`,
		team.ID)
	wantPGCode(t, err, checkViolation, "a provider ref with no channel")
}
