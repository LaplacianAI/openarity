package store

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A team is deleted in two steps: marked, which hides it and can be undone,
// and purged a week later, which is the cascade that already existed. These
// tests hold the queries to both halves of that — the mark has to hide the
// team from every reader except the ones that need it, and the purge has to be
// nothing more than the old DELETE, so the tombstones still fire.

func softDelete(t *testing.T, s *Store, id uuid.UUID) int64 {
	t.Helper()

	n, err := s.SoftDeleteTeam(t.Context(), id)
	if err != nil {
		t.Fatalf("SoftDeleteTeam: %v", err)
	}
	return n
}

// deletedAgo marks a team deleted at a time on the database's own clock, so
// the purge boundary is never a comparison between Go's clock and Postgres's.
func deletedAgo(t *testing.T, s *Store, id uuid.UUID, ago time.Duration) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE teams SET deleted_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, ago.Seconds()); err != nil {
		t.Fatalf("deletedAgo: %v", err)
	}
}

func dbNow(t *testing.T, s *Store) time.Time {
	t.Helper()
	return scalar[time.Time](t, s, `SELECT now()`)
}

func TestSoftDeleteMarksTheTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "doomed")

	if n := softDelete(t, s, team.ID); n != 1 {
		t.Fatalf("SoftDeleteTeam affected %d rows, want 1", n)
	}

	got, err := s.GetTeam(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.DeletedAt == nil {
		t.Error("deleted_at is still null")
	}
}

// The 409 rests on this: a run that is queued or running holds the team, and
// one that has finished does not.
func TestSoftDeleteRefusesLiveRuns(t *testing.T) {
	s := queryStore(t)

	queued := newRunFixture(t, s, "queued")
	createRun(t, s, queued)

	running := newRunFixture(t, s, "running")
	claim(t, s, createRun(t, s, running).ID, "w1")

	finished := newRunFixture(t, s, "finished")
	r := createRun(t, s, finished)
	claim(t, s, r.ID, "w1")
	if _, err := s.FinishRun(t.Context(), db.FinishRunParams{ID: r.ID, Worker: "w1", Status: "succeeded"}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	for _, tc := range []struct {
		name string
		team uuid.UUID
		want int64
	}{
		{"queued", queued.team, 0},
		{"running", running.team, 0},
		{"succeeded", finished.team, 1},
	} {
		if n := softDelete(t, s, tc.team); n != tc.want {
			t.Errorf("a team with a %s run: %d rows, want %d", tc.name, n, tc.want)
		}
	}
}

// A repeated delete must not move the purge date: if it did, calling it once
// a day would keep a team from ever being erased.
func TestSoftDeleteKeepsTheFirstDeletedAt(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "twice")

	deletedAgo(t, s, team.ID, 3*24*time.Hour)
	first := scalar[time.Time](t, s, `SELECT deleted_at FROM teams WHERE id = $1`, team.ID)

	if n := softDelete(t, s, team.ID); n != 0 {
		t.Errorf("a second delete affected %d rows, want 0", n)
	}
	if again := scalar[time.Time](t, s, `SELECT deleted_at FROM teams WHERE id = $1`, team.ID); !again.Equal(first) {
		t.Errorf("deleted_at moved from %s to %s", first, again)
	}
}

func TestSoftDeleteOfAMissingTeam(t *testing.T) {
	s := queryStore(t)

	if n := softDelete(t, s, uuid.New()); n != 0 {
		t.Errorf("deleting a team that does not exist affected %d rows", n)
	}
}

func TestRestoreBringsTheTeamBack(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "comeback")
	softDelete(t, s, team.ID)

	got, err := s.RestoreTeam(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("RestoreTeam: %v", err)
	}
	if got.DeletedAt != nil {
		t.Errorf("deleted_at = %v after a restore", got.DeletedAt)
	}
	if _, err := s.GetLiveTeam(t.Context(), team.ID); err != nil {
		t.Errorf("GetLiveTeam after a restore: %v", err)
	}
}

func TestRestoreOfAMissingTeam(t *testing.T) {
	s := queryStore(t)

	_, err := s.RestoreTeam(t.Context(), uuid.New())
	wantNoRow(t, err, "RestoreTeam of a team that does not exist")
}

