package logout

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	cmdcontext "github.com/LaplacianAI/openarity/apps/cli/internal/command/context"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential/store"
)

var commands = []clitest.Build{New, cmdcontext.New}

func seedContext(t *testing.T, name, token string) {
	t.Helper()

	clitest.Seed(t, commands, "context", "create", name, "--server", "https://"+name+".example.com")
	if token == "" {
		return
	}

	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	if err := store.Open(dir).Set(name, credential.Credential{Token: token, Refresh: token + "-refresh"}); err != nil {
		t.Fatalf("seed a credential for %s: %v", name, err)
	}
}

func storedToken(t *testing.T, name string) string {
	t.Helper()

	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	cred, err := store.Open(dir).Get(name)
	if err != nil {
		t.Fatalf("read the credential for %s: %v", name, err)
	}
	return cred.Token
}

func TestLogoutDiscardsTheCredential(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "staging", "an-access-token")

	out, err := clitest.Execute(t, commands, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := storedToken(t, "staging"); got != "" {
		t.Errorf("the credential survived the logout: %q", got)
	}
	if !strings.Contains(out, "logged out") {
		t.Errorf("the command did not report success:\n%s", out)
	}
}

// The whole reason credentials are keyed by context. Logging out of staging
// while testing must not cost the login to production.
func TestLogoutLeavesTheOtherContextsAlone(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "prod", "prod-token")
	seedContext(t, "staging", "staging-token")
	clitest.Seed(t, commands, "context", "use", "staging")

	if _, err := clitest.Execute(t, commands, "logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}

	if got := storedToken(t, "staging"); got != "" {
		t.Errorf("staging is still logged in: %q", got)
	}
	if got := storedToken(t, "prod"); got != "prod-token" {
		t.Errorf("prod's credential was taken too: %q", got)
	}
}

// Running it twice is not an error, and neither is logging out of a context
// that only ever used --token. A non-zero exit for "already done" breaks any
// script that logs out defensively.
func TestLoggingOutTwiceSucceeds(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "staging", "an-access-token")

	if _, err := clitest.Execute(t, commands, "logout"); err != nil {
		t.Fatalf("first logout: %v", err)
	}
	if _, err := clitest.Execute(t, commands, "logout"); err != nil {
		t.Errorf("second logout: %v", err)
	}
}

func TestLoggingOutOfAContextThatNeverLoggedInSucceeds(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "staging", "")

	if _, err := clitest.Execute(t, commands, "logout"); err != nil {
		t.Errorf("logout with no credential: %v", err)
	}
}

// Nothing to log out of, and nothing to name in the message either.
func TestLogoutRefusesWhenThereIsNoContext(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "logout"); err == nil {
		t.Fatal("logging out with no context reported success")
	}
}

// It is the command whose argument is a credential, so it is the one that must
// never echo one.
func TestLogoutNeverPrintsTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	seedContext(t, "staging", secret)

	out, err := clitest.Execute(t, commands, "logout")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the token is in the output:\n%s", out)
	}
}

// A script needs to tell a logout that happened from a command that never ran,
// so the machine formats have to carry something rather than an empty document.
func TestLogoutPrintsAViewUnderJSON(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "staging", "an-access-token")

	out, err := clitest.Execute(t, commands, "logout", "-o", "json")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	for _, want := range []string{`"context"`, `"staging"`, `"server"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the json output does not carry %s:\n%s", want, out)
		}
	}
}
