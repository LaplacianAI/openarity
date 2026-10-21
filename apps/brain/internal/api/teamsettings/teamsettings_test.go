package teamsettings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var errAny = errors.New("connection reset by peer")

type fakeStore struct {
	row       *db.TeamSetting
	getErr    error
	upsertErr error
	recordErr error

	asked    []uuid.UUID
	upserts  []db.UpsertTeamSettingsParams
	recorded []db.RecordDeletedSecretParams
}

func (f *fakeStore) GetTeamSettings(_ context.Context, team uuid.UUID) (db.TeamSetting, error) {
	f.asked = append(f.asked, team)
	if f.getErr != nil {
		return db.TeamSetting{}, f.getErr
	}
	if f.row == nil || f.row.TeamID != team {
		return db.TeamSetting{}, pgx.ErrNoRows
	}
	return *f.row, nil
}

func (f *fakeStore) UpsertTeamSettings(_ context.Context, p db.UpsertTeamSettingsParams) (db.TeamSetting, error) {
	f.upserts = append(f.upserts, p)
	if f.upsertErr != nil {
		return db.TeamSetting{}, f.upsertErr
	}
	row := db.TeamSetting{
		TeamID:             p.TeamID,
		ModelName:          p.ModelName,
		ModelMaxTokens:     p.ModelMaxTokens,
		ModelTemperature:   p.ModelTemperature,
		SearchProvider:     p.SearchProvider,
		SearchUrl:          p.SearchUrl,
		SearchSecretID:     p.SearchSecretID,
		Learning:           p.Learning,
		CompactionMessages: p.CompactionMessages,
		UpdatedAt:          time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC),
	}
	f.row = &row
	return row, nil
}

func (f *fakeStore) RecordDeletedSecret(_ context.Context, p db.RecordDeletedSecretParams) error {
	f.recorded = append(f.recorded, p)
	return f.recordErr
}

type put struct{ path, key, value string }

type fakeSecrets struct {
	puts   []put
	putErr error
}

func (f *fakeSecrets) Put(_ context.Context, path, key, value string) error {
	f.puts = append(f.puts, put{path, key, value})
	return f.putErr
}

type fakeAuthz struct {
	allowed bool
	asked   []authz.Action
}

func (f *fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return f.allowed, nil
}

func (f *fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a settings route used the strictly weaker CanInAnyTeam")
}

// What rbac.json maps for this package, built into the real guard so a scope
// that changes there fails here too.
func settingsRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	write := "team:settings"
	for _, r := range []struct {
		method, scope string
		perm          *string
	}{
		{"GET", "member", nil},
		{"PUT", "team", &write},
	} {
		if err := rs.Add(r.method, "/teams/{id}/settings", r.scope, r.perm); err != nil {
			t.Fatalf("Add %s: %v", r.method, err)
		}
	}
	return rs
}

// public is the deployment's predicate with no MCP_PRIVATE_NETWORKS: public
// addresses only.
func public(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsPrivate()
}

type env struct {
	team    uuid.UUID
	store   *fakeStore
	secrets *fakeSecrets
	authz   *fakeAuthz
	allow   func(netip.Addr) bool
}

func newEnv() *env {
	return &env{
		team:    uuid.New(),
		store:   &fakeStore{},
		secrets: &fakeSecrets{},
		authz:   &fakeAuthz{allowed: true},
		allow:   public,
	}
}

func (e *env) call(t *testing.T, u *auth.User, method, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), e.store, e.secrets, e.allow).
		Register(mux, api.NewGuard(settingsRoutes(t), e.authz, discardLogger()))

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method,
		"/teams/"+e.team.String()+"/settings", reader)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (e *env) member() *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: e.team, Name: "platform", Role: "member"}},
	}
}

func (e *env) get(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return e.call(t, e.member(), http.MethodGet, "")
}

func (e *env) save(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.call(t, e.member(), http.MethodPut, body)
}

