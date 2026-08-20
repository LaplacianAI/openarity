package users

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// openGuard maps every route and changes nothing. What the guard does with a
// denial is tested where the guard lives; these tests are about the handler.
type openGuard struct{}

func (openGuard) Wrap(_ string, next http.HandlerFunc) (http.HandlerFunc, error) {
	return next, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore reproduces the SQL's semantics rather than returning its rows
// unchanged: it filters on subject, applies the cursor, then truncates to
// PageSize. A fake that ignored any of the three would let a broken handler
// pass — the cursor especially, which round-trips through base64 and would
// look fine while selecting nothing.
type fakeStore struct {
	users []db.ListUsersRow
	err   error

	args []db.ListUsersParams
}

func (f *fakeStore) ListUsers(_ context.Context, arg db.ListUsersParams) ([]db.ListUsersRow, error) {
	f.args = append(f.args, arg)
	if f.err != nil {
		return nil, f.err
	}

	rows := slices.Clone(f.users)
	slices.SortFunc(rows, func(a, b db.ListUsersRow) int {
		return compareKey(a.Subject, a.ID, b.Subject, b.ID)
	})

	if arg.UseSubject {
		rows = slices.DeleteFunc(rows, func(u db.ListUsersRow) bool {
			return u.Subject != arg.Subject
		})
	}
	if arg.UseCursor {
		rows = slices.DeleteFunc(rows, func(u db.ListUsersRow) bool {
			return compareKey(u.Subject, u.ID, arg.AfterSubject, arg.AfterID) <= 0
		})
	}
	if int(arg.PageSize) < len(rows) {
		rows = rows[:arg.PageSize]
	}
	return rows, nil
}

// compareKey orders a (text, uuid) pair the way Postgres orders a row
// constructor: the first column decides unless it ties.
func compareKey(a string, aID uuid.UUID, b string, bID uuid.UUID) int {
	if c := strings.Compare(a, b); c != 0 {
		return c
	}
	return strings.Compare(aID.String(), bID.String())
}

// call drives one request through the registered routes with the user already
// on the context, exactly as the middleware chain leaves it. Driving the mux
// rather than the handler puts the pattern under test too.
func call(t *testing.T, s Store, u *auth.User, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s).Register(mux, openGuard{})

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func caller() *auth.User {
	return &auth.User{
		ID:      uuid.New(),
		Issuer:  "dev",
		Subject: "an-admin",
		Teams:   []auth.Membership{{TeamID: uuid.New(), Name: "platform", Role: "admin"}},
	}
}

// One issuer unless a test says otherwise. A row with a blank issuer would
// let a handler that never reads the column pass every test that does not
// look at it.
const testIssuer = "https://auth.example.com/application/o/openarity/"

func row(subject string, email *string) db.ListUsersRow {
	return db.ListUsersRow{ID: uuid.New(), Issuer: testIssuer, Subject: subject, Email: email}
}

func rowAt(issuer, subject string) db.ListUsersRow {
	return db.ListUsersRow{ID: uuid.New(), Issuer: issuer, Subject: subject}
}

func usersPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[user] {
	t.Helper()

	var got api.Page[user]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of users: %v (%s)", err, rec.Body)
	}
	return got
}

// --- the happy path ------------------------------------------------------

func TestListReturnsAPageOfUsers(t *testing.T) {
	t.Parallel()

	email := "alice@example.com"
	alice := row("alice", &email)
	s := &fakeStore{users: []db.ListUsersRow{alice, row("bob", nil)}}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := usersPage(t, rec)
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(got.Items), rec.Body)
	}
	if got.Items[0].ID != alice.ID || got.Items[0].Subject != "alice" {
		t.Errorf("first item = %+v, want alice", got.Items[0])
	}
	if got.Items[0].Email == nil || *got.Items[0].Email != email {
		t.Errorf("email = %v, want %q", got.Items[0].Email, email)
	}
	if got.NextCursor != nil {
		t.Errorf("a complete page carries a cursor: %q", *got.NextCursor)
	}
}

// The whole reason the route exists: resolving a name to an id. The filter has
// to reach the store as an exact match rather than being applied afterwards,
// or a page of 50 becomes the search space.
func TestSubjectIsPassedToTheStoreAsAnExactFilter(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{row("alice", nil), row("alice-two", nil), row("bob", nil)}}

	rec := call(t, s, caller(), http.MethodGet, "/users?subject=alice")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(s.args) != 1 {
		t.Fatalf("the store was asked %d times", len(s.args))
	}
	if !s.args[0].UseSubject || s.args[0].Subject != "alice" {
		t.Errorf("params = %+v, want an exact filter on alice", s.args[0])
	}

	got := usersPage(t, rec)
	if len(got.Items) != 1 || got.Items[0].Subject != "alice" {
		t.Errorf("items = %+v, want alice alone — alice-two must not match a prefix", got.Items)
	}
}

