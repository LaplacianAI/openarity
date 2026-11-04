package store

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func summarise(t *testing.T, s *Store, p db.TeamSummaryParams) db.TeamSummaryRow {
	t.Helper()

	row, err := s.TeamSummary(t.Context(), p)
	if err != nil {
		t.Fatalf("TeamSummary: %v", err)
	}
	return row
}

// runIn inserts a root run in a session, created hours ago on the database's
// clock, in a state its status allows.
func runIn(t *testing.T, s *Store, teamID, session, agent uuid.UUID, status string, hoursAgo int) uuid.UUID {
	t.Helper()

	id := mustInsertRun(t, s, inState(run{id: uuid.New(), team: teamID, session: session, agent: &agent}, status))
	age(t, s, id, "created_at", hoursAgo*3600)
	return id
}

// A team with nothing in it still has a summary: an aggregate with no GROUP BY
// returns its row, so a new team reads as zeros rather than as no row.
func TestANewTeamSummarisesAsZeros(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	got := summarise(t, s, db.TeamSummaryParams{TeamID: team.ID, Moderator: true})
	if got != (db.TeamSummaryRow{}) {
		t.Errorf("a new team summarises as %+v, want every count zero", got)
	}
}

// Runs are the last day's top-level runs, by status: a delegate's run is part
// of its parent's, an older one is history, and another team's is not this
// team's at all.
func TestTheSummaryCountsTheLastDaysRootRunsByStatus(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "elsewhere")

	// A different number of each, so a count read from the wrong status shows.
	for status, n := range map[string]int{"queued": 1, "succeeded": 3, "failed": 4} {
		for i := range n {
			runIn(t, s, f.team, f.session, f.agent, status, i)
		}
	}
	parent := runIn(t, s, f.team, f.session, f.agent, "running", 1)
	runIn(t, s, f.team, f.session, f.agent, "running", 23)
	runIn(t, s, f.team, f.session, f.agent, "failed", 25)
	mustInsertRun(t, s, inState(f.child(parent, "call-1"), "failed"))
	runIn(t, s, other.team, other.session, other.agent, "failed", 1)

	got := summarise(t, s, db.TeamSummaryParams{TeamID: f.team, Moderator: true})
	want := db.TeamSummaryRow{
		RunsQueued: 1, RunsRunning: 2, RunsSucceeded: 3, RunsFailed: 4, RunsCancelled: 0,
		Sessions: 1,
	}
	if got != want {
		t.Errorf("summarised %+v, want %+v", got, want)
	}
}

// Both counts follow the session rule, so nobody learns how many private
// conversations, or runs in them, they cannot open.
func TestTheSummaryFollowsTheSessionRule(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	asha := insertUser(t, s, "dev", "asha")
	bala := insertUser(t, s, "dev", "bala")

	direct, err := s.CreateDirectSession(t.Context(), db.CreateDirectSessionParams{TeamID: f.team, UserID: asha})
	if err != nil {
		t.Fatalf("CreateDirectSession: %v", err)
	}
	runIn(t, s, f.team, direct.ID, f.agent, "succeeded", 1)
	runIn(t, s, f.team, f.session, f.agent, "succeeded", 1)

	for name, tc := range map[string]struct {
		viewer    uuid.UUID
		moderator bool
		sessions  int64
		runs      int64
	}{
		"its participant": {asha, false, 2, 2},
		"another member":  {bala, false, 1, 1},
		"a moderator":     {bala, true, 2, 2},
	} {
		got := summarise(t, s, db.TeamSummaryParams{TeamID: f.team, Viewer: tc.viewer, Moderator: tc.moderator})
		if got.Sessions != tc.sessions || got.RunsSucceeded != tc.runs {
			t.Errorf("%s: %d sessions and %d runs, want %d and %d", name, got.Sessions, got.RunsSucceeded, tc.sessions, tc.runs)
		}
	}
}

// Waiting senders are counted across every channel of the team, and only
// this team's. Whether the caller may see the count is the handler's.
func TestTheSummaryCountsWaitingSendersAcrossTheTeamsChannels(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	support := mustCreateChannel(t, s, team.ID, "custom", "support")
	sales := mustCreateChannel(t, s, team.ID, "custom", "sales")
	elsewhere := mustCreateChannel(t, s, mustCreate(t, s, "elsewhere").ID, "custom", "support")

	record(t, s, support.ID, "u-1", "Asha")
	record(t, s, support.ID, "u-2", "Bala")
	record(t, s, sales.ID, "u-3", "Chen")
	record(t, s, support.ID, "u-1", "Asha")
	record(t, s, elsewhere.ID, "u-4", "Dev")

	if got := summarise(t, s, db.TeamSummaryParams{TeamID: team.ID}).WaitingSenders; got != 3 {
		t.Errorf("waiting senders = %d, want 3: three people, one of them seen twice", got)
	}
}

// A server is failing while its last discovery failed. One that failed and
// then worked is not, and neither is one never discovered.
func TestTheSummaryCountsServersWhoseLastDiscoveryFailed(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "elsewhere")

	mustFail(t, s, mustCreateServer(t, s, urlServer(team.ID, "failing")), "the server did not answer in time")
	recovered := mustCreateServer(t, s, urlServer(team.ID, "recovered"))
	mustFail(t, s, recovered, "the server did not answer in time")
	mustMark(t, s, recovered)
	mustCreateServer(t, s, urlServer(team.ID, "never"))
	mustFail(t, s, mustCreateServer(t, s, urlServer(other.ID, "theirs")), "the server did not answer in time")

	if got := summarise(t, s, db.TeamSummaryParams{TeamID: team.ID}).ServersFailing; got != 1 {
		t.Errorf("servers failing = %d, want 1", got)
	}
}
