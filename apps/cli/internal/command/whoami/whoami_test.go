package whoami

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

// id is required in the spec and always present — every principal, a
// development token included, is resolved to a row on first sight.
const devCaller = `{
  "kind": "dev",
  "subject": "dev",
  "issuer": "dev",
  "id": "3f8e1c2d-4b5a-4c6d-8e9f-0a1b2c3d4e5f",
  "teams": []
}`

const oidcCaller = `{
  "kind": "oidc",
  "subject": "a1b2c3",
  "id": "3f8e1c2d-4b5a-4c6d-8e9f-0a1b2c3d4e5f",
  "issuer": "https://auth.example.com/application/o/openarity/",
  "email": "someone@example.com",
  "teams": [
    {"id": "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d", "name": "platform", "role": "admin"},
    {"id": "7a2c9f5f-7e3b-5e2f-8b2f-3c9f5f7e3b5e", "name": "payments", "role": "developer"}
  ]
}`

// The quickest check that a login worked, so it has to name who you are and
// where that came from.
func TestWhoamiShowsTheCaller(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}

	for _, want := range []string{"oidc", "a1b2c3", "auth.example.com", "someone@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output is missing %q:\n%s", want, out)
		}
	}
}

// The only way to see which teams you are in and with what role — that is what
// the command is for, and a name without its role answers half the question.
func TestWhoamiShowsEveryTeamWithItsRole(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}

	for _, want := range []string{"platform", "admin", "payments", "developer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output is missing %q:\n%s", want, out)
		}
	}
}

// Belonging to nothing is the normal state on a fresh brain. A blank line
// there reads as a failed request rather than an empty set.
func TestNoTeamsSaysNone(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, devCaller)

	out, err := clitest.Execute(t, commands, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("a caller in no teams rendered as nothing:\n%s", out)
	}
}

// Email and issuer are absent when the provider released neither. Printing an
// empty row would look like the brain returned a blank value.
func TestAbsentFieldsAreOmitted(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, `{"kind":"dev","subject":"dev","teams":[]}`)

	out, err := clitest.Execute(t, commands, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, absent := range []string{"issuer", "email"} {
		if strings.Contains(out, absent) {
			t.Errorf("%q was printed with no value:\n%s", absent, out)
		}
	}
}