// An unknown subject is an empty page, not a 404. A 404 would make the route
// answer "does this person exist" with a status code, which is the thing
// restricting it to admins is meant to avoid leaking cheaply.
func TestAnUnknownSubjectIsAnEmptyPage(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{row("alice", nil)}}

	rec := call(t, s, caller(), http.MethodGet, "/users?subject=nobody")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := usersPage(t, rec); len(got.Items) != 0 {
		t.Errorf("items = %+v, want none", got.Items)
	}
}

// Absent and blank must mean the same thing. Treating "   " as a filter
// returns an empty page while `?subject=` alone returns everyone, which is two
// spellings of one intent answering differently.
func TestABlankSubjectIsNotAFilter(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/users", "/users?subject=", "/users?subject=%20%20"} {
		s := &fakeStore{users: []db.ListUsersRow{row("alice", nil), row("bob", nil)}}

		rec := call(t, s, caller(), http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200: %s", path, rec.Code, rec.Body)
		}
		if s.args[0].UseSubject {
			t.Errorf("%s: params = %+v, want no filter", path, s.args[0])
		}
		if got := usersPage(t, rec); len(got.Items) != 2 {
			t.Errorf("%s: items = %d, want both", path, len(got.Items))
		}
	}
}

// A fresh deployment has one user in it. `[]` and `null` are different answers
// to a client, and `jq length` fails on the second.
func TestAnEmptyDirectoryIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	rec := call(t, &fakeStore{}, caller(), http.MethodGet, "/users")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("an empty page serialised as %s, want items:[]", body)
	}
}

// The provider released no email. The key has to be absent rather than null —
// omitempty on a pointer is what distinguishes "we have none" from "we have
// an empty one".
func TestAMissingEmailIsOmitted(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{row("alice", nil)}}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	if strings.Contains(rec.Body.String(), "email") {
		t.Errorf("a user with no email carries the key: %s", rec.Body)
	}
}

// --- authorisation -------------------------------------------------------
//
// Which permission this route requires is a row in route_permissions, and the
// guard applies it before the handler is reached. Both live in
// internal/api/authorize_test.go now; asserting them here would test the fake
// guard this file installs.

// --- paging --------------------------------------------------------------

// One row more than the limit is read so the cursor can be decided without a
// COUNT. The extra row must not reach the client.
func TestAFullPageCarriesACursorAndNotTheExtraRow(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{
		row("alice", nil), row("bob", nil), row("carol", nil),
	}}

	rec := call(t, s, caller(), http.MethodGet, "/users?limit=2")

	got := usersPage(t, rec)
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(got.Items), rec.Body)
	}
	if got.NextCursor == nil {
		t.Fatal("a full page carries no cursor, so paging stops early")
	}
	if s.args[0].PageSize != 3 {
		t.Errorf("PageSize = %d, want limit+1", s.args[0].PageSize)
	}
}

// The cursor is opaque to the client and has to survive the round trip. This
// is the test that fails when the cursor is built from a column the ordering
// does not use.
func TestPagingWalksEveryUserExactlyOnce(t *testing.T) {
	t.Parallel()

	rows := []db.ListUsersRow{
		row("alice", nil), row("bob", nil), row("carol", nil), row("dave", nil), row("erin", nil),
	}
	s := &fakeStore{users: rows}

	seen := make([]string, 0, len(rows))
	path := "/users?limit=2"
	for range 10 {
		rec := call(t, s, caller(), http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}

		page := usersPage(t, rec)
		for _, u := range page.Items {
			seen = append(seen, u.Subject)
		}
		if page.NextCursor == nil {
			break
		}
		path = "/users?limit=2&cursor=" + *page.NextCursor
	}

	want := []string{"alice", "bob", "carol", "dave", "erin"}
	if !slices.Equal(seen, want) {
		t.Errorf("walked %v, want %v", seen, want)
	}
}

// A cursor survives the subject filter, because they are independent
// predicates. Dropping the filter on the second page would silently widen the
// search halfway through.
func TestACursorKeepsTheSubjectFilter(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{row("alice", nil), row("bob", nil)}}
	cursor, err := api.EncodeCursor(userCursor{Subject: "aaa", ID: uuid.Nil})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}

	call(t, s, caller(), http.MethodGet, "/users?subject=alice&cursor="+cursor)

	if !s.args[0].UseSubject || s.args[0].Subject != "alice" {
		t.Errorf("params = %+v, want the filter kept alongside the cursor", s.args[0])
	}
	if !s.args[0].UseCursor {
		t.Errorf("params = %+v, want the cursor applied", s.args[0])
	}
}

