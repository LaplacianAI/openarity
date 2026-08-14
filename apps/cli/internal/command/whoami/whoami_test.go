package whoami

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

// The root these tests drive.
var commands = []clitest.Build{New}

const devCaller = `{
  "kind": "dev",
  "subject": "dev",
  "issuer": "dev",
  "teams": []
}`

const oidcCaller = `{
  "kind": "oidc",
  "subject": "a1b2c3",
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
