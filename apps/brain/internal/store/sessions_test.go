package store

import (
	"reflect"
	"slices"
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

	// Moderator, because this is about ordering: without it the two direct
	// sessions would be filtered out and the test would pass for the wrong
	// reason the day ordering broke.
	rows, err := s.ListSessionsByChannel(t.Context(), db.ListSessionsByChannelParams{
		ChannelID: &ch.ID, PageSize: 10, Moderator: true,
	})
	if err != nil {
		t.Fatalf("ListSessionsByChannel: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d sessions, want 2", len(rows))
	}
	if rows[0].Session.ID != busy.ID {
		t.Error("the quietest session is listed first")
	}
}

// A session started from the dashboard or the API has no channel, and has to
// be reachable through its team.
func TestASessionCanExistWithoutAChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	// group, not direct: a direct session with no participant is visible to
	// moderators only, and this test is about having no channel rather than
	// about who may read it.
	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`,
		team.ID).Scan(&id); err != nil {
		t.Fatalf("a session with no channel was refused: %v", err)
	}

	rows, err := s.ListSessionsByTeam(t.Context(), db.ListSessionsByTeamParams{
		TeamID: team.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListSessionsByTeam: %v", err)
	}
	if len(rows) != 1 || rows[0].Session.ID != id {
		t.Errorf("a channelless session is not listed under its team: %+v", rows)
	}
	if rows[0].Session.ChannelID != nil || rows[0].ChannelName != nil {
		t.Errorf("ChannelID = %v, channel_name = %v; want both nil", rows[0].Session.ChannelID, rows[0].ChannelName)
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

// --- who may see a direct session ---

func mustCreateUser(t *testing.T, s *Store, subject string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('https://idp', $1) RETURNING id`,
		subject).Scan(&id); err != nil {
		t.Fatalf("create user %q: %v", subject, err)
	}
	return id
}

func directSession(t *testing.T, s *Store, teamID, channelID uuid.UUID, ref string, user *uuid.UUID) db.Session {
	t.Helper()

	session, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: teamID, ChannelID: channelID, ProviderRef: ref, Kind: "direct", UserID: user,
	})
	if err != nil {
		t.Fatalf("EnsureSession(%q): %v", ref, err)
	}
	return session
}

func listForTeam(t *testing.T, s *Store, teamID, viewer uuid.UUID, moderator bool) []db.Session {
	t.Helper()

	rows, err := s.ListSessionsByTeam(t.Context(), db.ListSessionsByTeamParams{
		TeamID: teamID, PageSize: 50, Viewer: viewer, Moderator: moderator,
	})
	if err != nil {
		t.Fatalf("ListSessionsByTeam: %v", err)
	}

	out := make([]db.Session, len(rows))
	for i, r := range rows {
		out[i] = r.Session
	}
	return out
}

func has(rows []db.Session, id uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// The whole point of the column: Asha's private conversation is hers, and a
// teammate reading the same list must not see it at all — not a redacted row,
// not a count, nothing.
func TestADirectSessionIsListedOnlyForItsParticipant(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "dm-visibility")

	asha := mustCreateUser(t, s, "asha")
	bala := mustCreateUser(t, s, "bala")
	dm := directSession(t, s, teamID, ch.ID, "D-ASHA", &asha)

	if !has(listForTeam(t, s, teamID, asha, false), dm.ID) {
		t.Error("the participant cannot see their own direct session")
	}
	if has(listForTeam(t, s, teamID, bala, false), dm.ID) {
		t.Error("a teammate can read somebody else's direct session")
	}
	if !has(listForTeam(t, s, teamID, bala, true), dm.ID) {
		t.Error("a moderator cannot see a direct session")
	}
}

// A shared room is shared. Without this the filter could hide everything and
// the test above would still pass.
func TestAGroupSessionIsListedForTheWholeTeam(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "group-visibility")

	bala := mustCreateUser(t, s, "bala")
	group := mustEnsureSession(t, s, teamID, ch.ID, "C-SUPPORT", "group")

	if !has(listForTeam(t, s, teamID, bala, false), group.ID) {
		t.Error("a group session is hidden from a team member")
	}
}

