package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// bootstrapStore is queryStore with the policy turned on. Setting the field
// rather than threading an Option through the fixture keeps this a change of
// one line against the store every other query test uses — the Option itself is
// covered by TestWithFirstUserBootstrapSetsThePolicy, which needs no database.
func bootstrapStore(t *testing.T) *Store {
	t.Helper()

	s := queryStore(t)
	s.firstUserBootstrap = true
	return s
}

func resolve(t *testing.T, s *Store, subject string) *auth.User {
	t.Helper()

	user, err := s.Resolve(t.Context(), principal(subject, subject+"@example.com"))
	if err != nil {
		t.Fatalf("Resolve(%s): %v", subject, err)
	}
	return user
}

func TestWithFirstUserBootstrapSetsThePolicy(t *testing.T) {
	t.Parallel()

	for _, enabled := range []bool{true, false} {
		var s Store
		WithFirstUserBootstrap(enabled)(&s)
		if s.firstUserBootstrap != enabled {
			t.Errorf("WithFirstUserBootstrap(%t) left the field %t", enabled, s.firstUserBootstrap)
		}
	}
}

// A store built without the option must not promote, which is the property
// that keeps every existing deployment and every existing test unchanged.
func TestNewDoesNotEnableTheBootstrapByDefault(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	if s.firstUserBootstrap {
		t.Fatal("a store built without the option has the bootstrap enabled")
	}

	user := resolve(t, s, "nobody")
	if user.SuperAdmin {
		t.Error("promoted a user at a store with the bootstrap disabled")
	}

	owned, err := s.AnySuperAdmin(t.Context())
	if err != nil {
		t.Fatalf("AnySuperAdmin: %v", err)
	}
	if owned {
		t.Error("the install has a super admin after a login it should not have promoted")
	}
}

func TestTheFirstLoginClaimsAnUnownedInstall(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	first := resolve(t, s, "alice")
	if !first.SuperAdmin {
		t.Fatal("the first login at an unowned install was not promoted")
	}

	// The grant is a row, not a decision made in memory: a second Resolve of
	// the same subject must still report it.
	again := resolve(t, s, "alice")
	if !again.SuperAdmin {
		t.Error("the grant did not survive a second resolve, so it was never written")
	}
}

func TestTheSecondLoginIsNotPromoted(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	if first := resolve(t, s, "alice"); !first.SuperAdmin {
		t.Fatal("the first login was not promoted, so this test proves nothing")
	}

	second := resolve(t, s, "bob")
	if second.SuperAdmin {
		t.Error("bob was promoted at an install alice already owns")
	}
}

// The guard is "no super admin exists", not "no users exist". If it were the
// latter, an ordinary member arriving first would close the window forever and
// leave a database nobody can administer.
func TestAnOrdinaryUserArrivingFirstDoesNotCloseTheWindow(t *testing.T) {
	t.Parallel()

	s := queryStore(t)

	// Arrives while the policy is off — a real install where the operator had
	// not yet set the flag, or simply had not logged in yet.
	if early := resolve(t, s, "bob"); early.SuperAdmin {
		t.Fatal("bob was promoted with the bootstrap disabled")
	}

	s.firstUserBootstrap = true

	owner := resolve(t, s, "alice")
	if !owner.SuperAdmin {
		t.Error("the install stayed unclaimable after an ordinary user logged in first")
	}
}

// Erasing the only super admin leaves the install unowned, and the next login
// claims it. Documented behaviour rather than an accident: being locked out of
// a single-user machine forever is the worse failure.
func TestErasingTheOnlyAdminReopensTheWindow(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	owner := resolve(t, s, "alice")
	if !owner.SuperAdmin {
		t.Fatal("the first login was not promoted")
	}

	if _, err := s.pool.Exec(t.Context(), "DELETE FROM users WHERE id = $1", owner.ID); err != nil {
		t.Fatalf("delete the owner: %v", err)
	}

	reclaimed := resolve(t, s, "bob")
	if !reclaimed.SuperAdmin {
		t.Error("the install stayed unowned after its only admin was erased")
	}
}

// Two logins arriving together must produce exactly one admin. Without the
// advisory lock both read "no super admin" and both promote, because they
// update different rows and so never contend for a row lock.
func TestConcurrentFirstLoginsPromoteExactlyOne(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	const racers = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		users []*auth.User
		errs  []error
	)

	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			user, err := s.Resolve(t.Context(), principal(subjectOf(i), subjectOf(i)+"@example.com"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			users = append(users, user)
		}()
	}
	close(start)
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("concurrent Resolve: %v", err)
	}

	promoted := 0
	for _, u := range users {
		if u.SuperAdmin {
			promoted++
		}
	}
	if promoted != 1 {
		t.Errorf("%d of %d concurrent logins were promoted, want exactly 1", promoted, len(users))
	}

	// And the database agrees with what Resolve reported.
	var rows int
	if err := s.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM users WHERE is_super_admin").Scan(&rows); err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows hold the grant, want 1", rows)
	}
}

func subjectOf(i int) string {
	return string(rune('a' + i))
}

// Resolve must keep doing its original job — the promotion is an addition to
// that transaction, not a replacement for it.
func TestResolveStillReturnsMembershipsWhileBootstrapping(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	team := mustCreate(t, s, "platform")
	row := upsert(t, s, testIssuer, "alice", ptr("alice@example.com"))
	addMember(t, s, team.ID, row.ID, "admin")

	user := resolve(t, s, "alice")
	if !user.SuperAdmin {
		t.Error("the existing user was not promoted at an unowned install")
	}
	if len(user.Teams) != 1 || user.Teams[0].Name != "platform" {
		t.Errorf("memberships = %+v, want one membership of platform", user.Teams)
	}
	if user.Email == nil || *user.Email != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", user.Email)
	}
}

// PromoteToSuperAdmin is the only writer of the column, so it must be the only
// thing that can set it — an upsert of an existing admin must not clear it.
func TestUpsertDoesNotClearAnExistingGrant(t *testing.T) {
	t.Parallel()

	s := bootstrapStore(t)

	owner := resolve(t, s, "alice")
	if !owner.SuperAdmin {
		t.Fatal("the first login was not promoted")
	}

	row, err := s.UpsertUser(t.Context(), db.UpsertUserParams{
		Issuer: testIssuer, Subject: "alice", Email: ptr("alice@elsewhere.example"),
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if !row.IsSuperAdmin {
		t.Error("re-upserting an admin cleared the grant")
	}
}