// -o is a persistent flag on the root, so every command carries it whether or
// not it honours it. whoami wrote through tabwriter and ignored it, which is
// worse than not offering it: a script asking for json got a padded table and
// no error.
func TestWhoamiHonoursTheOutputFormat(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami", "-o", "json")
	if err != nil {
		t.Fatalf("whoami -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	for field, want := range map[string]string{"kind": "oidc", "subject": "a1b2c3"} {
		if got[field] != want {
			t.Errorf("%s = %v, want %q", field, got[field], want)
		}
	}
}

// The reason the id was added to the spec in the first place. Reading it off a
// table means parsing columns; this is the output a script uses.
func TestTheCallersOwnIDIsMachineReadable(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami", "-o", "json")
	if err != nil {
		t.Fatalf("whoami -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got["id"] != "3f8e1c2d-4b5a-4c6d-8e9f-0a1b2c3d4e5f" {
		t.Errorf("id = %v", got["id"])
	}
}

// Teams is the field a script iterates, and it has to survive the round trip
// as structured data rather than as the "name (role)" the table renders.
func TestEveryTeamSurvivesAsAnObject(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami", "-o", "json")
	if err != nil {
		t.Fatalf("whoami -o json: %v", err)
	}

	var got struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if len(got.Teams) != 2 {
		t.Fatalf("teams = %d, want 2:\n%s", len(got.Teams), out)
	}
	if got.Teams[0].Name != "platform" || got.Teams[0].Role != "admin" {
		t.Errorf("first team = %+v", got.Teams[0])
	}
	if got.Teams[0].ID == "" {
		t.Error("the team id was dropped — it is what `oa teams` takes")
	}
}

// A nil slice marshals to null, and `jq '.teams | length'` fails on null.
// Belonging to no teams is the normal state on a fresh brain, so this is the
// first thing a new person's script would hit.
func TestNoTeamsIsAnEmptyArrayAndNotNull(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, devCaller)

	out, err := clitest.Execute(t, commands, "whoami", "-o", "json")
	if err != nil {
		t.Fatalf("whoami -o json: %v", err)
	}
	if strings.Contains(out, "null") {
		t.Errorf("a caller in no teams produced null:\n%s", out)
	}

	var got struct {
		Teams []any `json:"teams"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got.Teams == nil {
		t.Errorf("teams is null rather than []:\n%s", out)
	}
}

// yaml.v3 does not read json tags, so a field carrying omitempty for json
// alone is dropped from json and printed as `issuer: null` in yaml. Absent
// has to mean absent in both.
func TestAbsentFieldsAreOmittedFromEveryFormat(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			clitest.BrainStub(t, http.StatusOK, `{"kind":"dev","subject":"dev",`+
				`"id":"3f8e1c2d-4b5a-4c6d-8e9f-0a1b2c3d4e5f","teams":[]}`)

			out, err := clitest.Execute(t, commands, "whoami", "-o", format)
			if err != nil {
				t.Fatalf("whoami -o %s: %v", format, err)
			}
			for _, absent := range []string{"issuer", "email", "null"} {
				if strings.Contains(out, absent) {
					t.Errorf("%s output carries %q:\n%s", format, absent, out)
				}
			}
		})
	}
}

// Colour would land inside a json string and the consumer would fail to parse.
// The table branch is the only one that may style anything.
func TestNoStructuredFormatCarriesEscapeSequences(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		clitest.BrainStub(t, http.StatusOK, oidcCaller)

		out, err := clitest.Execute(t, commands, "whoami", "-o", format)
		if err != nil {
			t.Fatalf("whoami -o %s: %v", format, err)
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s carries an escape sequence: %q", format, out)
		}
	}
}

// The default is still a table, and it is what somebody reads. Switching the
// renderer must not turn the labelled rows into something else.
func TestTheDefaultIsStillAReadableTable(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("the default output is json:\n%s", out)
	}
	for _, want := range []string{"kind", "subject", "teams", "platform", "(admin)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// yaml is the format someone reads and edits, so the teams have to come back
// as a list of mappings rather than as rendered text.
func TestYAMLKeepsTheTeamsStructured(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, oidcCaller)

	out, err := clitest.Execute(t, commands, "whoami", "-o", "yaml")
	if err != nil {
		t.Fatalf("whoami -o yaml: %v", err)
	}

	var got struct {
		Subject string `yaml:"subject"`
		Teams   []struct {
			Name string `yaml:"name"`
			Role string `yaml:"role"`
		} `yaml:"teams"`
	}
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, out)
	}
	if got.Subject != "a1b2c3" {
		t.Errorf("subject = %q", got.Subject)
	}
	if len(got.Teams) != 2 || got.Teams[1].Role != "developer" {
		t.Errorf("teams = %+v", got.Teams)
	}
}

// This is the command someone runs to check a login, so the failure has to say
// the credential is the problem.
func TestAnUnauthenticatedCallerIsToldToLogIn(t *testing.T) {
	clitest.BrainStub(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)

	out, err := clitest.Execute(t, commands, "whoami")
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !strings.Contains(out+err.Error(), "oa login") {
		t.Errorf("a 401 did not name the fix: %v", err)
	}
}

// Authenticated and not allowed is a different problem, and telling someone to
// log in again is a loop they cannot escape.
func TestAForbiddenCallerIsNotToldToLogIn(t *testing.T) {
	clitest.BrainStub(t, http.StatusForbidden, `{"error":"forbidden"}`)

	out, err := clitest.Execute(t, commands, "whoami")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if strings.Contains(out+err.Error(), "log in") {
		t.Errorf("a 403 told the user to log in: %v", err)
	}
}

func TestAServerFailureNamesTheStatus(t *testing.T) {
	clitest.BrainStub(t, http.StatusInternalServerError, `internal server error`)

	out, err := clitest.Execute(t, commands, "whoami")
	if err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if !strings.Contains(out+err.Error(), "500") {
		t.Errorf("a 500 did not name the status: %v", err)
	}
}

// The credential has to reach the brain, and asking who you are is exactly the
// request where sending nothing would still look plausible.
func TestTheTokenIsSent(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, devCaller)

	if _, err := clitest.Execute(t, commands, "whoami"); err != nil {
		t.Fatalf("whoami: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if got.Auth != "Bearer a-token" {
		t.Errorf("Authorization = %q", got.Auth)
	}
	if got.Path != "/whoami" {
		t.Errorf("path = %q", got.Path)
	}
}

// A response the client cannot parse must not be reported as an empty caller.
func TestAMalformedResponseIsAnError(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, `not json at all`)

	if _, err := clitest.Execute(t, commands, "whoami"); err == nil {
		t.Fatal("an unparseable body was reported as success")
	}
}
