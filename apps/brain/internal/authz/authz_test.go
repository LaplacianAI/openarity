package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

// fakePermissions answers from a map and counts reads, so a test can prove
// both what Can decided and how much it asked.
type fakePermissions struct {
	actions map[string][]string
	err     error
	// errRole narrows err to one role, so a test can fail the read for one
	// membership while the others answer. An empty errRole fails every read.
	errRole string
	calls   int
	sawRole string
}

func (f *fakePermissions) ActionsFor(_ context.Context, role string) ([]string, error) {
	f.calls++
	f.sawRole = role
	if f.err != nil && (f.errRole == "" || f.errRole == role) {
		return nil, f.err
	}
	return f.actions[role], nil
}

// seeded is the permission set the migration ships with.
func seeded() *fakePermissions {
	// Strings, not Actions: this is what a table an administrator can edit
	// hands back, and it carries no guarantee of being a known action.
	return &fakePermissions{actions: map[string][]string{
		"admin":  {"agent:write", "tool:write", "channel:write", "membership:write"},
		"member": {"agent:write", "tool:write"},
	}}
}

func userIn(teamID uuid.UUID, role string) *auth.User {
	return &auth.User{
		ID:      uuid.New(),
		Issuer:  "https://idp.example.com",
		Subject: "user-42",
		Teams:   []auth.Membership{{TeamID: teamID, Name: "platform", Role: role}},
	}
}

// allowed fails the test if the answer is not a clean permit, and reports an
// error separately from a denial — the two must never be confused.
func allowed(t *testing.T, a *Authorizer, u *auth.User, action Action, r Resource) bool {
	t.Helper()

	ok, err := a.Can(t.Context(), u, action, r)
	if err != nil {
		t.Fatalf("Can returned an error: %v", err)
	}
	return ok
}

func TestCanAllowsAGrantedAction(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := New(seeded(), nil)

	if !allowed(t, a, userIn(team, "member"), ActionAgentWrite, Resource{TeamID: team}) {
		t.Error("a member was refused agent:write in their own team")
	}
}

func TestCanDeniesAnActionTheRoleDoesNotHave(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := New(seeded(), nil)

	if allowed(t, a, userIn(team, "member"), ActionMembershipWrite, Resource{TeamID: team}) {
		t.Error("a member was allowed membership:write")
	}
}

// A registered user with no memberships is the normal state after first login.
// They can do nothing until an administrator grants them something.
func TestCanDeniesAUserWithNoMemberships(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)
	fresh := &auth.User{ID: uuid.New(), Subject: "newcomer"}

	for _, action := range AllActions {
		if allowed(t, a, fresh, action, Resource{TeamID: uuid.New()}) {
			t.Errorf("a user with no memberships was allowed %q", action)
		}
	}
}

// Membership in one team must not carry into another. This is the check that
// keeps teams apart, so it is asserted with the strongest role available.
func TestCanDeniesInATeamTheUserIsNotIn(t *testing.T) {
	t.Parallel()

	mine, theirs := uuid.New(), uuid.New()
	a := New(seeded(), nil)

	if allowed(t, a, userIn(mine, "admin"), ActionAgentWrite, Resource{TeamID: theirs}) {
		t.Error("an admin of one team was allowed to act on another")
	}
}

// A user holds a different role in each team. The role that applies is the one
// for the team being acted on, not the first or the strongest.
func TestCanUsesTheRoleForTheTeamBeingActedOn(t *testing.T) {
	t.Parallel()

	alpha, bravo := uuid.New(), uuid.New()
	u := &auth.User{
		ID:      uuid.New(),
		Subject: "user-42",
		Teams: []auth.Membership{
			{TeamID: alpha, Name: "alpha", Role: "admin"},
			{TeamID: bravo, Name: "bravo", Role: "member"},
		},
	}
	a := New(seeded(), nil)

	if !allowed(t, a, u, ActionMembershipWrite, Resource{TeamID: alpha}) {
		t.Error("admin of alpha was refused membership:write in alpha")
	}
	if allowed(t, a, u, ActionMembershipWrite, Resource{TeamID: bravo}) {
		t.Error("admin of alpha was allowed membership:write in bravo, where they are a member")
	}
}

// The bootstrap path. The first super admin has no memberships — nobody has
// granted them any — so the check has to come before the membership lookup or
// there is no way to create the first team.
func TestCanAllowsASuperAdminWithNoMemberships(t *testing.T) {
	t.Parallel()

	a := New(seeded(), []string{"boss"})
	boss := &auth.User{ID: uuid.New(), Subject: "boss"}

	for _, action := range AllActions {
		if !allowed(t, a, boss, action, Resource{TeamID: uuid.New()}) {
			t.Errorf("a super admin was refused %q", action)
		}
	}
}

