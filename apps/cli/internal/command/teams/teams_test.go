package teams

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

// The root these tests drive.
var commands = []clitest.Build{New}

const twoTeams = `{
  "items": [
    {"id": "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d", "name": "platform", "role": "admin"},
    {"id": "7a2c9f5f-7e3b-5e2f-8b2f-3c9f5f7e3b5e", "name": "payments"}
  ],
  "next_cursor": "eyJjIjoiMjAyNi0wOC0xM1QwMDowMDowMFoifQ"
}`

// role is absent when the caller is not a member, which is the normal case
// for a super admin — so this is the field most likely to be missing, on the
// listing a super admin is most likely to run. Without a yaml tag carrying
// omitempty it printed as `role: null`, which reads as a member with no role
// rather than as somebody who is not a member.
func TestATeamTheCallerIsNotInHasNoRoleKeyInYAML(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	out, err := clitest.Execute(t, commands, "teams", "list", "-o", "yaml")
	if err != nil {
		t.Fatalf("teams list -o yaml: %v", err)
	}
	if strings.Contains(out, "null") {
		t.Errorf("a team the caller is not in rendered a null role:\n%s", out)
	}

	var got struct {
		Items []struct {
			Name string  `yaml:"name"`
			Role *string `yaml:"role"`
		} `yaml:"items"`
	}
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, out)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].Role == nil || *got.Items[0].Role != "admin" {
		t.Errorf("the role the caller does have was lost: %+v", got.Items[0])
	}
	if got.Items[1].Role != nil {
		t.Errorf("a team the caller is not in carries a role: %v", *got.Items[1].Role)
	}
}

func TestTeamsListRendersATable(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	out, err := clitest.Execute(t, commands, "teams", "list")
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
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	out, err := clitest.Execute(t, commands, "teams", "list", "-o", "json")
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
	got := clitest.BrainStub(t, http.StatusOK, twoTeams)

	if _, err := clitest.Execute(t, commands, "teams", "list"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if strings.Contains(got.Query, "limit") {
		t.Errorf("query = %q, want no limit when the flag was not given", got.Query)
	}
}

func TestTeamsListSendsTheLimitAndCursor(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoTeams)

	if _, err := clitest.Execute(t, commands, "teams", "list", "--limit", "5", "--cursor", "abc"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if !strings.Contains(got.Query, "limit=5") {
		t.Errorf("query = %q, want limit=5", got.Query)
	}
	if !strings.Contains(got.Query, "cursor=abc") {
		t.Errorf("query = %q, want cursor=abc", got.Query)
	}
}

// The cursor is unusable from a terminal unless it is printed, and it must
// never reach a document a parser will read — it is already in the envelope.
func TestTheCursorHintReachesAPersonOnly(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	table, err := clitest.Execute(t, commands, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(table, "--cursor") {
		t.Errorf("the table does not say how to fetch the next page:\n%s", table)
	}

	asJSON, err := clitest.Execute(t, commands, "teams", "list", "-o", "json")
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
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	out, err := clitest.Execute(t, commands, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(out, "not a member") {
		t.Errorf("a team with no role rendered as nothing:\n%s", out)
	}
}

func TestTeamsListWithNothingToShow(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, `{"items": []}`)

	table, err := clitest.Execute(t, commands, "teams", "list")
	if err != nil {
		t.Fatalf("teams list: %v", err)
	}
	if !strings.Contains(table, "no teams") {
		t.Errorf("an empty list said nothing:\n%s", table)
	}

	asJSON, err := clitest.Execute(t, commands, "teams", "list", "-o", "json")
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
	clitest.BrainStub(t, http.StatusForbidden, `{"error": "forbidden"}`)

	out, err := clitest.Execute(t, commands, "teams", "list")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}

	message := out + err.Error()
	if strings.Contains(message, "login") || strings.Contains(message, "log in") {
		t.Errorf("a 403 told the user to log in: %q", message)
	}
}

func TestAnUnauthorisedListSaysHowToFixIt(t *testing.T) {
	clitest.BrainStub(t, http.StatusUnauthorized, `{"error": "unauthorized"}`)

	out, err := clitest.Execute(t, commands, "teams", "list")
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
	clitest.BrainStub(t, http.StatusInternalServerError, `internal server error`)

	out, err := clitest.Execute(t, commands, "teams", "list")
	if err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if !strings.Contains(out+err.Error(), "500") {
		t.Errorf("a 500 did not name the status: %v", err)
	}
}

const oneTeam = `{"id": "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d", "name": "platform"}`

func TestTeamsCreatePostsTheName(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusCreated, oneTeam)

	out, err := clitest.Execute(t, commands, "teams", "create", "platform")
	if err != nil {
		t.Fatalf("teams create: %v", err)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "platform") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if !strings.Contains(got.Body, `"name":"platform"`) {
		t.Errorf("body = %q, want the name", got.Body)
	}
}

// Only a super admin may create a team, so a refusal is the common path here,
// not an edge case. Reporting success on one would have the caller believe a
// team exists that does not.
func TestTeamsCreateReportsARefusal(t *testing.T) {
	clitest.BrainStub(t, http.StatusForbidden, `{"error": "forbidden"}`)

	out, err := clitest.Execute(t, commands, "teams", "create", "platform")
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
	clitest.BrainStub(t, http.StatusConflict, `{"error":"a team called platform already exists"}`)

	_, err := clitest.Execute(t, commands, "teams", "create", "platform")
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
	got := clitest.BrainStub(t, http.StatusCreated, oneTeam)

	if _, err := clitest.Execute(t, commands, "teams", "create", "  platform  "); err != nil {
		t.Fatalf("teams create: %v", err)
	}

	got.Lock()
	if !strings.Contains(got.Body, `"name":"platform"`) {
		t.Errorf("body = %q, want the name trimmed", got.Body)
	}
	got.Unlock()

	blank := clitest.BrainStub(t, http.StatusCreated, oneTeam)
	if _, err := clitest.Execute(t, commands, "teams", "create", "   "); err == nil {
		t.Fatal("a name of only whitespace was accepted")
	}

	blank.Lock()
	defer blank.Unlock()

	if blank.Method != "" {
		t.Errorf("a request was sent for a name that could not be valid: %s %s",
			blank.Method, blank.Path)
	}
}

// The credential has to reach the brain, and it is the one thing a command
// cannot be seen to do without asserting it.
func TestTheTokenIsSent(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoTeams)

	if _, err := clitest.Execute(t, commands, "teams", "list"); err != nil {
		t.Fatalf("teams list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if got.Auth != "Bearer a-token" {
		t.Errorf("Authorization = %q", got.Auth)
	}
}

func TestEveryTeamsSubcommandIsRegistered(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "teams", "--help")
	if err != nil {
		t.Fatalf("teams --help: %v", err)
	}
	for _, verb := range []string{"list", "create", "members"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}

// Every format, one loop. A command that works as a table and crashes as yaml
// is a command nobody tests the second way.
func TestTeamsListWorksInEveryFormat(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoTeams)

	for _, format := range []string{"table", "json", "yaml"} {
		out, err := clitest.Execute(t, commands, "teams", "list", "-o", format)
		if err != nil {
			t.Errorf("teams list -o %s: %v", format, err)
			continue
		}
		if !strings.Contains(out, "platform") {
			t.Errorf("-o %s lost the data:\n%s", format, out)
		}
	}
}