// user_id is ON DELETE SET NULL, so a deleted person leaves direct sessions
// behind with no participant. Null has to mean nobody: read as "unset, so
// unrestricted" it would publish exactly the conversations that were private.
func TestADirectSessionWithNoParticipantIsModeratorOnly(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "orphan-dm")

	asha := mustCreateUser(t, s, "asha")
	bala := mustCreateUser(t, s, "bala")
	dm := directSession(t, s, teamID, ch.ID, "D-ASHA", &asha)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, asha); err != nil {
		t.Fatalf("delete the participant: %v", err)
	}

	row, err := s.GetSession(t.Context(), dm.ID)
	if err != nil {
		t.Fatalf("the session went with the user: %v", err)
	}
	if row.UserID != nil {
		t.Errorf("UserID = %v, want nil after the user was deleted", *row.UserID)
	}
	if has(listForTeam(t, s, teamID, bala, false), dm.ID) {
		t.Error("an orphaned direct session is readable by the team")
	}
	if !has(listForTeam(t, s, teamID, bala, true), dm.ID) {
		t.Error("an orphaned direct session is invisible even to a moderator")
	}
}

// The channel list is a second query with the same rule, and a filter fixed
// in one and forgotten in the other is the likely mistake.
func TestTheChannelListHidesOtherPeoplesDirectSessions(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "channel-dm")

	asha := mustCreateUser(t, s, "asha")
	bala := mustCreateUser(t, s, "bala")
	dm := directSession(t, s, teamID, ch.ID, "D-ASHA", &asha)

	byChannel := func(viewer uuid.UUID, moderator bool) []db.Session {
		t.Helper()
		rows, err := s.ListSessionsByChannel(t.Context(), db.ListSessionsByChannelParams{
			ChannelID: &ch.ID, PageSize: 50, Viewer: viewer, Moderator: moderator,
		})
		if err != nil {
			t.Fatalf("ListSessionsByChannel: %v", err)
		}

		out := make([]db.Session, len(rows))
		for i, r := range rows {
			out[i] = r.Session
		}
		return out
	}

	if !has(byChannel(asha, false), dm.ID) {
		t.Error("the participant cannot see their own direct session in the channel list")
	}
	if has(byChannel(bala, false), dm.ID) {
		t.Error("the channel list leaks somebody else's direct session")
	}
	if !has(byChannel(bala, true), dm.ID) {
		t.Error("a moderator cannot see a direct session in the channel list")
	}
}

// A group session naming one participant would read as "this belongs to one
// person" while several people speak in it. The database refuses rather than
// trusting every writer.
func TestAGroupSessionCannotNameAParticipant(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "group-participant")
	asha := mustCreateUser(t, s, "asha")

	_, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: teamID, ChannelID: ch.ID, ProviderRef: "C-SUPPORT", Kind: "group", UserID: &asha,
	})
	if err == nil {
		t.Fatal("a group session was allowed to name a participant")
	}
	if !strings.Contains(err.Error(), "sessions_participant_only_when_direct") {
		t.Errorf("refused by something else: %v", err)
	}
}

// EnsureSession is find-or-create, and the find half must not move the
// participant: a second approved sender reaching the same conversation would
// otherwise take over somebody else's private thread.
func TestEnsureSessionKeepsTheFirstParticipant(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "participant-stays")

	asha := mustCreateUser(t, s, "asha")
	bala := mustCreateUser(t, s, "bala")

	first := directSession(t, s, teamID, ch.ID, "D-ASHA", &asha)
	second := directSession(t, s, teamID, ch.ID, "D-ASHA", &bala)

	if first.ID != second.ID {
		t.Fatalf("two sessions for one conversation: %s and %s", first.ID, second.ID)
	}
	if second.UserID == nil || *second.UserID != asha {
		t.Errorf("UserID moved to %v, want it to stay with the first speaker", second.UserID)
	}
}

// --- what a listed session shows ---

func listRows(t *testing.T, s *Store, p db.ListSessionsByTeamParams) []db.ListSessionsByTeamRow {
	t.Helper()

	if p.PageSize == 0 {
		p.PageSize = 50
	}
	rows, err := s.ListSessionsByTeam(t.Context(), p)
	if err != nil {
		t.Fatalf("ListSessionsByTeam: %v", err)
	}
	return rows
}