// A super admin needs no role, so the permission source must not be consulted.
// Reading it would make the bootstrap depend on seed data being present.
func TestCanDoesNotReadPermissionsForASuperAdmin(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, []string{"boss"})

	allowed(t, a, &auth.User{Subject: "boss"}, ActionAgentWrite, Resource{TeamID: uuid.New()})

	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a super admin", perms.calls)
	}
}

// The allowlist is exact. A prefix, a different case, or an empty subject must
// not match — an empty one especially, because a principal without a subject
// would otherwise be an administrator.
func TestSuperAdminMatchingIsExact(t *testing.T) {
	t.Parallel()

	a := New(seeded(), []string{"boss"})

	for _, subject := range []string{"bos", "bossy", "BOSS", "Boss", " boss", "boss ", ""} {
		u := &auth.User{ID: uuid.New(), Subject: subject}
		if allowed(t, a, u, ActionAgentWrite, Resource{TeamID: uuid.New()}) {
			t.Errorf("subject %q was treated as a super admin", subject)
		}
	}
}

// With no allowlist configured, nobody bypasses. An empty list must not mean
// "everybody" through a zero-value map lookup.
func TestNoSuperAdminsMeansNobodyBypasses(t *testing.T) {
	t.Parallel()

	for _, list := range [][]string{nil, {}} {
		a := New(seeded(), list)
		u := &auth.User{ID: uuid.New(), Subject: "anyone"}

		if allowed(t, a, u, ActionAgentWrite, Resource{TeamID: uuid.New()}) {
			t.Errorf("with super admins %v, an ordinary user bypassed authorisation", list)
		}
	}
}

// A failed permission read is "unknown", not "denied". The caller has to be
// able to tell them apart, because one is a 500 and the other a 403 — and a
// database blip that looks like a permissions problem costs somebody an
// afternoon checking roles.
func TestCanReportsAPermissionReadFailureAsAnError(t *testing.T) {
	t.Parallel()

	down := errors.New("connection refused")
	a := New(&fakePermissions{err: down}, nil)
	team := uuid.New()

	ok, err := a.Can(t.Context(), userIn(team, "admin"), ActionAgentWrite, Resource{TeamID: team})
	if err == nil {
		t.Fatal("a failed permission read was reported as a plain denial")
	}
	if ok {
		t.Error("Can allowed the action despite failing to read permissions")
	}
	if !errors.Is(err, down) {
		t.Errorf("the underlying failure was lost: %v", err)
	}
}

// The error has to name the role. "permission denied" with no context is
// unactionable when the cause is a missing row for one role.
func TestPermissionErrorNamesTheRole(t *testing.T) {
	t.Parallel()

	a := New(&fakePermissions{err: errors.New("boom")}, nil)
	team := uuid.New()

	_, err := a.Can(t.Context(), userIn(team, "release-manager"), ActionAgentWrite, Resource{TeamID: team})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "release-manager") {
		t.Errorf("error does not name the role: %v", err)
	}
}

// Not being in the team is decided from data already loaded, so it must not
// cost a read. A handler running three checks would otherwise pay three round
// trips to be told no three times.
func TestCanDoesNotReadPermissionsWhenTheUserIsNotInTheTeam(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, nil)

	allowed(t, a, userIn(uuid.New(), "admin"), ActionAgentWrite, Resource{TeamID: uuid.New()})

	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a user outside the team", perms.calls)
	}
}

func TestCanReadsPermissionsOncePerCall(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, nil)
	team := uuid.New()

	allowed(t, a, userIn(team, "admin"), ActionAgentWrite, Resource{TeamID: team})

	if perms.calls != 1 {
		t.Errorf("the permission source was read %d times, want 1", perms.calls)
	}
	if perms.sawRole != "admin" {
		t.Errorf("asked for role %q, want admin", perms.sawRole)
	}
}

// A role an administrator invented but never granted anything to permits
// nothing. It must not fall through to a default.
func TestCanDeniesARoleWithNoPermissions(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := New(seeded(), nil)

	for _, action := range AllActions {
		if allowed(t, a, userIn(team, "release-manager"), action, Resource{TeamID: team}) {
			t.Errorf("a role with no permissions allowed %q", action)
		}
	}
}

// Permissions are rows an administrator can edit, so the source can hand back
// a string that is not an Action at all. It must match nothing rather than
// being coerced into the vocabulary.
func TestCanIgnoresPermissionsThatAreNotActions(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := New(&fakePermissions{actions: map[string][]string{
		"typist": {"agent:wrote", "AGENT:WRITE", "agent write", "", "agent:write:x"},
	}}, nil)

	u := userIn(team, "typist")
	for _, action := range AllActions {
		if allowed(t, a, u, action, Resource{TeamID: team}) {
			t.Errorf("a permission row that is not an action allowed %q", action)
		}
	}
}

