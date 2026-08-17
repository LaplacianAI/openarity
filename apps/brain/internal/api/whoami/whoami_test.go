package whoami

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ptr(s string) *string { return &s }

// call drives one request through the registered route with a principal and
// user already on the context, exactly as the middleware chain leaves them.
func call(t *testing.T, method string, p *auth.Principal, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger()).Register(mux)

	req := httptest.NewRequestWithContext(t.Context(), method, "/whoami", nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, p *auth.Principal, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, http.MethodGet, p, u)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) whoamiResponse {
	t.Helper()

	var got whoamiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON (%v): %s", err, rec.Body)
	}
	return got
}

func testPrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.KindUser, Issuer: "https://idp", Subject: "user-42"}
}

func TestWhoamiReturnsTheResolvedCaller(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	u := &auth.User{
		ID: uuid.New(), Issuer: "https://idp", Subject: "user-42", Email: ptr("a@example.com"),
		Teams: []auth.Membership{{TeamID: team, Name: "platform", Role: "admin"}},
	}

	rec := get(t, testPrincipal(), u)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := decode(t, rec)
	if got.Kind != "user" || got.Subject != "user-42" || got.Issuer != "https://idp" {
		t.Errorf("identity = %+v", got)
	}
	if got.Email == nil || *got.Email != "a@example.com" {
		t.Errorf("Email = %v", got.Email)
	}
	if len(got.Teams) != 1 || got.Teams[0].ID != team ||
		got.Teams[0].Name != "platform" || got.Teams[0].Role != "admin" {
		t.Errorf("Teams = %+v", got.Teams)
	}
}

// A registered user with no access is a real state and the CLI has to say so.
// An empty array means "we asked and there are none"; a missing key is
// indistinguishable from a server that predates the field.
func TestWhoamiReportsNoTeamsAsAnEmptyArray(t *testing.T) {
	t.Parallel()

	u := &auth.User{ID: uuid.New(), Issuer: "https://idp", Subject: "newcomer"}
	rec := get(t, testPrincipal(), u)

	if !strings.Contains(rec.Body.String(), `"teams":[]`) {
		t.Errorf("teams is not an empty array: %s", rec.Body)
	}
	if got := decode(t, rec); got.Teams == nil {
		t.Error("Teams decoded as null, want an empty slice")
	}
}

// The identity comes from the database row, not the token, because that is
// what every other table joins on.
func TestWhoamiTakesTheIdentityFromTheUser(t *testing.T) {
	t.Parallel()

	p := &auth.Principal{Kind: auth.KindUser, Issuer: "https://stale", Subject: "stale"}
	u := &auth.User{ID: uuid.New(), Issuer: "https://idp", Subject: "user-42"}

	got := decode(t, get(t, p, u))
	if got.Issuer != "https://idp" || got.Subject != "user-42" {
		t.Errorf("identity = (%q, %q), want the resolved user's", got.Issuer, got.Subject)
	}
}

// Kind is the one field with no column — an authentication fact, so it comes
// off the principal.
func TestWhoamiTakesTheKindFromThePrincipal(t *testing.T) {
	t.Parallel()

	p := &auth.Principal{Kind: auth.KindDev, Issuer: "dev", Subject: "dev"}
	u := &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "dev"}

	if got := decode(t, get(t, p, u)); got.Kind != "dev" {
		t.Errorf("Kind = %q, want dev", got.Kind)
	}
}

func TestWhoamiOmitsAnAbsentEmail(t *testing.T) {
	t.Parallel()

	u := &auth.User{ID: uuid.New(), Issuer: "https://idp", Subject: "user-42"}
	rec := get(t, testPrincipal(), u)

	if strings.Contains(rec.Body.String(), "email") {
		t.Errorf("body names email for a user that has none: %s", rec.Body)
	}
}

// The response is a wire contract, not the internal structs. Fields those grow
// must not appear here by accident.
func TestWhoamiExposesOnlyTheContractedFields(t *testing.T) {
	t.Parallel()

	u := &auth.User{
		ID: uuid.New(), Issuer: "https://idp", Subject: "user-42", Email: ptr("a@b.c"),
		Teams: []auth.Membership{{TeamID: uuid.New(), Name: "platform", Role: "admin"}},
	}

	rec := get(t, testPrincipal(), u)

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	allowed := map[string]bool{
		"id": true, "kind": true, "issuer": true, "subject": true, "email": true, "teams": true,
	}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("unexpected field %q: %s", key, rec.Body)
		}
	}

	teams, _ := raw["teams"].([]any)
	if len(teams) != 1 {
		t.Fatalf("teams = %v", raw["teams"])
	}
	first, ok := teams[0].(map[string]any)
	if !ok {
		t.Fatalf("a team is not an object: %v", teams[0])
	}
	for key := range first {
		if key != "id" && key != "name" && key != "role" {
			t.Errorf("unexpected team field %q: %s", key, rec.Body)
		}
	}
}

// The caller's own id, which is the only way someone in no team can learn it —
// GET /users needs membership:write somewhere, and a person with no team holds
// nothing. Without this they cannot ask to be added.
//
// It was deliberately withheld until /users existed. Publishing it discloses
// nothing they did not already send.
func TestWhoamiPublishesTheCallersOwnID(t *testing.T) {
	t.Parallel()

	u := &auth.User{ID: uuid.New(), Issuer: "https://idp", Subject: "user-42"}
	rec := get(t, testPrincipal(), u)

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got := raw["id"]; got != u.ID.String() {
		t.Errorf("id = %v, want %s", got, u.ID)
	}
}

// Never absent, so no omitempty: Resolve upserts every principal on first
// sight, a development token included. A missing key would send a client
// looking for a field the contract says is always there.
func TestWhoamiAlwaysCarriesAnID(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]*auth.Principal{
		"a user": testPrincipal(),
		"a dev token": {
			Kind: auth.KindDev, Issuer: "dev", Subject: "dev",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := get(t, p, &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "dev"})

			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("body is not valid JSON: %v", err)
			}
			if _, ok := raw["id"]; !ok {
				t.Errorf("no id in the response: %s", rec.Body)
			}
		})
	}
}

func TestWhoamiIsJSON(t *testing.T) {
	t.Parallel()

	rec := get(t, testPrincipal(), &auth.User{ID: uuid.New(), Subject: "user-42"})

	got := rec.Result().Header.Get("Content-Type") //nolint:bodyclose // ResponseRecorder needs no close
	if !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// Both branches are unreachable behind the middleware. If either fires, this
// route was registered somewhere it should not be — and 401 would lie, telling
// the client to fetch a token when theirs was fine.
func TestWhoamiReportsAServerErrorWithoutItsContext(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		p *auth.Principal
		u *auth.User
	}{
		"no principal": {u: &auth.User{ID: uuid.New()}},
		"no user":      {p: testPrincipal()},
		"neither":      {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := get(t, tc.p, tc.u)
			if rec.Code == http.StatusUnauthorized {
				t.Fatal("a wiring mistake was reported as an authentication failure")
			}
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	}
}

// The route is mounted at the prefix with an empty pattern, so it answers
// /whoami and nothing else.
func TestWhoamiIsGETOnly(t *testing.T) {
	t.Parallel()

	u := &auth.User{ID: uuid.New(), Subject: "user-42"}

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		if rec := call(t, method, testPrincipal(), u); rec.Code == http.StatusOK {
			t.Errorf("%s /whoami returned 200: %s", method, rec.Body)
		}
	}
}
