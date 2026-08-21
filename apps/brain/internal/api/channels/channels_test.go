package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- fakes ---

type fakeStore struct {
	channels  []db.Channel
	err       error
	deleteErr error

	created  []db.CreateChannelParams
	deleted  []uuid.UUID
	listArgs []db.ListChannelsByTeamParams
}

func (f *fakeStore) CreateChannel(_ context.Context, arg db.CreateChannelParams) (db.Channel, error) {
	f.created = append(f.created, arg)
	if f.err != nil {
		return db.Channel{}, f.err
	}
	row := db.Channel{ID: uuid.New(), TeamID: arg.TeamID, Provider: arg.Provider, Name: arg.Name}
	f.channels = append(f.channels, row)
	return row, nil
}

func (f *fakeStore) GetChannel(_ context.Context, id uuid.UUID) (db.Channel, error) {
	if f.err != nil {
		return db.Channel{}, f.err
	}
	for _, c := range f.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return db.Channel{}, pgx.ErrNoRows
}

func (f *fakeStore) ListChannelsByTeam(_ context.Context, arg db.ListChannelsByTeamParams) ([]db.Channel, error) {
	f.listArgs = append(f.listArgs, arg)
	if f.err != nil {
		return nil, f.err
	}

	var out []db.Channel
	for _, c := range f.channels {
		if c.TeamID == arg.TeamID {
			out = append(out, c)
		}
	}
	if n := int(arg.PageSize); n < len(out) {
		out = out[:n]
	}
	return out, nil
}

func (f *fakeStore) DeleteChannel(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	kept := f.channels[:0]
	for _, c := range f.channels {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.channels = kept
	return nil
}

// fakeSecrets records what was written where, so a test can assert that a
// secret never reached Postgres and did reach the store under the derived
// path.
type fakeSecrets struct {
	put     map[string]map[string]string
	deleted []string
	putErr  error
	delErr  error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{put: map[string]map[string]string{}}
}

func (f *fakeSecrets) Put(_ context.Context, path, key, value string) error {
	if f.putErr != nil {
		return f.putErr
	}
	if f.put[path] == nil {
		f.put[path] = map[string]string{}
	}
	f.put[path][key] = value
	return nil
}

func (f *fakeSecrets) Delete(_ context.Context, path string) error {
	f.deleted = append(f.deleted, path)
	return f.delErr
}

type fakeAuthz struct {
	super   bool
	allowed bool
	err     error
	asked   []authz.Action
}

func (f *fakeAuthz) IsSuperAdmin(*auth.User) bool { return f.super }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return f.allowed, f.err
}

// No route in this package is any_team, so reaching the strictly weaker check
// is itself the failure.
func (f *fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a channels route used the strictly weaker CanInAnyTeam")
}

// channelRoutes is what rbac.json maps for this package. Building the real
// guard rather than an open one means a route whose scope changed fails here
// too, not only in internal/store.
func channelRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	add := func(method, path, scope string, permission *string) {
		t.Helper()
		if err := rs.Add(method, path, scope, permission); err != nil {
			t.Fatalf("Add %s %s: %v", method, path, err)
		}
	}

	write := "channel:write"
	add("GET", "/teams/{id}/channels", "member", nil)
	add("POST", "/teams/{id}/channels", "team", &write)
	add("DELETE", "/teams/{id}/channels/{channelID}", "team", &write)
	return rs
}

func registry(t *testing.T) gateway.Registry {
	t.Helper()

	reg, err := gateway.NewRegistry(custom.New())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

func call(
	t *testing.T, s Store, sec Secrets, a *fakeAuthz, u *auth.User, method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s, sec, registry(t)).
		Register(mux, api.NewGuard(channelRoutes(t), a, discardLogger()))

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, reader)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(teamID uuid.UUID, role string) *auth.User {
	return &auth.User{
		ID:      uuid.New(),
		Issuer:  "dev",
		Subject: "someone",
		Teams:   []auth.Membership{{TeamID: teamID, Name: "platform", Role: role}},
	}
}

func outsider() *auth.User {
	return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
}

func channelsPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[channel] {
	t.Helper()

	var got api.Page[channel]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of channels: %v (%s)", err, rec.Body)
	}
	return got
}

func decodeCreated(t *testing.T, rec *httptest.ResponseRecorder) created {
	t.Helper()

	var got created
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a created channel: %v (%s)", err, rec.Body)
	}
	return got
}

// --- authorisation ---

// Connecting a channel is an admin act: channel:write is granted to admin and
// not to member in rbac.json. The handler contains no check at all — the guard
// runs before it, so this is really a test that the mapping is wired.
func TestCreateNeedsChannelWrite(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	for name, tc := range map[string]struct {
		allowed bool
		want    int
	}{
		"granted": {allowed: true, want: http.StatusCreated},
		"refused": {allowed: false, want: http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := &fakeAuthz{allowed: tc.allowed}
			rec := call(t, &fakeStore{}, newFakeSecrets(), a,
				memberOf(teamID, "admin"), http.MethodPost,
				"/teams/"+teamID.String()+"/channels",
				`{"provider":"custom","name":"support"}`)

			if rec.Code != tc.want {
				t.Errorf("POST = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			if len(a.asked) != 1 || a.asked[0] != authz.Action("channel:write") {
				t.Errorf("the guard asked for %v, want channel:write", a.asked)
			}
		})
	}
}

// Listing is `member`: belonging is the whole check, so a non-member gets 404
// rather than 403. A 403 would confirm the team id is real.
func TestListIsVisibleToAnyMemberAndHiddenFromOutsiders(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{channels: []db.Channel{
		{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"},
	}}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{}, memberOf(teamID, "member"),
		http.MethodGet, "/teams/"+teamID.String()+"/channels", "")
	if rec.Code != http.StatusOK {
		t.Errorf("GET as member = %d, want 200 (%s)", rec.Code, rec.Body)
	}

	rec = call(t, s, newFakeSecrets(), &fakeAuthz{}, outsider(),
		http.MethodGet, "/teams/"+teamID.String()+"/channels", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET as an outsider = %d, want 404", rec.Code)
	}
}

func TestListReturnsAPageEnvelope(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{channels: []db.Channel{
		{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"},
	}}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{}, memberOf(teamID, "member"),
		http.MethodGet, "/teams/"+teamID.String()+"/channels", "")

	page := channelsPage(t, rec)
	if len(page.Items) != 1 || page.Items[0].Name != "support" {
		t.Errorf("items = %+v, want the one channel", page.Items)
	}
	if !strings.Contains(rec.Body.String(), `"items"`) {
		t.Errorf("body %s is not a page envelope", rec.Body)
	}
}

func TestListAsksOnlyForTheTeamInThePath(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}

	call(t, s, newFakeSecrets(), &fakeAuthz{}, memberOf(teamID, "member"),
		http.MethodGet, "/teams/"+teamID.String()+"/channels", "")

	if len(s.listArgs) != 1 {
		t.Fatalf("the store was asked %d times, want once", len(s.listArgs))
	}
	if s.listArgs[0].TeamID != teamID {
		t.Errorf("listed team %s, want %s", s.listArgs[0].TeamID, teamID)
	}
}

// --- creating ---

// A provider no adapter answers to would accept a channel whose webhooks can
// never be verified — and the failure would be a 404 on every request with
// nothing in the logs pointing at the row.
func TestCreateRejectsAnUnknownProvider(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodPost,
		"/teams/"+teamID.String()+"/channels",
		`{"provider":"nosuch","name":"support"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown provider = %d, want 400", rec.Code)
	}
	if len(s.created) != 0 {
		t.Errorf("a row was written for an unknown provider: %+v", s.created)
	}
}

// The registry is matched byte for byte, so a capitalised provider is an
// unknown one rather than a tolerated one.
func TestCreateRejectsAProviderWithTheWrongCase(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodPost,
		"/teams/"+teamID.String()+"/channels",
		`{"provider":"Custom","name":"support"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf(`provider "Custom" = %d, want 400`, rec.Code)
	}
}