// Can is the security boundary, so a nil user is denied rather than a panic.
// Unreachable behind the middleware, and the one place being defensive is free.
func TestCanDeniesANilUser(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, []string{""})

	ok, err := a.Can(t.Context(), nil, ActionAgentWrite, Resource{TeamID: uuid.New()})
	if err != nil {
		t.Fatalf("Can returned an error for a nil user: %v", err)
	}
	if ok {
		t.Error("a nil user was allowed")
	}
	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a nil user", perms.calls)
	}
}

// An action nothing grants is denied. This is what stops a new constant being
// permitted by default before anyone seeds a permission for it.
func TestCanDeniesAnActionNobodyHolds(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := New(seeded(), nil)

	for _, action := range []Action{"", "agent:delete", "AGENT:WRITE", "agent:write "} {
		if allowed(t, a, userIn(team, "admin"), action, Resource{TeamID: team}) {
			t.Errorf("an admin was allowed %q, which nothing grants", action)
		}
	}
}

// The zero team is a uuid like any other, and no membership names it. A
// Resource left unset must not become a wildcard.
func TestCanDeniesTheZeroTeam(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)

	if allowed(t, a, userIn(uuid.New(), "admin"), ActionAgentWrite, Resource{}) {
		t.Error("an unset Resource authorised an action")
	}
}

// IsSuperAdmin is exported and handlers call it directly, so it cannot rely on
// Can's nil check upstream. Creating a team is the platform-level decision, and
// a panic there is a 500 with no reply body.
func TestIsSuperAdminDeniesANilUser(t *testing.T) {
	t.Parallel()

	a := New(seeded(), []string{"boss"})
	if a.IsSuperAdmin(nil) {
		t.Error("a nil user was reported as a super admin")
	}
}

func TestIsSuperAdminMatchesOnlyTheAllowlist(t *testing.T) {
	t.Parallel()

	a := New(seeded(), []string{"boss"})

	if !a.IsSuperAdmin(&auth.User{Subject: "boss"}) {
		t.Error("a listed subject was not reported as a super admin")
	}
	for _, subject := range []string{"bos", "bossy", "BOSS", " boss", ""} {
		if a.IsSuperAdmin(&auth.User{Subject: subject}) {
			t.Errorf("subject %q was reported as a super admin", subject)
		}
	}
}

// With no allowlist, nobody holds the platform role — an empty map lookup must
// not read as a match.
func TestIsSuperAdminWithNoAllowlist(t *testing.T) {
	t.Parallel()

	for _, list := range [][]string{nil, {}} {
		a := New(seeded(), list)
		if a.IsSuperAdmin(&auth.User{Subject: "anyone"}) {
			t.Errorf("with super admins %v, an ordinary user held the platform role", list)
		}
	}
}

// CanInAnyTeam answers the same question without a team. Everything below is
// about the two ways that can go wrong: being weaker than it should be, and
// being confused with Can by a future caller.

// userWith builds one membership per role, each in its own team. The team ids
// are what CanInAnyTeam must ignore.
func userWith(roles ...string) *auth.User {
	u := &auth.User{ID: uuid.New(), Subject: "user-42"}
	for _, role := range roles {
		u.Teams = append(u.Teams, auth.Membership{TeamID: uuid.New(), Name: "team", Role: role})
	}
	return u
}

func allowedAnywhere(t *testing.T, a *Authorizer, u *auth.User, action Action) bool {
	t.Helper()

	ok, err := a.CanInAnyTeam(t.Context(), u, action)
	if err != nil {
		t.Fatalf("CanInAnyTeam returned an error: %v", err)
	}
	return ok
}

// The whole reason it exists: one qualifying membership is enough, and the
// caller never says which team they meant.
func TestCanInAnyTeamAllowsWhenOneMembershipGrantsIt(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)

	if !allowedAnywhere(t, a, userWith("member", "admin"), ActionMembershipWrite) {
		t.Error("an admin of one team was refused membership:write anywhere")
	}
}

// The position of the qualifying membership must not matter. A loop that
// returns on the first role's answer rather than continuing passes the test
// above and fails this one.
func TestCanInAnyTeamDoesNotDependOnMembershipOrder(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)

	for _, roles := range [][]string{
		{"admin", "member"},
		{"member", "admin"},
		{"member", "member", "admin"},
		{"admin"},
	} {
		if !allowedAnywhere(t, a, userWith(roles...), ActionMembershipWrite) {
			t.Errorf("roles %v were refused membership:write", roles)
		}
	}
}

// Being in twenty teams with a role that does not grant it is still no.
func TestCanInAnyTeamDeniesWhenNoMembershipGrantsIt(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)
	roles := make([]string, 20)
	for i := range roles {
		roles[i] = "member"
	}

	if allowedAnywhere(t, a, userWith(roles...), ActionMembershipWrite) {
		t.Error("a member of twenty teams was allowed membership:write")
	}
}

