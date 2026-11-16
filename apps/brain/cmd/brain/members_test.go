package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teams"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type memberWorld struct {
	store teamStore
	team  uuid.UUID
	dsn   string
	t     *testing.T
}

func newMemberWorld(t *testing.T) *memberWorld {
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
	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatal(err)
	}
	return &memberWorld{store: teamStore{s}, team: team.ID, dsn: dsn, t: t}
}

func (w *memberWorld) user(issuer, subject, role string) uuid.UUID {
	w.t.Helper()
	u, err := w.store.UpsertUser(w.t.Context(), db.UpsertUserParams{Issuer: issuer, Subject: subject})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.store.AddTeamMember(w.t.Context(), db.AddTeamMemberParams{TeamID: w.team, UserID: u.ID, Role: role}); err != nil {
		w.t.Fatal(err)
	}
	return u.ID
}

func (w *memberWorld) role(id uuid.UUID) string {
	w.t.Helper()
	role, err := w.store.FindTeamMember(w.t.Context(), db.FindTeamMemberParams{TeamID: w.team, UserID: id})
	if err != nil {
		w.t.Fatal(err)
	}
	return role
}

func ptr(s string) *string { return &s }

func TestARoleAndAChannelUsersNameAreChangedInPlace(t *testing.T) {
	w := newMemberWorld(t)
	guest := w.user("openarity:channel", uuid.NewString()+":919800000001", "guest")

	if err := w.store.UpdateMember(t.Context(), w.team, guest, teams.MemberChange{Role: ptr("member"), Name: ptr("Ravi Kumar")}); err != nil {
		t.Fatal(err)
	}

	if w.role(guest) != "member" {
		t.Errorf("role %q, want member", w.role(guest))
	}
	members, err := w.store.ListTeamMembers(t.Context(), db.ListTeamMembersParams{TeamID: w.team, PageSize: 10})
	if err != nil || len(members) != 1 || members[0].Name == nil || *members[0].Name != "Ravi Kumar" || !members[0].ChannelUser {
		t.Errorf("members = %+v, %v; want Ravi Kumar, a channel user", members, err)
	}
}

func TestTheLastAdminKeepsTheirRole(t *testing.T) {
	w := newMemberWorld(t)
	admin := w.user("https://id.test", "ann", "admin")

	err := w.store.UpdateMember(t.Context(), w.team, admin, teams.MemberChange{Role: ptr("member")})

	if !errors.Is(err, teams.ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}
	if w.role(admin) != "admin" {
		t.Error("the refusal still demoted the last admin")
	}
}

// Two admins demoting each other at once: counted without the lock, each
// sees the other still there, and the team is left with none.
func TestTwoAdminsDemotedAtOnceLeaveOne(t *testing.T) {
	w := newMemberWorld(t)
	a, b := w.user("https://id.test", "ann", "admin"), w.user("https://id.test", "bo", "admin")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []uuid.UUID{a, b} {
		wg.Go(func() {
			errs[i] = w.store.UpdateMember(t.Context(), w.team, id, teams.MemberChange{Role: ptr("member")})
		})
	}
	wg.Wait()

	admins, err := w.store.CountTeamAdmins(t.Context(), w.team)
	if err != nil || admins != 1 {
		t.Fatalf("%d admins left, %v; want 1. errors %v", admins, err, errs)
	}
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Errorf("errors %v, want exactly one ErrLastAdmin", errs)
	}
}

func TestOnlyAChannelUserIsRenamed(t *testing.T) {
	w := newMemberWorld(t)
	member := w.user("https://id.test", "ann", "member")

	err := w.store.UpdateMember(t.Context(), w.team, member, teams.MemberChange{Role: ptr("approver"), Name: ptr("Annie")})

	if !errors.Is(err, teams.ErrNotChannelUser) {
		t.Fatalf("err = %v, want ErrNotChannelUser", err)
	}
	if w.role(member) != "member" {
		t.Error("the refused change still changed the role")
	}
}

func TestChangingSomeoneOutsideTheTeamIsRefused(t *testing.T) {
	w := newMemberWorld(t)
	u, err := w.store.UpsertUser(t.Context(), db.UpsertUserParams{Issuer: "https://id.test", Subject: "stranger"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.UpdateMember(t.Context(), w.team, u.ID, teams.MemberChange{Role: ptr("member")}); !errors.Is(err, teams.ErrNotMember) {
		t.Fatalf("err = %v, want ErrNotMember", err)
	}
}

// The lock is what makes the count true: a change waits for any other one
// to the same team's roles, rather than counting admins it cannot see.
func TestAMemberChangeWaitsForTheTeamsLock(t *testing.T) {
	w := newMemberWorld(t)
	admin := w.user("https://id.test", "ann", "admin")
	w.user("https://id.test", "bo", "admin")

	holder, err := pgx.Connect(t.Context(), w.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close(t.Context()) }()
	tx, err := holder.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM teams WHERE id = $1 FOR UPDATE`, w.team); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- w.store.UpdateMember(t.Context(), w.team, admin, teams.MemberChange{Role: ptr("member")})
	}()

	// Waiting is seen in Postgres, not guessed from a sleep: the change's
	// backend is blocked on a lock while the holder's transaction is open.
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("the change finished while the team was locked: %v", err)
		default:
		}
		var waiting int
		if err := holder.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '%FROM teams WHERE id = $1 FOR UPDATE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the change neither waited for the lock nor finished")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
}
