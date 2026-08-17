package users

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	aliceID = "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d"
	bobID   = "0f1e2d3c-4b5a-4968-8776-655443332211"
)

const anIssuer = "https://auth.example.com/application/o/openarity/"

const twoUsers = `{
  "items": [
    {"id": "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d", "subject": "alice",
     "issuer": "https://auth.example.com/application/o/openarity/",
     "email": "alice@example.com"},
    {"id": "0f1e2d3c-4b5a-4968-8776-655443332211", "subject": "bob",
     "issuer": "https://auth.example.com/application/o/openarity/"}
  ],
  "next_cursor": "eyJzIjoiYm9iIn0"
}`

// One provider reached under two URLs. The subjects are identical, so the
// issuer is the only thing that tells the rows apart — and it is exactly the
// listing that sent somebody to psql to read their own user table.
const oneSubjectTwoIssuers = `{
  "items": [
    {"id": "47c93149-6e9e-49ea-a9f6-34011f1e1e04", "subject": "akadmin",
     "issuer": "http://10.0.0.5:9000/application/o/openarity/"},
    {"id": "e096f85a-a05b-4699-919a-e0c5c5b57072", "subject": "akadmin",
     "issuer": "https://auth.example.com/application/o/openarity/"}
  ]
}`

func TestUsersListRendersATable(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list")
	if err != nil {
		t.Fatalf("users list: %v", err)
	}
	for _, want := range []string{"alice", "alice@example.com", aliceID, "bob", bobID} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// The id is the reason the command exists — it is what gets pasted into a
// membership request. A table that showed only subjects would be useless.
func TestTheIDIsAlwaysShown(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list", "alice")
	if err != nil {
		t.Fatalf("users list alice: %v", err)
	}
	if !strings.Contains(out, aliceID) {
		t.Errorf("no id in the output, which is the whole point:\n%s", out)
	}
}

// The subject is a positional, and it has to reach the query string. Sent as
// anything else it silently lists everybody and the first row looks like a
// match.
func TestASubjectIsSentAsAnExactFilter(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	if _, err := clitest.Execute(t, commands, "users", "list", "alice"); err != nil {
		t.Fatalf("users list alice: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if got.Path != "/users" {
		t.Errorf("path = %q", got.Path)
	}
	if !strings.Contains(got.Query, "subject=alice") {
		t.Errorf("query = %q, want subject=alice", got.Query)
	}
}

func TestNoArgumentSendsNoSubject(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	if _, err := clitest.Execute(t, commands, "users", "list"); err != nil {
		t.Fatalf("users list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if strings.Contains(got.Query, "subject") {
		t.Errorf("query = %q, want no subject when none was given", got.Query)
	}
}

// A blank argument is a shell accident — `oa users list "$NAME"` with NAME
// unset. Sending it would list everybody, which is the opposite of what was
// asked for.
func TestABlankSubjectIsRefusedWithoutARequest(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	if _, err := clitest.Execute(t, commands, "users", "list", "   "); err == nil {
		t.Fatal("a blank subject was accepted")
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != "" {
		t.Errorf("a request went out for a blank subject: %s %s", got.Method, got.Path)
	}
}

// The envelope is the contract. Printing the items alone would drop
// next_cursor and every consumer would believe it had the last page.
func TestUsersListPrintsTheEnvelope(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list", "-o", "json")
	if err != nil {
		t.Fatalf("users list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 2 {
		t.Fatalf("items is not two rows: %#v", got["items"])
	}
	if got["next_cursor"] != "eyJzIjoiYm9iIn0" {
		t.Errorf("next_cursor = %#v, want it carried through verbatim", got["next_cursor"])
	}
}

// Two rows reading `akadmin` with different ids and nothing else on the line
// is unreadable — it looks like the brain created a duplicate rather than like
// two distinct principals, which is what they are.
func TestTheTableDistinguishesOneSubjectAtTwoIssuers(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oneSubjectTwoIssuers)

	out, err := clitest.Execute(t, commands, "users", "list")
	if err != nil {
		t.Fatalf("users list: %v", err)
	}

	for _, want := range []string{
		"http://10.0.0.5:9000/application/o/openarity/",
		"https://auth.example.com/application/o/openarity/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table cannot tell the rows apart — %q is missing:\n%s", want, out)
		}
	}

	// Each issuer belongs on the line of the person it authenticated, not
	// printed once as a heading.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "47c93149") && !strings.Contains(line, "10.0.0.5") {
			t.Errorf("the row is not on the same line as its issuer:\n%s", out)
		}
	}
}

