package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// What the stub saw. The handler runs on the server's goroutine, so every
// field is read under the mutex after the command has returned.
type seen struct {
	mu     sync.Mutex
	method string
	path   string
	query  string
	body   string
	auth   string
}

func (s *seen) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.method = r.Method
	s.path = r.URL.Path
	s.query = r.URL.RawQuery
	s.auth = r.Header.Get("Authorization")

	if r.Body != nil {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		s.body = string(buf[:n])
	}
}

// brainStub points oa at a fake brain and hands back what it received. The
// token comes from the environment so no discovery call is made — that path
// belongs to internal/auth and is tested there.
func brainStub(t *testing.T, status int, response string) *seen {
	t.Helper()

	got := &seen{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	isolate(t)
	t.Setenv("OPENARITY_SERVER", server.URL)
	t.Setenv("OPENARITY_TOKEN", "a-token")

	return got
}

const twoTeams = `{
  "items": [
    {"id": "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d", "name": "platform", "role": "admin"},
    {"id": "7a2c9f5f-7e3b-5e2f-8b2f-3c9f5f7e3b5e", "name": "payments"}
  ],
  "next_cursor": "eyJjIjoiMjAyNi0wOC0xM1QwMDowMDowMFoifQ"
}`

func TestTeamsListRendersATable(t *testing.T) {
	brainStub(t, http.StatusOK, twoTeams)

	out, err := execute(t, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	for _, want := range []string{"platform", "payments", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// The envelope is the contract. Printing the items alone would drop
// next_cursor, and every consumer would believe it had the last page.
func TestTeamsListPrintsTheEnvelope(t *testing.T) {
	brainStub(t, http.StatusOK, twoTeams)

	out, err := execute(t, "teams", "list", "-o", "json")
	if err != nil {
		t.Fatalf("teams list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}

	items, ok := got["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items is not two rows: %#v", got["items"])
	}
	if got["next_cursor"] != "eyJjIjoiMjAyNi0wOC0xM1QwMDowMDowMFoifQ" {
		t.Errorf("next_cursor = %#v, want it carried through verbatim", got["next_cursor"])
	}
}

// Int32Var defaults to zero and the brain answers 400 to a limit of zero, so
// sending it unconditionally would break every default invocation.
func TestTeamsListSendsNoLimitUnlessAsked(t *testing.T) {
	got := brainStub(t, http.StatusOK, twoTeams)

	if _, err := execute(t, "teams", "list"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.mu.Lock()
	defer got.mu.Unlock()

	if strings.Contains(got.query, "limit") {
		t.Errorf("query = %q, want no limit when the flag was not given", got.query)
	}
}

func TestTeamsListSendsTheLimitAndCursor(t *testing.T) {
	got := brainStub(t, http.StatusOK, twoTeams)

	if _, err := execute(t, "teams", "list", "--limit", "5", "--cursor", "abc"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.mu.Lock()
	defer got.mu.Unlock()

	if !strings.Contains(got.query, "limit=5") {
		t.Errorf("query = %q, want limit=5", got.query)
	}
	if !strings.Contains(got.query, "cursor=abc") {
		t.Errorf("query = %q, want cursor=abc", got.query)
	}
}

// The cursor is unusable from a terminal unless it is printed, and it must
// never reach a document a parser will read — it is already in the envelope.
func TestTheCursorHintReachesAPersonOnly(t *testing.T) {
	brainStub(t, http.StatusOK, twoTeams)

	table, err := execute(t, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(table, "--cursor") {
		t.Errorf("the table does not say how to fetch the next page:\n%s", table)
	}

	asJSON, err := execute(t, "teams", "list", "-o", "json")
	if err != nil {
		t.Fatalf("teams list -o json: %v", err)
	}
	if strings.Contains(asJSON, "--cursor") {
		t.Errorf("the hint corrupted the document:\n%s", asJSON)
	}
	if err := json.Unmarshal([]byte(asJSON), &map[string]any{}); err != nil {
		t.Fatalf("the json does not parse: %v", err)
	}
}

// A super admin sees teams they are not in, so a missing role is normal rather
// than an error. An empty cell would read as a bug.
func TestAMissingRoleSaysSo(t *testing.T) {
	brainStub(t, http.StatusOK, twoTeams)

	out, err := execute(t, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(out, "not a member") {
		t.Errorf("a team with no role rendered as nothing:\n%s", out)
	}
}

func TestTeamsListWithNothingToShow(t *testing.T) {
	brainStub(t, http.StatusOK, `{"items": []}`)

	table, err := execute(t, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(table, "no teams") {
		t.Errorf("an empty list said nothing:\n%s", table)
	}

	asJSON, err := execute(t, "teams", "list", "-o", "json")
	if err != nil {
		t.Fatalf("teams list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(asJSON), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, asJSON)
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %#v, want an empty array", got["items"])
	}
}

// 403 means authenticated and not allowed. Telling someone to log in again is
// a loop they cannot escape.
func TestAForbiddenListNeverSuggestsLoggingIn(t *testing.T) {
	brainStub(t, http.StatusForbidden, `{"error": "forbidden"}`)

	out, err := execute(t, "teams", "list")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}

	message := out + err.Error()
	if strings.Contains(message, "login") || strings.Contains(message, "log in") {
		t.Errorf("a 403 told the user to log in: %q", message)
	}
}

func TestAnUnauthorisedListSaysHowToFixIt(t *testing.T) {
	brainStub(t, http.StatusUnauthorized, `{"error": "unauthorized"}`)

	out, err := execute(t, "teams", "list")
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !strings.Contains(out+err.Error(), "oa login") {
		t.Errorf("a 401 did not name the fix: %v", err)
	}
}

// A 500 is the brain's problem, not the caller's, and the status is the only
// thing that tells them apart.
func TestAServerFailureNamesTheStatus(t *testing.T) {
	brainStub(t, http.StatusInternalServerError, `internal server error`)

	out, err := execute(t, "teams", "list")
	if err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if !strings.Contains(out+err.Error(), "500") {
		t.Errorf("a 500 did not name the status: %v", err)
	}
}

const oneTeam = `{"id": "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d", "name": "platform"}`

func TestTeamsCreatePostsTheName(t *testing.T) {
	got := brainStub(t, http.StatusCreated, oneTeam)

	out, err := execute(t, "teams", "create", "platform")
	if err != nil {
		t.Fatalf("teams create: %v", err)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "platform") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}

	got.mu.Lock()
	defer got.mu.Unlock()

	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if !strings.Contains(got.body, `"name":"platform"`) {
		t.Errorf("body = %q, want the name", got.body)
	}
}

// Only a super admin may create a team, so a refusal is the common path here,
// not an edge case. Reporting success on one would have the caller believe a
// team exists that does not.
func TestTeamsCreateReportsARefusal(t *testing.T) {
	brainStub(t, http.StatusForbidden, `{"error": "forbidden"}`)

	out, err := execute(t, "teams", "create", "platform")
	if err == nil {
		t.Fatal("a 403 on create was reported as success")
	}
	if strings.Contains(out, "created") {
		t.Errorf("a refused create printed a confirmation:\n%s", out)
	}
	if strings.Contains(out+err.Error(), "log in") {
		t.Errorf("a 403 told the user to log in: %v", err)
	}
}

// A name already taken is a 409, and the brain's sentence is the useful part —
// the CLI must pass it through rather than replacing it with a status.
func TestTeamsCreateCarriesTheBrainsReason(t *testing.T) {
	brainStub(t, http.StatusConflict, `{"error":"a team called platform already exists"}`)

	_, err := execute(t, "teams", "create", "platform")
	if err == nil {
		t.Fatal("a 409 was reported as success")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

// Whitespace around a pasted name is not part of it, and the brain answers 400
// to a name of only whitespace — better to refuse before the round trip.
func TestTeamsCreateTrimsAndRefusesAnEmptyName(t *testing.T) {
	got := brainStub(t, http.StatusCreated, oneTeam)

	if _, err := execute(t, "teams", "create", "  platform  "); err != nil {
		t.Fatalf("teams create: %v", err)
	}

	got.mu.Lock()
	if !strings.Contains(got.body, `"name":"platform"`) {
		t.Errorf("body = %q, want the name trimmed", got.body)
	}
	got.mu.Unlock()

	blank := brainStub(t, http.StatusCreated, oneTeam)
	if _, err := execute(t, "teams", "create", "   "); err == nil {
		t.Fatal("a name of only whitespace was accepted")
	}

	blank.mu.Lock()
	defer blank.mu.Unlock()

	if blank.method != "" {
		t.Errorf("a request was sent for a name that could not be valid: %s %s",
			blank.method, blank.path)
	}
}

// The credential has to reach the brain, and it is the one thing a command
// cannot be seen to do without asserting it.
func TestTheTokenIsSent(t *testing.T) {
	got := brainStub(t, http.StatusOK, twoTeams)

	if _, err := execute(t, "teams", "list"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.mu.Lock()
	defer got.mu.Unlock()

	if got.auth != "Bearer a-token" {
		t.Errorf("Authorization = %q", got.auth)
	}
}

func TestEveryTeamsSubcommandIsRegistered(t *testing.T) {
	isolate(t)

	out, err := execute(t, "teams", "--help")
	if err != nil {
		t.Fatalf("teams --help: %v", err)
	}
	for _, verb := range []string{"list", "create"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}

// Every format, one loop. A command that works as a table and crashes as yaml
// is a command nobody tests the second way.
func TestTeamsListWorksInEveryFormat(t *testing.T) {
	brainStub(t, http.StatusOK, twoTeams)

	for _, format := range []string{"table", "json", "yaml"} {
		out, err := execute(t, "teams", "list", "-o", format)
		if err != nil {
			t.Errorf("teams list -o %s: %v", format, err)
			continue
		}
		if !strings.Contains(out, "platform") {
			t.Errorf("-o %s lost the data:\n%s", format, out)
		}
	}
}
