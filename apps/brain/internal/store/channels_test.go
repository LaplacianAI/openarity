package store

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func mustCreateChannel(t *testing.T, s *Store, teamID uuid.UUID, provider, name string) db.Channel {
	t.Helper()

	ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: teamID, Provider: provider, Name: name,
	})
	if err != nil {
		t.Fatalf("CreateChannel(%s, %q, %q): %v", teamID, provider, name, err)
	}
	return ch
}

func backdateChannel(t *testing.T, s *Store, id uuid.UUID, at time.Time) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(),
		"UPDATE channels SET created_at = $1 WHERE id = $2", at, id); err != nil {
		t.Fatalf("backdate channel %s: %v", id, err)
	}
}

// A channel belongs to exactly one team, because that is what scopes every
// permission check and what derives its secret path.
func TestChannelIsScopedToOneTeam(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if ch.TeamID != team.ID {
		t.Errorf("TeamID = %s, want %s", ch.TeamID, team.ID)
	}
	if ch.ID == uuid.Nil {
		t.Error("the channel has no id, and the id is the routing key in its hook URL")
	}
}

// Deleting a team must not orphan its channels — their secrets live under
// teams/<team_id>/channels/<id>, so an orphan row points at a path nobody
// owns and can never be cleaned up.
func TestDeletingATeamDeletesItsChannels(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if err := s.DeleteTeam(t.Context(), team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}

	if _, err := s.GetChannel(t.Context(), ch.ID); err == nil {
		t.Error("the channel survived its team")
	}
}

func TestAChannelCannotBelongToATeamThatDoesNotExist(t *testing.T) {
	t.Parallel()

	s := queryStore(t)

	_, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: uuid.New(), Provider: "custom", Name: "support",
	})
	wantPGCode(t, err, foreignKeyViolation, "a channel in a team that does not exist")
}

// The name is how a person names a channel on the command line, so "Support"
// and "support" being two channels is a trap rather than a feature.
func TestATeamCannotHaveTwoChannelsWithOneName(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mustCreateChannel(t, s, team.ID, "custom", "support")

	for _, name := range []string{"support", "Support", "SUPPORT"} {
		_, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
			TeamID: team.ID, Provider: "slack", Name: name,
		})
		wantPGCode(t, err, uniqueViolation, "a second channel called "+name)
	}
}

// The uniqueness is per team, not global. Two customers both calling their
// channel "support" is the normal case.
func TestTwoTeamsMayEachHaveAChannelCalledSupport(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	platform := mustCreate(t, s, "platform")
	finance := mustCreate(t, s, "finance")

	mustCreateChannel(t, s, platform.ID, "custom", "support")
	mustCreateChannel(t, s, finance.ID, "custom", "support")
}

// provider is matched byte for byte against the adapter registry and appears
// in the hook URL, so a stored "Slack" is a channel that routes nowhere and
// reports nothing.
func TestAProviderMustBeLowerCase(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	_, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: team.ID, Provider: "Slack", Name: "support",
	})
	wantPGCode(t, err, checkViolation, "a provider with a capital letter")
}

func TestAChannelNeedsAProviderAndAName(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for what, params := range map[string]db.CreateChannelParams{
		"no provider": {TeamID: team.ID, Provider: "", Name: "support"},
		"no name":     {TeamID: team.ID, Provider: "custom", Name: ""},
	} {
		_, err := s.CreateChannel(t.Context(), params)
		wantPGCode(t, err, checkViolation, "a channel with "+what)
	}
}

// The webhook handler knows only what is in the URL. The row is what tells it
// which team the channel belongs to, so the lookup cannot require one.
func TestGetChannelNeedsOnlyTheID(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	got, err := s.GetChannel(t.Context(), ch.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.TeamID != team.ID || got.Provider != "custom" || got.Name != "support" {
		t.Errorf("GetChannel = %+v, want the channel we created", got)
	}
}

func TestGetChannelDoesNotFindADeletedChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if err := s.DeleteChannel(t.Context(), ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if _, err := s.GetChannel(t.Context(), ch.ID); err == nil {
		t.Error("GetChannel found a deleted channel")
	}
}

// The whole point of the query taking a team. A leak here would show one
// team's channel ids to another, and a channel id is the routing key in a
// hook URL.
func TestListChannelsByTeamShowsOnlyThatTeam(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	platform := mustCreate(t, s, "platform")
	finance := mustCreate(t, s, "finance")

	mine := mustCreateChannel(t, s, platform.ID, "custom", "support")
	mustCreateChannel(t, s, finance.ID, "custom", "billing")

	got, err := s.ListChannelsByTeam(t.Context(), db.ListChannelsByTeamParams{
		TeamID: platform.ID, PageSize: 1000,
	})
	if err != nil {
		t.Fatalf("ListChannelsByTeam: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d channels, want only the one in platform: %+v", len(got), got)
	}
	if got[0].ID != mine.ID {
		t.Errorf("got channel %s, want %s", got[0].ID, mine.ID)
	}
}

func TestListChannelsByTeamReturnsNewestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	now := time.Now()
	oldest := mustCreateChannel(t, s, team.ID, "custom", "oldest")
	middle := mustCreateChannel(t, s, team.ID, "custom", "middle")
	newest := mustCreateChannel(t, s, team.ID, "custom", "newest")

	// Three inserts in one millisecond is not hypothetical, so the order is
	// made explicit rather than left to how fast the machine is.
	backdateChannel(t, s, oldest.ID, now.Add(-3*time.Hour))
	backdateChannel(t, s, middle.ID, now.Add(-2*time.Hour))
	backdateChannel(t, s, newest.ID, now.Add(-time.Hour))

	got, err := s.ListChannelsByTeam(t.Context(), db.ListChannelsByTeamParams{
		TeamID: team.ID, PageSize: 1000,
	})
	if err != nil {
		t.Fatalf("ListChannelsByTeam: %v", err)
	}

	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("got %d channels, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("order = %v, want %v", channelNames(got), want)
		}
	}
}

// The cursor is (created_at, id) so that channels sharing a timestamp still
// have one total order — otherwise a page boundary landing between two of
// them repeats one and skips another.
func TestListChannelsByTeamPagesFromACursor(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	now := time.Now()
	for i, name := range []string{"oldest", "middle", "newest"} {
		ch := mustCreateChannel(t, s, team.ID, "custom", name)
		backdateChannel(t, s, ch.ID, now.Add(time.Duration(i-3)*time.Hour))
	}

	first, err := s.ListChannelsByTeam(t.Context(), db.ListChannelsByTeamParams{
		TeamID: team.ID, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := channelNames(first); len(got) != 2 || got[0] != "newest" || got[1] != "middle" {
		t.Fatalf("first page = %v, want [newest middle]", got)
	}

	last := first[len(first)-1]
	second, err := s.ListChannelsByTeam(t.Context(), db.ListChannelsByTeamParams{
		TeamID:         team.ID,
		PageSize:       2,
		UseCursor:      true,
		AfterCreatedAt: last.CreatedAt,
		AfterID:        last.ID,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := channelNames(second); len(got) != 1 || got[0] != "oldest" {
		t.Fatalf("second page = %v, want [oldest]", got)
	}
}

func channelNames(channels []db.Channel) []string {
	names := make([]string, len(channels))
	for i, ch := range channels {
		names[i] = ch.Name
	}
	return names
}