// The issuer is a url and the table is column-aligned, so a row that loses its
// id or its email to the new column is a silent regression.
func TestEveryColumnSurvivesTheIssuer(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list")
	if err != nil {
		t.Fatalf("users list: %v", err)
	}
	for _, want := range []string{"alice", anIssuer, "alice@example.com", aliceID, "bob", "no email", bobID} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// The two structured formats have to describe the same thing. yaml.v3 does
// not read json tags, so without yaml tags on the generated types it falls
// back to lowercasing the Go field name and ignores omitempty — one command
// answering `user_id` in json and `userid` in yaml, and omitting an absent
// email from one while printing `email: null` in the other.
//
// The tags come from oapi-codegen.yaml rather than from any file here, which
// is why this is asserted through a command: nothing else fails if that
// setting is removed.
func TestJSONAndYAMLDescribeTheSameUsers(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	asJSON, err := clitest.Execute(t, commands, "users", "list", "-o", "json")
	if err != nil {
		t.Fatalf("users list -o json: %v", err)
	}

	clitest.BrainStub(t, http.StatusOK, twoUsers)
	asYAML, err := clitest.Execute(t, commands, "users", "list", "-o", "yaml")
	if err != nil {
		t.Fatalf("users list -o yaml: %v", err)
	}

	var fromJSON, fromYAML map[string]any
	if err := json.Unmarshal([]byte(asJSON), &fromJSON); err != nil {
		t.Fatalf("not json: %v\n%s", err, asJSON)
	}
	if err := yaml.Unmarshal([]byte(asYAML), &fromYAML); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, asYAML)
	}

	if !reflect.DeepEqual(fromJSON, fromYAML) {
		t.Errorf("the two formats disagree\njson:\n%s\nyaml:\n%s", asJSON, asYAML)
	}
}

// bob has no email, and absent has to mean absent in both formats. `null` is
// a value: a consumer testing for the key finds one and reads nothing.
func TestAnAbsentEmailIsOmittedFromYAMLToo(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list", "-o", "yaml")
	if err != nil {
		t.Fatalf("users list -o yaml: %v", err)
	}
	if strings.Contains(out, "null") {
		t.Errorf("a user with no email rendered as null:\n%s", out)
	}
}

// Paging a search must keep the search. Without the subject in the hint, page
// two silently widens to the whole directory.
func TestTheCursorHintKeepsTheSubject(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list", "alice")
	if err != nil {
		t.Fatalf("users list alice: %v", err)
	}
	if !strings.Contains(out, "--cursor") {
		t.Errorf("no hint about the next page:\n%s", out)
	}
	if !strings.Contains(out, "oa users list alice") {
		t.Errorf("the hint drops the subject, so page two lists everybody:\n%s", out)
	}

	asJSON, err := clitest.Execute(t, commands, "users", "list", "alice", "-o", "json")
	if err != nil {
		t.Fatalf("users list -o json: %v", err)
	}
	if strings.Contains(asJSON, "--cursor") {
		t.Errorf("the hint corrupted the document:\n%s", asJSON)
	}
}

// A provider that released no email is normal. An empty cell reads as a bug in
// the CLI rather than a fact about the account.
func TestAUserWithNoEmailSaysSo(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoUsers)

	out, err := clitest.Execute(t, commands, "users", "list")
	if err != nil {
		t.Fatalf("users list: %v", err)
	}
	if !strings.Contains(out, "no email") {
		t.Errorf("a user with no email rendered as nothing:\n%s", out)
	}
}

// Nobody matched is the common answer for a name that has never logged in, and
// the message has to say why rather than leaving somebody retyping it.
func TestNothingFoundSaysWhy(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, `{"items": []}`)

	table, err := clitest.Execute(t, commands, "users", "list", "nobody")
	if err != nil {
		t.Fatalf("users list nobody: %v", err)
	}
	if !strings.Contains(table, "oa login") {
		t.Errorf("an empty result did not say how a user comes to exist:\n%s", table)
	}

	asJSON, err := clitest.Execute(t, commands, "users", "list", "nobody", "-o", "json")
	if err != nil {
		t.Fatalf("users list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(asJSON), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, asJSON)
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %#v, want an empty array rather than null", got["items"])
	}
}

// Int32Var defaults to zero and the brain answers 400 to a limit of zero, so
// sending it unconditionally would break every default invocation.
func TestUsersListSendsNoLimitUnlessAsked(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	if _, err := clitest.Execute(t, commands, "users", "list"); err != nil {
		t.Fatalf("users list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if strings.Contains(got.Query, "limit") {
		t.Errorf("query = %q, want no limit when the flag was not given", got.Query)
	}
}

func TestUsersListSendsTheLimitAndCursor(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	_, err := clitest.Execute(t, commands, "users", "list", "--limit", "5", "--cursor", "abc")
	if err != nil {
		t.Fatalf("users list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	for _, want := range []string{"limit=5", "cursor=abc"} {
		if !strings.Contains(got.Query, want) {
			t.Errorf("query = %q, want %s", got.Query, want)
		}
	}
}

// A caller without membership:write anywhere is refused by the brain. The CLI
// must not report that as an empty directory.
func TestAForbiddenListIsAnError(t *testing.T) {
	clitest.BrainStub(t, http.StatusForbidden, "forbidden")

	out, err := clitest.Execute(t, commands, "users", "list")
	if err == nil {
		t.Fatalf("a 403 was reported as success:\n%s", out)
	}
	if strings.Contains(err.Error(), "oa login") {
		t.Errorf("a 403 told the caller to log in again, which is a loop: %v", err)
	}
}

// Two arguments is a mistyped command, not a search for two people. Ignoring
// the second would search for the first and look like it worked.
func TestASecondArgumentIsRefused(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoUsers)

	if _, err := clitest.Execute(t, commands, "users", "list", "alice", "bob"); err == nil {
		t.Fatal("a second argument was accepted")
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != "" {
		t.Errorf("a request went out for a malformed command: %s", got.Method)
	}
}
