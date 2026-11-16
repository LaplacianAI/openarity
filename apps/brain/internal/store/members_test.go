package store

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type teamWithChannel struct {
	team    db.Team
	channel db.Channel
}

func newTeamWithChannel(t *testing.T, s *Store) teamWithChannel {
	t.Helper()
	team := mustCreate(t, s, "members-"+uuid.NewString()[:8])
	return teamWithChannel{team: team, channel: mustCreateChannel(t, s, team.ID, "whatsapp", "mine")}
}

func mustJoin(t *testing.T, s *Store, teamID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := s.AddTeamMember(t.Context(), db.AddTeamMemberParams{TeamID: teamID, UserID: userID, Role: role}); err != nil {
		t.Fatalf("AddTeamMember(%s): %v", role, err)
	}
}

func approveNew(t *testing.T, s *Store, ch db.Channel, ref string, holder *uuid.UUID) db.ApproveSenderAsNewRow {
	t.Helper()
	row, err := s.BindSenderAsNew(t.Context(), db.ApproveSenderAsNewParams{
		ChannelID: ch.ID, SenderRef: ref, Name: "Ravi", Role: "guest", ExpectedHolder: holder,
	})
	if err != nil {
		t.Fatalf("BindSenderAsNew(%q): %v", ref, err)
	}
	return row
}

func boundTo(t *testing.T, s *Store, ch db.Channel, ref string) uuid.UUID {
	t.Helper()
	id, err := s.FindChannelSender(t.Context(), db.FindChannelSenderParams{ChannelID: ch.ID, SenderRef: ref})
	if err != nil {
		t.Fatalf("FindChannelSender(%q): %v", ref, err)
	}
	return id
}

func userExists(t *testing.T, s *Store, id uuid.UUID) bool {
	t.Helper()
	return count(t, s, `SELECT count(*) FROM users WHERE id = $1`, id) == 1
}

// One approval makes the whole person: a user of their own that no login
// reaches, a guest of the team, and the binding, with the sender out of the
// queue.
func TestApprovingSomeoneNewMakesAUserAMemberAndABinding(t *testing.T) {
	s := queryStore(t)
	f := newTeamWithChannel(t, s)
	record(t, s, f.channel.ID, "919800000001", "Ravi K")

	row := approveNew(t, s, f.channel, "919800000001", nil)

	if row.Linked != 1 || boundTo(t, s, f.channel, "919800000001") != row.UserID {
		t.Fatalf("linked %d, bound to %s; want the new user %s", row.Linked, boundTo(t, s, f.channel, "919800000001"), row.UserID)
	}
	if n := count(t, s, `SELECT count(*) FROM users WHERE id = $1 AND issuer = 'openarity:channel'
		AND subject = $2 AND name = 'Ravi' AND email IS NULL`, row.UserID, f.channel.ID.String()+":919800000001"); n != 1 {
		t.Error("the user is not a channel user keyed on the channel and the ref, named Ravi")
	}
	if role, err := s.FindTeamMember(t.Context(), db.FindTeamMemberParams{TeamID: f.team.ID, UserID: row.UserID}); err != nil || role != "guest" {
		t.Errorf("role %q, %v; want guest", role, err)
	}
	if len(pending(t, s, f.channel.ID)) != 0 {
		t.Error("the sender is still waiting")
	}
}

func TestApprovingTheSameSenderAgainChangesNothing(t *testing.T) {
	s := queryStore(t)
	f := newTeamWithChannel(t, s)
	first := approveNew(t, s, f.channel, "919800000001", nil)
	users := count(t, s, `SELECT count(*) FROM users`)

	again := approveNew(t, s, f.channel, "919800000001", nil)

	if again.Linked != 1 || again.UserID != first.UserID {
		t.Errorf("again: linked %d, user %s; want the same user, still linked", again.Linked, again.UserID)
	}
	if n := count(t, s, `SELECT count(*) FROM users`); n != users {
		t.Errorf("%d users after approving again, want %d", n, users)
	}
}