// stored puts a saved row in the fake, as an earlier save would have.
func (e *env) stored(provider string, secretID *uuid.UUID, url *string) {
	e.store.row = &db.TeamSetting{
		TeamID:             e.team,
		SearchProvider:     provider,
		SearchSecretID:     secretID,
		SearchUrl:          url,
		CompactionMessages: 100,
		UpdatedAt:          time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
}

// The wire shape as the spec has it, decoded independently of the package's
// own types so a renamed JSON tag fails here.
type wire struct {
	Model *struct {
		Name        string   `json:"name"`
		MaxTokens   int32    `json:"max_tokens"`
		Temperature *float64 `json:"temperature"`
	} `json:"model"`
	Search struct {
		Provider string  `json:"provider"`
		URL      *string `json:"url"`
		KeySet   bool    `json:"key_set"`
	} `json:"search"`
	Learning           bool       `json:"learning"`
	CompactionMessages int32      `json:"compaction_messages"`
	UpdatedAt          *time.Time `json:"updated_at"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) wire {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var w wire
	if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
		t.Fatalf("body is not settings: %v (%s)", err, rec.Body)
	}
	return w
}

func body(model, search string, learning bool, compaction int) string {
	if model == "" {
		model = "null"
	}
	l := "false"
	if learning {
		l = "true"
	}
	return `{"model":` + model + `,"search":` + search +
		`,"learning":` + l + `,"compaction_messages":` + itoa(compaction) + `}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

const (
	searchOff = `{"provider":"none"}`
	gpt       = `{"name":"gpt-5","max_tokens":4096,"temperature":0.2}`
)

// ---------------------------------------------------------------------------
// Reading

func TestATeamThatNeverSavedReadsTheDefaults(t *testing.T) {
	e := newEnv()

	got := decode(t, e.get(t))

	if got.Model != nil || got.Search.Provider != "none" || got.Search.URL != nil ||
		got.Search.KeySet || got.Learning || got.CompactionMessages != 100 || got.UpdatedAt != nil {
		t.Errorf("defaults = %+v; want no model, search none with no url or key, "+
			"learning off, compaction 100, updated_at null", got)
	}
}

// Null, not absent: a reader can tell "never saved" from a field the server
// forgot to send.
func TestTheDefaultsSendNullsRatherThanLeavingFieldsOut(t *testing.T) {
	e := newEnv()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(e.get(t).Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"model", "updated_at"} {
		if v, ok := raw[field]; !ok || string(v) != "null" {
			t.Errorf("%s = %s (present %v), want null", field, v, ok)
		}
	}
}

func TestReadingAsksForTheTeamInThePathOnly(t *testing.T) {
	e := newEnv()
	e.get(t)

	if len(e.store.asked) != 1 || e.store.asked[0] != e.team {
		t.Errorf("asked for %v, want [%s]", e.store.asked, e.team)
	}
}

func TestReadingSaysAKeyIsSetAndNeverWhatItIs(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("brave", &id, nil)

	rec := e.get(t)
	got := decode(t, rec)

	if !got.Search.KeySet {
		t.Error("key_set = false for a saved Brave key")
	}
	if strings.Contains(rec.Body.String(), id.String()) {
		t.Errorf("the response names the key's secret id: %s", rec.Body)
	}
}

func TestAFailedReadIsAServerError(t *testing.T) {
	e := newEnv()
	e.store.getErr = errAny

	if rec := e.get(t); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Who

func TestAMemberCanReadButNotChange(t *testing.T) {
	e := newEnv()
	e.authz.allowed = false

	if rec := e.get(t); rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200 for any member", rec.Code)
	}
	if rec := e.save(t, body("", searchOff, true, 100)); rec.Code != http.StatusForbidden {
		t.Errorf("PUT status = %d, want 403 without team:settings", rec.Code)
	}
	if len(e.authz.asked) != 1 || e.authz.asked[0] != "team:settings" {
		t.Errorf("asked for %v, want [team:settings]", e.authz.asked)
	}
	if len(e.store.upserts) != 0 {
		t.Errorf("a refused save wrote %d rows", len(e.store.upserts))
	}
}

func TestSomeoneOutsideTheTeamCannotTellItExists(t *testing.T) {
	e := newEnv()
	outsider := &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}

	if rec := e.call(t, outsider, http.MethodGet, ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(e.store.asked) != 0 {
		t.Error("the store was read for somebody outside the team")
	}
}

// ---------------------------------------------------------------------------
// Saving

func TestSavingReturnsWhatWasSaved(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, body(gpt, `{"provider":"searxng","url":"http://searxng:8080"}`, true, 250)))

	if got.Model == nil || got.Model.Name != "gpt-5" || got.Model.MaxTokens != 4096 ||
		got.Model.Temperature == nil || *got.Model.Temperature != 0.2 {
		t.Errorf("model = %+v, want gpt-5, 4096, 0.2", got.Model)
	}
	if got.Search.Provider != "searxng" || got.Search.URL == nil || *got.Search.URL != "http://searxng:8080" {
		t.Errorf("search = %+v, want searxng at http://searxng:8080", got.Search)
	}
	if !got.Learning || got.CompactionMessages != 250 || got.UpdatedAt == nil {
		t.Errorf("learning %v, compaction %d, updated_at %v; want true, 250, set",
			got.Learning, got.CompactionMessages, got.UpdatedAt)
	}
	if len(e.store.upserts) != 1 || e.store.upserts[0].TeamID != e.team {
		t.Errorf("upserts = %+v, want one for team %s", e.store.upserts, e.team)
	}
}