// A deleted team's name is free at once, so a team of the same name can be
// made the same day. Restoring the old one then has to fail, not produce two.
func TestADeletedTeamFreesItsName(t *testing.T) {
	s := queryStore(t)
	old := mustCreate(t, s, "platform")
	softDelete(t, s, old.ID)

	mustCreate(t, s, "platform")

	_, err := s.RestoreTeam(t.Context(), old.ID)
	wantPGCode(t, err, uniqueViolation, "restoring a team whose name was taken")

	if n := scalar[int](t, s, `SELECT count(*) FROM teams WHERE name = 'platform' AND deleted_at IS NULL`); n != 1 {
		t.Errorf("%d live teams are called platform", n)
	}
}

// Two live teams still cannot share a name; only deleted ones step aside.
func TestLiveTeamsStillHaveUniqueNames(t *testing.T) {
	s := queryStore(t)
	mustCreate(t, s, "platform")

	_, err := s.CreateTeam(t.Context(), "platform")
	wantPGCode(t, err, uniqueViolation, "a second live team called platform")
}

// Memberships are loaded on every request through ListUserTeams, so this one
// filter is what turns a deleted team into a 404 for every member on every
// route.
func TestListUserTeamsHidesADeletedTeam(t *testing.T) {
	s := queryStore(t)
	user := insertUser(t, s, "https://idp.example.com", "ada")
	kept := mustCreate(t, s, "kept")
	gone := mustCreate(t, s, "gone")
	for _, id := range []uuid.UUID{kept.ID, gone.ID} {
		if err := insertMember(t, s, id, user, "member"); err != nil {
			t.Fatalf("insertMember: %v", err)
		}
	}
	softDelete(t, s, gone.ID)

	rows, err := s.ListUserTeams(t.Context(), user)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != kept.ID {
		t.Errorf("ListUserTeams = %+v, want only %q", rows, "kept")
	}
}

func TestListTeamsHidesADeletedTeam(t *testing.T) {
	s := queryStore(t)
	mustCreate(t, s, "kept")
	softDelete(t, s, mustCreate(t, s, "gone").ID)

	teams, err := allTeams(t.Context(), s)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if got := teamNames(teams); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("ListTeams = %v, want [kept]", got)
	}
}

func TestListDeletedTeamsListsOnlyDeletedOnes(t *testing.T) {
	s := queryStore(t)
	mustCreate(t, s, "kept")
	softDelete(t, s, mustCreate(t, s, "gone").ID)

	teams, err := s.ListDeletedTeams(t.Context(), db.ListDeletedTeamsParams{PageSize: 1000})
	if err != nil {
		t.Fatalf("ListDeletedTeams: %v", err)
	}
	if got := teamNames(teams); !slices.Equal(got, []string{"gone"}) {
		t.Errorf("ListDeletedTeams = %v, want [gone]", got)
	}
}

// The cursor walks deleted teams only, newest first, without repeating or
// skipping one — the same contract ListTeams keeps.
func TestListDeletedTeamsPagesThroughEveryRow(t *testing.T) {
	s := queryStore(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var want []string
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		team := mustCreate(t, s, name)
		backdate(t, s, team.ID, base.Add(time.Duration(i)*time.Hour))
		softDelete(t, s, team.ID)
		want = append([]string{name}, want...)
	}
	mustCreate(t, s, "live")

	var got []string
	params := db.ListDeletedTeamsParams{PageSize: 2}
	for range 10 {
		page, err := s.ListDeletedTeams(t.Context(), params)
		if err != nil {
			t.Fatalf("ListDeletedTeams: %v", err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, teamNames(page)...)
		last := page[len(page)-1]
		params = db.ListDeletedTeamsParams{UseCursor: true, AfterCreatedAt: last.CreatedAt, AfterID: last.ID, PageSize: 2}
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged = %v, want %v", got, want)
	}
}

// GetTeam is what the graph projector reads to decide a team is gone and drop
// its graph. A deleted team is not gone yet, so GetTeam must still find it;
// GetLiveTeam is the one the API reads.
func TestGetTeamStillSeesADeletedTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "hidden")
	softDelete(t, s, team.ID)

	if _, err := s.GetTeam(t.Context(), team.ID); err != nil {
		t.Errorf("GetTeam of a deleted team: %v", err)
	}
	_, err := s.GetLiveTeam(t.Context(), team.ID)
	wantNoRow(t, err, "GetLiveTeam of a deleted team")
}