// A sender already speaking as a member is not taken by "someone new"
// unless the caller was checked against that member.
func TestSomeoneNewDoesNotTakeABindingNobodyChecked(t *testing.T) {
	s := queryStore(t)
	f := newTeamWithChannel(t, s)
	member := mustCreateUser(t, s, "ann")
	mustJoin(t, s, f.team.ID, member, "member")
	if _, err := s.BindSender(t.Context(), db.ApproveSenderParams{ChannelID: f.channel.ID, SenderRef: "919800000001", UserID: member}); err != nil {
		t.Fatal(err)
	}

	if row := approveNew(t, s, f.channel, "919800000001", nil); row.Linked != 0 {
		t.Fatalf("linked %d, want 0: the binding was taken unchecked", row.Linked)
	}
	if boundTo(t, s, f.channel, "919800000001") != member {
		t.Error("the refused approval moved the binding")
	}

	if row := approveNew(t, s, f.channel, "919800000001", &member); row.Linked != 1 || boundTo(t, s, f.channel, "919800000001") != row.UserID {
		t.Fatal("checked against its holder, the binding did not move")
	}
	if !userExists(t, s, member) {
		t.Error("a user who logs in was deleted for losing a binding")
	}
}

// Nobody logs in as a channel user, so one whose last sender moved away, or
// was removed, is gone, not left as a member who can do nothing.
func TestAChannelUserLeftWithNoSenderIsDeleted(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Store, f teamWithChannel, guest uuid.UUID){
		"moved to a member": func(t *testing.T, s *Store, f teamWithChannel, guest uuid.UUID) {
			member := mustCreateUser(t, s, "ann")
			mustJoin(t, s, f.team.ID, member, "member")
			if row, err := s.BindSender(t.Context(), db.ApproveSenderParams{
				ChannelID: f.channel.ID, SenderRef: "919800000001", UserID: member, ExpectedHolder: &guest,
			}); err != nil || row.Linked != 1 {
				t.Fatalf("BindSender: %v, %+v", err, row)
			}
		},
		"removed": func(t *testing.T, s *Store, f teamWithChannel, guest uuid.UUID) {
			if removed, err := s.UnbindSender(t.Context(), db.RemoveSenderParams{
				ChannelID: f.channel.ID, SenderRef: "919800000001", ExpectedHolder: &guest,
			}); err != nil || !removed {
				t.Fatalf("UnbindSender: %v, %v", err, removed)
			}
		},
		"removed from the team": func(t *testing.T, s *Store, f teamWithChannel, guest uuid.UUID) {
			if err := s.RemoveMember(t.Context(), f.team.ID, guest); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := queryStore(t)
			f := newTeamWithChannel(t, s)
			guest := approveNew(t, s, f.channel, "919800000001", nil).UserID

			change(t, s, f, guest)

			if userExists(t, s, guest) {
				t.Error("the channel user is still there with no sender")
			}
		})
	}
}

// "This is Ravi" on a second channel binds a second sender to one channel
// user. Losing one of them leaves Ravi.
func TestAChannelUserWithAnotherSenderIsKept(t *testing.T) {
	s := queryStore(t)
	f := newTeamWithChannel(t, s)
	guest := approveNew(t, s, f.channel, "919800000001", nil).UserID
	other := mustCreateChannel(t, s, f.team.ID, "whatsapp", "other")
	if _, err := s.BindSender(t.Context(), db.ApproveSenderParams{ChannelID: other.ID, SenderRef: "919800000001", UserID: guest}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UnbindSender(t.Context(), db.RemoveSenderParams{ChannelID: f.channel.ID, SenderRef: "919800000001", ExpectedHolder: &guest}); err != nil {
		t.Fatal(err)
	}

	if !userExists(t, s, guest) {
		t.Error("a channel user still speaking on another channel was deleted")
	}
}

func TestARemovedLoginUserIsKept(t *testing.T) {
	s := queryStore(t)
	f := newTeamWithChannel(t, s)
	member := mustCreateUser(t, s, "ann")
	mustJoin(t, s, f.team.ID, member, "member")

	if err := s.RemoveMember(t.Context(), f.team.ID, member); err != nil {
		t.Fatal(err)
	}
	if !userExists(t, s, member) {
		t.Error("removing a user who logs in from a team deleted them")
	}
}

const beforeChannelUsers = 20261116120000

func TestChannelUsersRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mustCreateUser(t, s, "kept")

	downTo(t, s, beforeChannelUsers)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'name'`); n != 0 {
		t.Error("rolled back, users still has name")
	}
	if n := count(t, s, `SELECT count(*) FROM users WHERE subject = 'kept'`); n != 1 {
		t.Errorf("rolling back lost the user: %d left", n)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