// The key goes to a path of its own, and the row holds only that path's id.
// It is stored exactly as sent: trimming a credential changes it.
func TestTheKeyGoesToItsOwnPathInTheSecretStore(t *testing.T) {
	e := newEnv()
	const key = " brave-key-with-spaces "

	rec := e.save(t, body("", `{"provider":"brave","api_key":"`+key+`"}`, false, 100))
	got := decode(t, rec)

	if len(e.secrets.puts) != 1 {
		t.Fatalf("puts = %+v, want one", e.secrets.puts)
	}
	p := e.secrets.puts[0]
	saved := e.store.upserts[0].SearchSecretID
	if saved == nil {
		t.Fatal("the row names no secret")
	}
	if want := secrets.Path(e.team, secrets.KindSearch, *saved); p.path != want {
		t.Errorf("path = %q, want %q: the row's id must name the path written", p.path, want)
	}
	if p.key != KeyAPI || p.value != key {
		t.Errorf("put %q = %q, want %q = %q", p.key, p.value, KeyAPI, key)
	}
	if !got.Search.KeySet {
		t.Error("key_set = false after saving a key")
	}
	if strings.Contains(rec.Body.String(), "brave-key") {
		t.Errorf("the response carries the key: %s", rec.Body)
	}
}

func TestEverySavedKeyGetsANewPath(t *testing.T) {
	e := newEnv()

	e.save(t, body("", `{"provider":"brave","api_key":"one"}`, false, 100))
	e.save(t, body("", `{"provider":"brave","api_key":"two"}`, false, 100))

	if len(e.secrets.puts) != 2 || e.secrets.puts[0].path == e.secrets.puts[1].path {
		t.Errorf("puts = %+v, want two different paths", e.secrets.puts)
	}
}

// Review Focus 1. The dashboard cannot send the key back, because it never
// has it. A save that flips learning must not take search down with it.
func TestSavingWithoutAKeyKeepsTheStoredOne(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("brave", &id, nil)

	got := decode(t, e.save(t, body("", `{"provider":"brave"}`, true, 100)))

	if len(e.secrets.puts) != 0 {
		t.Errorf("wrote a key nobody sent: %+v", e.secrets.puts)
	}
	if s := e.store.upserts[0].SearchSecretID; s == nil || *s != id {
		t.Errorf("saved secret id %v, want the stored %s kept", s, id)
	}
	if !got.Search.KeySet || !got.Learning {
		t.Errorf("key_set %v, learning %v; want both true", got.Search.KeySet, got.Learning)
	}
}

// Review Focus 2. Brave's key sent to Tavily is a key leaked to a third
// party, so switching needs a key of its own.
func TestAnotherKeyedProviderNeedsItsOwnKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("brave", &id, nil)

	rec := e.save(t, body("", `{"provider":"tavily"}`, false, 100))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "search.api_key") {
		t.Errorf("status %d, body %q; want 400 naming search.api_key", rec.Code, rec.Body)
	}
	if len(e.store.upserts) != 0 || len(e.secrets.puts) != 0 {
		t.Error("a refused save wrote something")
	}
}