func TestCreateRejectsAnEmptyOrOversizedName(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	for name, body := range map[string]string{
		"empty":      `{"provider":"custom","name":""}`,
		"whitespace": `{"provider":"custom","name":"   "}`,
		"too long":   `{"provider":"custom","name":"` + strings.Repeat("a", maxNameBytes+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true},
				memberOf(teamID, "admin"), http.MethodPost,
				"/teams/"+teamID.String()+"/channels", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("a %s name = %d, want 400", name, rec.Code)
			}
		})
	}
}

// Omitting the secret is the custom case: the scheme is ours, so there is
// nothing to match and no reason for a human to choose the bytes.
func TestCreateGeneratesASecretAndReturnsItOnce(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}
	sec := newFakeSecrets()

	rec := call(t, s, sec, &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}

	got := decodeCreated(t, rec)
	if got.SigningSecret == "" {
		t.Fatal("no secret was returned, so nobody can ever sign a request to this channel")
	}
	if !strings.HasPrefix(got.SigningSecret, secretPrefix) {
		t.Errorf("secret %q has no %q prefix", got.SigningSecret, secretPrefix)
	}

	path := secrets.Path(teamID, secrets.KindChannel, got.ID)
	if stored := sec.put[path][gateway.KeySigning]; stored != got.SigningSecret {
		t.Errorf("stored %q at %s, want the secret that was returned", stored, path)
	}
}

// Two channels must not share a secret, which they would if the generator
// were seeded once or returned a constant.
func TestEveryGeneratedSecretIsDifferent(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	seen := map[string]bool{}

	for range 20 {
		rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true},
			memberOf(teamID, "admin"), http.MethodPost,
			"/teams/"+teamID.String()+"/channels",
			`{"provider":"custom","name":"support"}`)

		secret := decodeCreated(t, rec).SigningSecret
		if seen[secret] {
			t.Fatalf("the same secret was generated twice: %q", secret)
		}
		seen[secret] = true
	}
}

// A supplied secret is the provider's own — Slack's Signing Secret. The caller
// already has it, so echoing it back only widens where it has been.
func TestASuppliedSecretIsStoredAndNeverReturned(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	sec := newFakeSecrets()

	rec := call(t, &fakeStore{}, sec, &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodPost,
		"/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support","signing_secret":"theirs"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "theirs") {
		t.Errorf("the response repeated the supplied secret: %s", rec.Body)
	}

	got := decodeCreated(t, rec)
	path := secrets.Path(teamID, secrets.KindChannel, got.ID)
	if stored := sec.put[path][gateway.KeySigning]; stored != "theirs" {
		t.Errorf("stored %q, want the supplied secret", stored)
	}
}

// hmac.Equal("", "") is true, so a blank secret is a channel that an adapter
// forgetting to check would accept anything for. Refuse it while it is a 400.
func TestABlankSuppliedSecretIsRefused(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodPost,
		"/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support","signing_secret":"   "}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("a blank signing_secret = %d, want 400", rec.Code)
	}
	if len(s.created) != 0 {
		t.Errorf("a row was written with a blank secret: %+v", s.created)
	}
}

// A row whose secret is missing refuses every webhook with no explanation and
// nothing to point at. Rolling the row back turns that into a 503 the caller
// can act on.
func TestAChannelIsRolledBackWhenItsSecretCannotBeStored(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}
	sec := newFakeSecrets()
	sec.putErr = errors.New("openbao is down")

	rec := call(t, s, sec, &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST with a failing secret store = %d, want 503", rec.Code)
	}
	if len(s.deleted) != 1 {
		t.Fatalf("the row was not rolled back: deleted %v", s.deleted)
	}
	if len(s.channels) != 0 {
		t.Errorf("a channel survived with no secret: %+v", s.channels)
	}
}

func TestADuplicateNameIsAConflict(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{err: &pgconn.PgError{Code: codeUniqueViolation}}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodPost,
		"/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("a duplicate name = %d, want 409 (%s)", rec.Code, rec.Body)
	}
}

// --- deleting ---