// Webhooks reach a team through GetChannel, so a deleted team's channels have
// to be as unknown as a channel that never existed.
func TestGetChannelHidesADeletedTeamsChannel(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "quiet")
	ch := mustCreateChannel(t, s, team.ID, "custom", "inbox")
	softDelete(t, s, team.ID)

	_, err := s.GetChannel(t.Context(), ch.ID)
	wantNoRow(t, err, "GetChannel on a deleted team")

	if _, err := s.RestoreTeam(t.Context(), team.ID); err != nil {
		t.Fatalf("RestoreTeam: %v", err)
	}
	if _, err := s.GetChannel(t.Context(), ch.ID); err != nil {
		t.Errorf("GetChannel after a restore: %v", err)
	}
}

func TestLockLiveTeamRefusesADeletedTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "locked")

	if _, err := s.LockLiveTeam(t.Context(), team.ID); err != nil {
		t.Fatalf("LockLiveTeam on a live team: %v", err)
	}
	softDelete(t, s, team.ID)

	_, err := s.LockLiveTeam(t.Context(), team.ID)
	wantNoRow(t, err, "LockLiveTeam on a deleted team")
}

// A rebuild of every team must not project the deleted ones back.
func TestListTeamIDsLeavesOutDeletedTeams(t *testing.T) {
	s := queryStore(t)
	kept := mustCreate(t, s, "kept")
	softDelete(t, s, mustCreate(t, s, "gone").ID)

	ids, err := s.ListTeamIDs(t.Context())
	if err != nil {
		t.Fatalf("ListTeamIDs: %v", err)
	}
	if !slices.Equal(ids, []uuid.UUID{kept.ID}) {
		t.Errorf("ListTeamIDs = %v, want [%s]", ids, kept.ID)
	}
}

// The purge is the old DELETE, so the cascade and its triggers still run:
// the channel's signing secret is tombstoned exactly as it was before.
func TestPurgeTeamsBoundary(t *testing.T) {
	s := queryStore(t)
	const window = 7 * 24 * time.Hour

	expired := mustCreate(t, s, "expired")
	ch := mustCreateChannel(t, s, expired.ID, "custom", "inbox")
	deletedAgo(t, s, expired.ID, window+time.Minute)

	recent := mustCreate(t, s, "recent")
	deletedAgo(t, s, recent.ID, window-time.Minute)

	live := mustCreate(t, s, "live")

	purged, err := s.PurgeTeams(t.Context(), db.PurgeTeamsParams{Before: dbNow(t, s).Add(-window), Limit: 10})
	if err != nil {
		t.Fatalf("PurgeTeams: %v", err)
	}
	if !slices.Equal(purged, []uuid.UUID{expired.ID}) {
		t.Errorf("purged %v, want only %s", purged, expired.ID)
	}

	for _, keep := range []db.Team{recent, live} {
		if _, err := s.GetTeam(t.Context(), keep.ID); err != nil {
			t.Errorf("%s was purged: %v", keep.Name, err)
		}
	}

	want := secrets.Path(expired.ID, secrets.KindChannel, ch.ID)
	rows := secretTombstones(t, s)
	if !slices.ContainsFunc(rows, func(r db.DeletedSecret) bool { return r.Path == want }) {
		t.Errorf("no tombstone for %s after the purge; got %+v", want, rows)
	}
}

func TestPurgeTeamsHonoursTheLimit(t *testing.T) {
	s := queryStore(t)
	for _, name := range []string{"a", "b", "c"} {
		deletedAgo(t, s, mustCreate(t, s, name).ID, 30*24*time.Hour)
	}

	purged, err := s.PurgeTeams(t.Context(), db.PurgeTeamsParams{Before: dbNow(t, s), Limit: 2})
	if err != nil {
		t.Fatalf("PurgeTeams: %v", err)
	}
	if len(purged) != 2 {
		t.Errorf("purged %d teams with a limit of 2", len(purged))
	}
	if n := countTeams(t, s); n != 1 {
		t.Errorf("%d teams left, want 1", n)
	}
}
