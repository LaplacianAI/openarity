package teams

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func patchMember(t *testing.T, s *fakeStore, a *fakeAuthz, team, user uuid.UUID, body string) int {
	t.Helper()
	return call(t, s, a, memberOf(team, "admin"), http.MethodPatch,
		fmt.Sprintf("/teams/%s/members/%s", team, user), body).Code
}

func TestARoleAndANameAreChanged(t *testing.T) {
	t.Parallel()
	team, user := uuid.New(), uuid.New()
	s := &fakeStore{}

	if code := patchMember(t, s, &fakeAuthz{allowed: true}, team, user, `{"role":" member ","name":" Ravi "}`); code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	if len(s.updated) != 1 {
		t.Fatalf("%d updates, want 1", len(s.updated))
	}
	got := s.updated[0]
	if got.team != team || got.user != user || *got.change.Role != "member" || *got.change.Name != "Ravi" {
		t.Errorf("update %+v, role %q, name %q; want the member, trimmed", got, *got.change.Role, *got.change.Name)
	}
}

// Every refusal is decided before the store, or by it, and changes nothing.
func TestAMemberChangeIsRefusedForWhatItIs(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body     string
		storeErr error
		want     int
	}{
		"nothing to change":       {`{}`, nil, http.StatusBadRequest},
		"an empty role":           {`{"role":"  "}`, nil, http.StatusBadRequest},
		"an empty name":           {`{"name":""}`, nil, http.StatusBadRequest},
		"a name too long":         {`{"name":"` + strings.Repeat("r", 65) + `"}`, nil, http.StatusBadRequest},
		"the last admin":          {`{"role":"member"}`, ErrLastAdmin, http.StatusConflict},
		"not in the team":         {`{"role":"member"}`, ErrNotMember, http.StatusNotFound},
		"a login user's name":     {`{"name":"Annie"}`, ErrNotChannelUser, http.StatusBadRequest},
		"a role nobody defined":   {`{"role":"owner"}`, &pgconn.PgError{ConstraintName: "team_members_role_fkey"}, http.StatusBadRequest},
		"the store failing":       {`{"role":"member"}`, errors.New("connection reset"), http.StatusInternalServerError},
		"a body that is not JSON": {`{`, nil, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := &fakeStore{updateErr: tc.storeErr}
			if code := patchMember(t, s, &fakeAuthz{allowed: true}, uuid.New(), uuid.New(), tc.body); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
			if len(s.updated) != 0 {
				t.Error("a refused change reached the store")
			}
		})
	}
}

func TestAMemberChangeNeedsMembershipWrite(t *testing.T) {
	t.Parallel()
	s := &fakeStore{}
	a := &fakeAuthz{allowed: false}

	if code := patchMember(t, s, a, uuid.New(), uuid.New(), `{"role":"admin"}`); code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
	if len(s.updated) != 0 {
		t.Error("the store was written to anyway")
	}
}

func TestAMemberChangeNamesTheUserByUUID(t *testing.T) {
	t.Parallel()
	team := uuid.New()
	rec := call(t, &fakeStore{}, &fakeAuthz{allowed: true}, memberOf(team, "admin"), http.MethodPatch,
		"/teams/"+team.String()+"/members/ravi", `{"role":"member"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