func TestDeleteRemovesTheChannelAndItsSecret(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"}
	s := &fakeStore{channels: []db.Channel{ch}}
	sec := newFakeSecrets()

	rec := call(t, s, sec, &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodDelete, "/teams/"+teamID.String()+"/channels/"+ch.ID.String(), "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if len(s.deleted) != 1 || s.deleted[0] != ch.ID {
		t.Errorf("deleted %v, want %s", s.deleted, ch.ID)
	}

	// A Postgres cascade does not reach the secret store, so the delete has
	// to be explicit or a disconnected channel leaves live credentials behind.
	want := secrets.Path(teamID, secrets.KindChannel, ch.ID)
	if len(sec.deleted) != 1 || sec.deleted[0] != want {
		t.Errorf("deleted secrets %v, want [%s]", sec.deleted, want)
	}
}

// The guard authorised the caller for the team in the path. It knows nothing
// about the channel id, so without this check an admin of any team could
// delete any channel by naming their own team in the URL.
func TestDeleteRefusesAChannelBelongingToAnotherTeam(t *testing.T) {
	t.Parallel()

	mine := uuid.New()
	theirs := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: theirs, Provider: "custom", Name: "billing"}
	s := &fakeStore{channels: []db.Channel{ch}}
	sec := newFakeSecrets()

	rec := call(t, s, sec, &fakeAuthz{allowed: true}, memberOf(mine, "admin"),
		http.MethodDelete, "/teams/"+mine.String()+"/channels/"+ch.ID.String(), "")

	// 404 and not 403: a 403 would confirm the id is real, which is how
	// someone walks the uuid space for channels in other teams.
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleting another team's channel = %d, want 404", rec.Code)
	}
	if len(s.deleted) != 0 {
		t.Errorf("another team's channel was deleted: %v", s.deleted)
	}
	if len(sec.deleted) != 0 {
		t.Errorf("another team's secret was deleted: %v", sec.deleted)
	}
}

func TestDeletingAChannelThatIsNotThereIsNotFound(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodDelete,
		"/teams/"+teamID.String()+"/channels/"+uuid.New().String(), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of an unknown channel = %d, want 404", rec.Code)
	}
}

func TestDeleteRejectsAChannelIDThatIsNotAUUID(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true},
		memberOf(teamID, "admin"), http.MethodDelete,
		"/teams/"+teamID.String()+"/channels/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed channel id = %d, want 400", rec.Code)
	}
}

// The row goes first, so a failure to delete the secret leaves something inert
// — the path carries the channel's uuid and uuids are not reused — rather than
// a channel that refuses every webhook.
func TestTheChannelIsGoneEvenIfItsSecretCannotBeDeleted(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"}
	s := &fakeStore{channels: []db.Channel{ch}}
	sec := newFakeSecrets()
	sec.delErr = errors.New("openbao is down")

	rec := call(t, s, sec, &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodDelete, "/teams/"+teamID.String()+"/channels/"+ch.ID.String(), "")

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE = %d, want 204 — the channel is gone either way", rec.Code)
	}
	if len(s.channels) != 0 {
		t.Errorf("the row survived: %+v", s.channels)
	}
}

// --- the secret never leaks ---

// The one rule this package exists to keep. Every response body, on every
// path, is checked for the supplied secret.
func TestNoResponseEverRepeatsAStoredSecret(t *testing.T) {
	t.Parallel()

	const secret = "whsec-nobody-should-see-this"

	teamID := uuid.New()
	s := &fakeStore{}
	sec := newFakeSecrets()
	a := &fakeAuthz{allowed: true}
	u := memberOf(teamID, "admin")
	base := "/teams/" + teamID.String() + "/channels"

	rec := call(t, s, sec, a, u, http.MethodPost, base,
		`{"provider":"custom","name":"support","signing_secret":"`+secret+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	id := decodeCreated(t, rec).ID

	for what, rec := range map[string]*httptest.ResponseRecorder{
		"create": rec,
		"list":   call(t, s, sec, a, u, http.MethodGet, base, ""),
		"delete": call(t, s, sec, a, u, http.MethodDelete, base+"/"+id.String(), ""),
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the %s response contains the signing secret: %s", what, rec.Body)
		}
	}
}