// A mangled cursor is a 400, never a silent restart from the top — that turns
// a client's paging loop into an infinite one.
func TestAMangledCursorIsABadRequest(t *testing.T) {
	t.Parallel()

	for _, cursor := range []string{"not-base64!!", "bm90LWpzb24", "eyJzIjo0Mn0"} {
		s := &fakeStore{users: []db.ListUsersRow{row("alice", nil)}}

		rec := call(t, s, caller(), http.MethodGet, "/users?cursor="+cursor)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cursor %q: status = %d, want 400: %s", cursor, rec.Code, rec.Body)
		}
		if len(s.args) != 0 {
			t.Errorf("cursor %q: the store was read despite a bad cursor", cursor)
		}
	}
}

// --- the contract --------------------------------------------------------

// Fails when the response struct grows a field, rather than when a client
// notices. issuer is the one worth guarding: it is on the row, it is half of
// identity, and publishing it would enumerate the providers this deployment
// trusts.
func TestTheResponseCarriesOnlyContractedFields(t *testing.T) {
	t.Parallel()

	email := "alice@example.com"
	s := &fakeStore{users: []db.ListUsersRow{row("alice", &email)}}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not an object: %v (%s)", err, rec.Body)
	}
	for key := range envelope {
		if key != "items" && key != "next_cursor" {
			t.Errorf("the envelope carries an uncontracted key %q", key)
		}
	}

	items, ok := envelope["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v", envelope["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("the item is not an object: %v", items[0])
	}
	for key := range first {
		if key != "id" && key != "issuer" && key != "subject" && key != "email" {
			t.Errorf("a user carries an uncontracted key %q", key)
		}
	}
}

// The issuer is required in the spec, and it is the half of the identity that
// makes two rows with one subject two different people. Dropped, a listing
// cannot be read: the duplicated subjects look like a bug in the upsert.
func TestEveryUserCarriesItsIssuer(t *testing.T) {
	t.Parallel()

	s := &fakeStore{users: []db.ListUsersRow{row("alice", nil)}}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	got := usersPage(t, rec)
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	if got.Items[0].Issuer != testIssuer {
		t.Errorf("issuer = %q, want %q", got.Items[0].Issuer, testIssuer)
	}
}

// The case that sent somebody to psql: one provider reached under two URLs
// leaves the same person under both, and the subject alone cannot tell them
// apart. Two rows, same subject, and the issuer is the only thing that
// distinguishes them.
func TestTwoIssuersWithOneSubjectStayDistinguishable(t *testing.T) {
	t.Parallel()

	const (
		byName = "https://auth.example.com/application/o/openarity/"
		byIP   = "http://10.0.0.5:9000/application/o/openarity/"
	)
	s := &fakeStore{users: []db.ListUsersRow{
		rowAt(byName, "akadmin"),
		rowAt(byIP, "akadmin"),
	}}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	got := usersPage(t, rec)
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want both rows", len(got.Items))
	}

	issuers := map[string]bool{}
	for _, u := range got.Items {
		if u.Subject != "akadmin" {
			t.Errorf("subject = %q", u.Subject)
		}
		issuers[u.Issuer] = true
	}
	for _, want := range []string{byName, byIP} {
		if !issuers[want] {
			t.Errorf("the response cannot distinguish the rows — %q is missing: %s", want, rec.Body)
		}
	}
}

func TestTheRouteAnswersOnlyGet(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		s := &fakeStore{}
		rec := call(t, s, caller(), method, "/users")

		if rec.Code == http.StatusOK {
			t.Errorf("%s /users answered 200", method)
		}
		if len(s.args) != 0 {
			t.Errorf("%s /users reached the store", method)
		}
	}
}

// A partial read is not a short page. Returning the rows it managed to get
// alongside a nil error would hand a caller a directory missing whoever they
// were looking for.
func TestAStoreFailureIsA500(t *testing.T) {
	t.Parallel()

	s := &fakeStore{err: errors.New("connection refused")}

	rec := call(t, s, caller(), http.MethodGet, "/users")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the reply leaks the underlying error: %s", rec.Body)
	}
}

// A limit above the maximum is clamped rather than refused, and a nonsense one
// is a 400. Both are api.Limit's job; this asserts the route actually calls it.
func TestTheLimitIsClampedAndValidated(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	if rec := call(t, s, caller(), http.MethodGet, "/users?limit=5000"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if s.args[0].PageSize != api.MaxLimit+1 {
		t.Errorf("PageSize = %d, want the maximum plus one", s.args[0].PageSize)
	}

	for _, limit := range []string{"0", "-1", "abc"} {
		bad := &fakeStore{}
		rec := call(t, bad, caller(), http.MethodGet, "/users?limit="+limit)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit %q: status = %d, want 400", limit, rec.Code)
		}
		if len(bad.args) != 0 {
			t.Errorf("limit %q: the store was read", limit)
		}
	}
}