func TestAKeyedProviderWithNoKeyStoredNeedsOne(t *testing.T) {
	e := newEnv()

	rec := e.save(t, body("", `{"provider":"brave"}`, false, 100))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "search.api_key") {
		t.Errorf("status %d, body %q; want 400 naming search.api_key", rec.Code, rec.Body)
	}
}

func TestTurningSearchOffDropsTheKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("tavily", &id, nil)

	got := decode(t, e.save(t, body("", searchOff, false, 100)))

	if s := e.store.upserts[0].SearchSecretID; s != nil {
		t.Errorf("saved secret id %s, want none: the trigger tombstones the old one", s)
	}
	if got.Search.KeySet {
		t.Error("key_set = true with search off")
	}
}

func TestSwitchingToSearxngDropsTheKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("brave", &id, nil)

	e.save(t, body("", `{"provider":"searxng","url":"https://search.example.com"}`, false, 100))

	if s := e.store.upserts[0].SearchSecretID; s != nil {
		t.Errorf("saved secret id %s, want none for searxng", s)
	}
}

func TestNoModelIsSavedAsNoModel(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, body("null", searchOff, false, 100)))

	p := e.store.upserts[0]
	if p.ModelName != nil || p.ModelMaxTokens != nil || p.ModelTemperature != nil || got.Model != nil {
		t.Errorf("saved model %v/%v/%v, response %+v; want all empty",
			p.ModelName, p.ModelMaxTokens, p.ModelTemperature, got.Model)
	}
}

func TestAModelWithNoTemperatureLeavesItToTheModel(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, body(`{"name":"gpt-5","max_tokens":4096}`, searchOff, false, 100)))

	if e.store.upserts[0].ModelTemperature != nil || got.Model == nil || got.Model.Temperature != nil {
		t.Error("a temperature appeared that nobody set")
	}
}

// ---------------------------------------------------------------------------
// Failures

func TestASecretStoreFailureChangesNothing(t *testing.T) {
	e := newEnv()
	e.secrets.putErr = secrets.ErrUnavailable

	rec := e.save(t, body("", `{"provider":"brave","api_key":"k"}`, false, 100))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if len(e.store.upserts) != 0 {
		t.Error("settings were saved naming a key the store never took")
	}
}

// Review Focus 3. The key reached the store and the row did not: record it,
// so the sweep removes a credential nothing names.
func TestAFailedSaveDestroysTheKeyItWrote(t *testing.T) {
	e := newEnv()
	e.store.upsertErr = errAny

	rec := e.save(t, body("", `{"provider":"tavily","api_key":"k"}`, false, 100))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if len(e.secrets.puts) != 1 || len(e.store.recorded) != 1 {
		t.Fatalf("puts %+v, recorded %+v; want one of each", e.secrets.puts, e.store.recorded)
	}
	if r := e.store.recorded[0]; r.Path != e.secrets.puts[0].path || r.TeamID != e.team {
		t.Errorf("recorded %+v, want the path just written, for team %s", r, e.team)
	}
}

func TestAFailedSaveWithNoNewKeyRecordsNothing(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("brave", &id, nil)
	e.store.upsertErr = errAny

	e.save(t, body("", `{"provider":"brave"}`, false, 100))

	if len(e.store.recorded) != 0 {
		t.Errorf("recorded %+v: the kept key is still the team's", e.store.recorded)
	}
}

// Reading what is stored is part of deciding whether the key may be kept, so
// a read that fails must not be taken for "nothing stored".
func TestAFailedReadBeforeSavingIsAServerError(t *testing.T) {
	e := newEnv()
	e.store.getErr = errAny

	rec := e.save(t, body("", `{"provider":"brave"}`, false, 100))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if len(e.store.upserts) != 0 {
		t.Error("saved after failing to read what was stored")
	}
}

// ---------------------------------------------------------------------------
// Validation, through the route: what is refused, and the field it names.