// The state right after a first login. Nothing has been granted, so the
// directory is not readable.
func TestCanInAnyTeamDeniesAUserWithNoMemberships(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, nil)
	fresh := &auth.User{ID: uuid.New(), Subject: "newcomer"}

	for _, action := range AllActions {
		if allowedAnywhere(t, a, fresh, action) {
			t.Errorf("a user with no memberships was allowed %q anywhere", action)
		}
	}
	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a user with no memberships", perms.calls)
	}
}

// Same bootstrap problem as Can: a super admin holds no membership rows, so
// the loop would find nothing and the first team could never be populated.
func TestCanInAnyTeamAllowsASuperAdminWithNoMemberships(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, []string{"boss"})
	boss := &auth.User{ID: uuid.New(), Subject: "boss"}

	for _, action := range AllActions {
		if !allowedAnywhere(t, a, boss, action) {
			t.Errorf("a super admin was refused %q", action)
		}
	}
	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a super admin", perms.calls)
	}
}

func TestCanInAnyTeamDeniesANilUser(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, []string{""})

	ok, err := a.CanInAnyTeam(t.Context(), nil, ActionAgentWrite)
	if err != nil {
		t.Fatalf("CanInAnyTeam returned an error for a nil user: %v", err)
	}
	if ok {
		t.Error("a nil user was allowed")
	}
	if perms.calls != 0 {
		t.Errorf("the permission source was read %d times for a nil user", perms.calls)
	}
}

// The dangerous direction. If a failed read were skipped rather than returned,
// this user's admin membership would be found afterwards and the answer would
// be a permit produced by a database error.
func TestCanInAnyTeamReportsAReadFailureRatherThanContinuing(t *testing.T) {
	t.Parallel()

	down := errors.New("connection refused")
	perms := seeded()
	perms.err, perms.errRole = down, "member"
	a := New(perms, nil)

	ok, err := a.CanInAnyTeam(t.Context(), userWith("member", "admin"), ActionMembershipWrite)
	if err == nil {
		t.Fatal("a failed permission read was reported as a plain answer")
	}
	if ok {
		t.Error("a failed permission read produced a permit")
	}
	if !errors.Is(err, down) {
		t.Errorf("the underlying failure was lost: %v", err)
	}
}

func TestCanInAnyTeamNamesTheRoleInAnError(t *testing.T) {
	t.Parallel()

	a := New(&fakePermissions{err: errors.New("boom")}, nil)

	_, err := a.CanInAnyTeam(t.Context(), userWith("release-manager"), ActionAgentWrite)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "release-manager") {
		t.Errorf("error does not name the role: %v", err)
	}
}

// Twenty memberships are usually two roles. Asking per membership rather than
// per distinct role turns one directory lookup into twenty round trips, and
// nothing about the answer would change.
func TestCanInAnyTeamAsksOncePerDistinctRole(t *testing.T) {
	t.Parallel()

	perms := seeded()
	a := New(perms, nil)

	roles := make([]string, 0, 20)
	for range 10 {
		roles = append(roles, "member", "reviewer")
	}

	if allowedAnywhere(t, a, userWith(roles...), ActionMembershipWrite) {
		t.Fatal("neither role grants membership:write")
	}
	if perms.calls != 2 {
		t.Errorf("the permission source was read %d times for 2 distinct roles across 20 teams", perms.calls)
	}
}

// A role an administrator invented and granted nothing to permits nothing,
// however many teams it is held in.
func TestCanInAnyTeamDeniesARoleWithNoPermissions(t *testing.T) {
	t.Parallel()

	a := New(seeded(), nil)

	for _, action := range AllActions {
		if allowedAnywhere(t, a, userWith("release-manager", "release-manager"), action) {
			t.Errorf("a role with no permissions allowed %q", action)
		}
	}
}

// CanInAnyTeam is strictly weaker than Can, and that is the property a future
// caller has to be unable to get wrong. Where Can denies for a specific team,
// CanInAnyTeam may still permit — so the two are never interchangeable.
func TestCanInAnyTeamIsWeakerThanCan(t *testing.T) {
	t.Parallel()

	alpha, bravo := uuid.New(), uuid.New()
	u := &auth.User{
		ID:      uuid.New(),
		Subject: "user-42",
		Teams: []auth.Membership{
			{TeamID: alpha, Name: "alpha", Role: "admin"},
			{TeamID: bravo, Name: "bravo", Role: "member"},
		},
	}
	a := New(seeded(), nil)

	if allowed(t, a, u, ActionMembershipWrite, Resource{TeamID: bravo}) {
		t.Error("Can allowed membership:write in a team where the user is only a member")
	}
	if !allowedAnywhere(t, a, u, ActionMembershipWrite) {
		t.Error("CanInAnyTeam refused what the user holds in alpha")
	}
}