func rowFor(t *testing.T, rows []db.ListSessionsByTeamRow, id uuid.UUID) db.ListSessionsByTeamRow {
	t.Helper()

	for _, r := range rows {
		if r.Session.ID == id {
			return r
		}
	}
	t.Fatalf("session %s is not listed", id)
	return db.ListSessionsByTeamRow{}
}

// said inserts a message received seconds ago, on the database's clock.
func said(t *testing.T, s *Store, ch db.Channel, session, user uuid.UUID, text string, secondsAgo int) {
	t.Helper()

	id := insertMessage(t, s, message(ch, db.Session{ID: session}, user, uuid.NewString(), text))
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE messages SET received_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, secondsAgo); err != nil {
		t.Fatalf("backdate message: %v", err)
	}
}

// ranIn inserts a root run in a session, created seconds ago.
func ranIn(t *testing.T, s *Store, teamID, session, agent uuid.UUID, status string, secondsAgo int) uuid.UUID {
	t.Helper()

	id := mustInsertRun(t, s, inState(run{id: uuid.New(), team: teamID, session: session, agent: &agent}, status))
	age(t, s, id, "created_at", secondsAgo)
	return id
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// A group session is shown by its channel, whoever spoke last, what they
// said, and how the newest run the team started in it went. A delegate's run
// is part of its parent's, so a newer child does not count.
func TestAListedSessionShowsItsChannelNewestMessageAndLatestRun(t *testing.T) {
	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")
	agent := insertAgent(t, s, teamID, "triage")
	asha := insertUser(t, s, "dev", "asha")
	bala := insertUser(t, s, "dev", "bala")

	room := mustEnsureSession(t, s, teamID, ch.ID, "C-ROOM", "group")
	said(t, s, ch, room.ID, asha, "first", 60)
	said(t, s, ch, room.ID, bala, "second", 5)
	ranIn(t, s, teamID, room.ID, agent, "failed", 120)
	newer := ranIn(t, s, teamID, room.ID, agent, "succeeded", 30)
	child := run{
		id: uuid.New(), team: teamID, session: room.ID, agent: &agent,
		root: newer, parent: &newer, toolCall: ptr("call-1"), depth: 1,
	}
	mustInsertRun(t, s, inState(child, "failed"))

	got := rowFor(t, listRows(t, s, db.ListSessionsByTeamParams{TeamID: teamID, Moderator: true}), room.ID)
	if deref(got.ChannelName) != ch.Name || deref(got.ParticipantEmail) != "bala@example.com" ||
		deref(got.LastMessage) != "second" || deref(got.LatestRunStatus) != "succeeded" {
		t.Errorf("channel %s, participant %s, last %s, run %s; want %s, bala@example.com, second, succeeded",
			deref(got.ChannelName), deref(got.ParticipantEmail), deref(got.LastMessage), deref(got.LatestRunStatus), ch.Name)
	}

	// The channel's list must not disagree with the team's about a row.
	byChannel, err := s.ListSessionsByChannel(t.Context(), db.ListSessionsByChannelParams{
		ChannelID: &ch.ID, PageSize: 10, Moderator: true,
	})
	if err != nil || len(byChannel) != 1 {
		t.Fatalf("ListSessionsByChannel: %d rows, %v", len(byChannel), err)
	}
	if !reflect.DeepEqual(db.ListSessionsByTeamRow(byChannel[0]), got) {
		t.Errorf("the channel list shows %+v, the team list %+v", byChannel[0], got)
	}
}

// A direct session is with one person, whoever spoke last in it.
func TestADirectSessionsParticipantIsWhoItIsWith(t *testing.T) {
	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")
	asha := insertUser(t, s, "dev", "asha")
	bala := insertUser(t, s, "dev", "bala")

	dm := directSession(t, s, teamID, ch.ID, "D-ASHA", &asha)
	said(t, s, ch, dm.ID, bala, "speaking in asha's thread", 1)

	got := rowFor(t, listRows(t, s, db.ListSessionsByTeamParams{TeamID: teamID, Moderator: true}), dm.ID)
	if deref(got.ParticipantEmail) != "asha@example.com" {
		t.Errorf("participant = %s, want asha@example.com", deref(got.ParticipantEmail))
	}
}

// A session with no channel, no messages and no run lists every extra as
// null. This is the row that fails to scan if a column is generated as a
// plain string, which a LATERAL join would have done.
func TestASessionWithNothingInItListsNulls(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	carol := mustCreateUser(t, s, "carol")

	empty := insertSession(t, s, team.ID)
	direct, err := s.CreateDirectSession(t.Context(), db.CreateDirectSessionParams{TeamID: team.ID, UserID: carol})
	if err != nil {
		t.Fatalf("CreateDirectSession: %v", err)
	}

	rows := listRows(t, s, db.ListSessionsByTeamParams{TeamID: team.ID, Moderator: true})
	got := rowFor(t, rows, empty)
	if got.ChannelName != nil || got.ParticipantEmail != nil || got.ParticipantSubject != nil ||
		got.LastMessage != nil || got.LatestRunStatus != nil {
		t.Errorf("an empty session lists %+v, want every extra null", got)
	}

	// A person with no email is still named, by their subject.
	dm := rowFor(t, rows, direct.ID)
	if dm.ParticipantEmail != nil || deref(dm.ParticipantSubject) != "carol" {
		t.Errorf("email %s, subject %s; want none and carol", deref(dm.ParticipantEmail), deref(dm.ParticipantSubject))
	}
}

func TestTheSessionListFilters(t *testing.T) {
	s := queryStore(t)
	ch, teamID := sessionFixture(t, s, "support")
	other := mustCreateChannel(t, s, teamID, "custom", "sales")
	elsewhere, _ := sessionFixture(t, s, "elsewhere")
	agent := insertAgent(t, s, teamID, "triage")

	failing := mustEnsureSession(t, s, teamID, ch.ID, "failing", "group")
	ranIn(t, s, teamID, failing.ID, agent, "failed", 10)
	recovered := mustEnsureSession(t, s, teamID, ch.ID, "recovered", "group")
	ranIn(t, s, teamID, recovered.ID, agent, "failed", 60)
	ranIn(t, s, teamID, recovered.ID, agent, "succeeded", 10)
	quiet := mustEnsureSession(t, s, teamID, ch.ID, "quiet", "group")
	closed := mustEnsureSession(t, s, teamID, ch.ID, "closed", "group")
	if _, err := s.pool.Exec(t.Context(), `UPDATE sessions SET status = 'closed' WHERE id = $1`, closed.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	sales := mustEnsureSession(t, s, teamID, other.ID, "sales", "group")

	all := []uuid.UUID{failing.ID, recovered.ID, quiet.ID, closed.ID, sales.ID}
	for name, tc := range map[string]struct {
		p    db.ListSessionsByTeamParams
		want []uuid.UUID
	}{
		"nil filters":   {db.ListSessionsByTeamParams{}, all},
		"empty filters": {db.ListSessionsByTeamParams{Statuses: []string{}, RunStatuses: []string{}}, all},
		// The newest run is what is asked about: a session that failed and
		// then succeeded is not failing, and one with no run matches no status.
		"a failing latest run": {db.ListSessionsByTeamParams{RunStatuses: []string{"failed"}}, []uuid.UUID{failing.ID}},
		"either status": {
			db.ListSessionsByTeamParams{RunStatuses: []string{"failed", "succeeded"}},
			[]uuid.UUID{failing.ID, recovered.ID},
		},
		"closed":                 {db.ListSessionsByTeamParams{Statuses: []string{"closed"}}, []uuid.UUID{closed.ID}},
		"open":                   {db.ListSessionsByTeamParams{Statuses: []string{"open"}}, []uuid.UUID{failing.ID, recovered.ID, quiet.ID, sales.ID}},
		"one channel":            {db.ListSessionsByTeamParams{ChannelID: &other.ID}, []uuid.UUID{sales.ID}},
		"another team's channel": {db.ListSessionsByTeamParams{ChannelID: &elsewhere.ID}, []uuid.UUID{}},
	} {
		tc.p.TeamID, tc.p.Moderator = teamID, true
		rows := listRows(t, s, tc.p)
		got := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			got[i] = r.Session.ID
		}
		slices.SortFunc(got, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
		want := slices.Clone(tc.want)
		slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
		if !slices.Equal(got, want) {
			t.Errorf("%s: listed %v, want %v", name, got, want)
		}
	}
}