func TestValidateRefuses(t *testing.T) {
	big := strings.Repeat("k", 4097)

	for name, tc := range map[string]struct {
		body  string
		field string
	}{
		"a model name with a leading space": {
			body(`{"name":" gpt","max_tokens":1}`, searchOff, false, 100), "model.name",
		},
		"an empty model name": {
			body(`{"name":"","max_tokens":1}`, searchOff, false, 100), "model.name",
		},
		"max_tokens of zero": {
			body(`{"name":"gpt","max_tokens":0}`, searchOff, false, 100), "model.max_tokens",
		},
		"a negative temperature": {
			body(`{"name":"gpt","max_tokens":1,"temperature":-0.1}`, searchOff, false, 100), "model.temperature",
		},
		"a temperature above 2": {
			body(`{"name":"gpt","max_tokens":1,"temperature":2.01}`, searchOff, false, 100), "model.temperature",
		},
		"compaction below 10": {
			body("", searchOff, false, 9), "compaction_messages",
		},
		"compaction above 10000": {
			body("", searchOff, false, 10001), "compaction_messages",
		},
		"a provider nobody wrote": {
			body("", `{"provider":"bing"}`, false, 100), "search.provider",
		},
		"no provider": {
			body("", `{}`, false, 100), "search.provider",
		},
		"searxng with no url": {
			body("", `{"provider":"searxng"}`, false, 100), "search.url",
		},
		"searxng over ftp": {
			body("", `{"provider":"searxng","url":"ftp://searxng"}`, false, 100), "search.url",
		},
		"searxng with no host": {
			body("", `{"provider":"searxng","url":"http://"}`, false, 100), "search.url",
		},
		"searxng with credentials in the url": {
			body("", `{"provider":"searxng","url":"http://user:pw@searxng"}`, false, 100), "search.url",
		},
		"searxng with a query": {
			body("", `{"provider":"searxng","url":"http://searxng/?q=1"}`, false, 100), "search.url",
		},
		"searxng with a fragment": {
			body("", `{"provider":"searxng","url":"http://searxng/#top"}`, false, 100), "search.url",
		},
		"searxng on loopback": {
			body("", `{"provider":"searxng","url":"http://127.0.0.1:8080"}`, false, 100), "search.url",
		},
		"searxng on IPv6 loopback": {
			body("", `{"provider":"searxng","url":"http://[::1]:8080"}`, false, 100), "search.url",
		},
		"searxng on the metadata address": {
			body("", `{"provider":"searxng","url":"http://169.254.169.254"}`, false, 100), "search.url",
		},
		"searxng on a private address not allowed": {
			body("", `{"provider":"searxng","url":"http://10.0.0.5"}`, false, 100), "search.url",
		},
		"self-hosted firecrawl with no url": {
			body("", `{"provider":"firecrawl-oss"}`, false, 100), "search.url",
		},
		"self-hosted firecrawl on loopback": {
			body("", `{"provider":"firecrawl-oss","url":"http://127.0.0.1:3002"}`, false, 100), "search.url",
		},
		"firecrawl cloud with a url": {
			body("", `{"provider":"firecrawl-cloud","url":"https://api.firecrawl.dev","api_key":"fc-k"}`, false, 100), "search.url",
		},
		"firecrawl cloud with no key": {
			body("", `{"provider":"firecrawl-cloud"}`, false, 100), "search.api_key",
		},
		"an empty key for firecrawl cloud": {
			body("", `{"provider":"firecrawl-cloud","api_key":""}`, false, 100), "search.api_key",
		},
		"self-hosted firecrawl with a key holding NUL": {
			body("", `{"provider":"firecrawl-oss","url":"http://firecrawl:3002","api_key":"a\u0000b"}`, false, 100), "search.api_key",
		},
		"searxng with a key": {
			body("", `{"provider":"searxng","url":"http://searxng","api_key":"k"}`, false, 100), "search.api_key",
		},
		"search off with a url": {
			body("", `{"provider":"none","url":"http://searxng"}`, false, 100), "search.url",
		},
		"search off with a key": {
			body("", `{"provider":"none","api_key":"k"}`, false, 100), "search.api_key",
		},
		"brave with a url": {
			body("", `{"provider":"brave","url":"http://x","api_key":"k"}`, false, 100), "search.url",
		},
		"an empty key": {
			body("", `{"provider":"brave","api_key":""}`, false, 100), "search.api_key",
		},
		"a key holding NUL": {
			body("", `{"provider":"brave","api_key":"a\u0000b"}`, false, 100), "search.api_key",
		},
		"a key over 4096 bytes": {
			body("", `{"provider":"brave","api_key":"`+big+`"}`, false, 100), "search.api_key",
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()

			rec := e.save(t, tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.field) {
				t.Errorf("message %q does not name %s", strings.TrimSpace(rec.Body.String()), tc.field)
			}
			if len(e.store.upserts) != 0 || len(e.secrets.puts) != 0 {
				t.Error("a refused body wrote something")
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	for name, tc := range map[string]struct {
		body  string
		allow func(netip.Addr) bool
	}{
		// A name is checked when the brain connects, not here: only a dial
		// sees what it resolves to.
		"searxng by compose name": {
			body("", `{"provider":"searxng","url":"http://searxng:8080"}`, false, 100), public,
		},
		"searxng under a path": {
			body("", `{"provider":"searxng","url":"https://search.example.com/searxng"}`, false, 100), public,
		},
		"searxng on a public address": {
			body("", `{"provider":"searxng","url":"http://203.0.113.7"}`, false, 100), public,
		},
		"searxng on a private range the deployment allows": {
			body("", `{"provider":"searxng","url":"http://10.0.0.5"}`, false, 100),
			func(netip.Addr) bool { return true },
		},
		"self-hosted firecrawl with no key": {
			body("", `{"provider":"firecrawl-oss","url":"http://firecrawl:3002"}`, false, 100), public,
		},
		"firecrawl cloud with a key": {
			body("", `{"provider":"firecrawl-cloud","api_key":"fc-k"}`, false, 100), public,
		},
		"the edges of compaction": {
			body("", searchOff, false, 10), public,
		},
		"compaction at its ceiling": {
			body("", searchOff, false, 10000), public,
		},
		"a temperature of exactly 0 and 2": {
			body(`{"name":"gpt","max_tokens":1,"temperature":0}`, searchOff, false, 100), public,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.allow = tc.allow

			if rec := e.save(t, tc.body); rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
		})
	}
}

// The deployment's allowance never reaches link-local: 169.254.169.254 is the
// cloud's credential endpoint on every major provider, and no private network
// setting should be able to name it.
func TestLinkLocalIsRefusedEvenWhenTheDeploymentAllowsEverything(t *testing.T) {
	e := newEnv()
	e.allow = func(netip.Addr) bool { return true }

	for _, url := range []string{"http://169.254.169.254", "http://[fe80::1]"} {
		rec := e.save(t, body("", `{"provider":"searxng","url":"`+url+`"}`, false, 100))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", url, rec.Code)
		}
	}
}

func TestAnUnknownFieldIsRefused(t *testing.T) {
	e := newEnv()

	rec := e.save(t, `{"model":null,"search":{"provider":"none"},"learning":false,`+
		`"compaction_messages":100,"web_search":true}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Firecrawl: a cloud provider that needs a key, and a self-hosted one where a
// key is optional because an instance may run with authentication off.

func TestSelfHostedFirecrawlStoresAKeyWhenGivenOne(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, body("",
		`{"provider":"firecrawl-oss","url":"http://firecrawl:3002","api_key":"fc-self"}`, false, 100)))

	if len(e.secrets.puts) != 1 || e.secrets.puts[0].value != "fc-self" {
		t.Errorf("puts = %+v, want the key stored", e.secrets.puts)
	}
	if !got.Search.KeySet || got.Search.URL == nil || *got.Search.URL != "http://firecrawl:3002" {
		t.Errorf("search = %+v, want the url and a key set", got.Search)
	}
}

func TestSelfHostedFirecrawlRunsWithoutAKey(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, body("", `{"provider":"firecrawl-oss","url":"http://firecrawl:3002"}`, false, 100)))

	if len(e.secrets.puts) != 0 || e.store.upserts[0].SearchSecretID != nil || got.Search.KeySet {
		t.Error("a key appeared that nobody sent")
	}
}

// Leaving the key out keeps it, as for every provider, so the dashboard can
// save a learning switch without knowing it.
func TestSelfHostedFirecrawlKeepsItsKeyWhenLeftOut(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	url := "http://firecrawl:3002"
	e.stored("firecrawl-oss", &id, &url)

	e.save(t, body("", `{"provider":"firecrawl-oss","url":"http://firecrawl:3002"}`, true, 100))

	if s := e.store.upserts[0].SearchSecretID; s == nil || *s != id {
		t.Errorf("saved secret id %v, want the stored %s kept", s, id)
	}
}

// An empty key is how a self-hosted instance's key is removed: JSON cannot
// tell null from absent, and absent means keep.
func TestAnEmptyKeyRemovesSelfHostedFirecrawlsKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	url := "http://firecrawl:3002"
	e.stored("firecrawl-oss", &id, &url)

	got := decode(t, e.save(t, body("",
		`{"provider":"firecrawl-oss","url":"http://firecrawl:3002","api_key":""}`, false, 100)))

	if len(e.secrets.puts) != 0 {
		t.Errorf("stored an empty key: %+v", e.secrets.puts)
	}
	if e.store.upserts[0].SearchSecretID != nil || got.Search.KeySet {
		t.Error("the key is still named; the trigger only tombstones it once the id is gone")
	}
}

// A self-hosted instance's key is not the cloud's, and the reverse.
func TestSwitchingBetweenFirecrawlsNeedsItsOwnKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	url := "http://firecrawl:3002"
	e.stored("firecrawl-oss", &id, &url)

	rec := e.save(t, body("", `{"provider":"firecrawl-cloud"}`, false, 100))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "search.api_key") {
		t.Errorf("status %d, body %q; want 400 naming search.api_key", rec.Code, rec.Body)
	}
}

func TestSwitchingToSelfHostedFirecrawlDoesNotCarryAKeyOver(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.stored("firecrawl-cloud", &id, nil)

	got := decode(t, e.save(t, body("", `{"provider":"firecrawl-oss","url":"http://firecrawl:3002"}`, false, 100)))

	if e.store.upserts[0].SearchSecretID != nil || got.Search.KeySet {
		t.Error("the cloud key was carried to a self-hosted instance")
	}
}

// An IPv4 address written in its IPv6 form is the same address. The
// deployment's MCP_PRIVATE_NETWORKS are prefixes, and an IPv4 prefix never
// contains a mapped address, so without unmapping an allowed range would be
// refused when spelled ::ffff:10.0.0.5. netip's own predicates already see
// through the mapping; the prefix test does not.
func TestAMappedAddressIsJudgedAsTheAddressItMaps(t *testing.T) {
	allowed := netip.MustParsePrefix("10.0.0.0/8")
	e := newEnv()
	e.allow = func(a netip.Addr) bool { return public(a) || allowed.Contains(a) }

	rec := e.save(t, body("", `{"provider":"searxng","url":"http://[::ffff:10.0.0.5]:8080"}`, false, 100))
	if rec.Code != http.StatusOK {
		t.Errorf("an allowed range written as IPv6: status %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = e.save(t, body("", `{"provider":"searxng","url":"http://[::ffff:127.0.0.1]"}`, false, 100))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("loopback written as IPv6: status %d, want 400", rec.Code)
	}
}

// A key is kept only for the endpoint it was given for. Moving a
// self-hosted instance's URL while leaving the key out would otherwise send
// the stored key to whatever host the new URL names — a way to read a
// write-only secret by pointing the brain at yourself.
func TestMovingSelfHostedFirecrawlNeedsTheKeyAgain(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	url := "http://firecrawl:3002"
	e.stored("firecrawl-oss", &id, &url)

	rec := e.save(t, body("", `{"provider":"firecrawl-oss","url":"https://attacker.example"}`, false, 100))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "search.api_key") {
		t.Errorf("status %d, body %q; want 400 naming search.api_key", rec.Code, rec.Body)
	}
	if len(e.store.upserts) != 0 {
		t.Error("saved a new URL with the old key still attached")
	}
}

// Moving it with an empty key is the explicit "this instance has no key".
func TestMovingSelfHostedFirecrawlWithAnEmptyKeyDropsTheKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	url := "http://firecrawl:3002"
	e.stored("firecrawl-oss", &id, &url)

	got := decode(t, e.save(t, body("",
		`{"provider":"firecrawl-oss","url":"http://firecrawl-2:3002","api_key":""}`, false, 100)))

	if e.store.upserts[0].SearchSecretID != nil || got.Search.KeySet {
		t.Error("the old key followed the instance to its new address")
	}
}
