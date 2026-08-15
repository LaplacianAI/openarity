package context

import (
	"os"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	cmdconfig "github.com/LaplacianAI/openarity/apps/cli/internal/command/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential/store"
)

// Two commands, because several of these assert through `oa config` —
// `config set server` writes to the active context, and `config show`
// resolves it. That coupling is the behaviour under test.
var commands = []clitest.Build{New, cmdconfig.New}

// No command writes a token yet — `oa login` will — so a test that needs one
// seeds it.
//
// The credential goes through the same store the command will read, rather
// than into config.yaml. Writing it by hand into the file would pass whether
// or not the command consults the store at all.
func seedContext(t *testing.T, name, server, token string) {
	t.Helper()

	clitest.Seed(t, commands, "context", "create", name, "--server", server)
	if token == "" {
		return
	}

	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	if err := store.Open(dir).Set(name, credential.Credential{Token: token}); err != nil {
		t.Fatalf("seed a credential for %s: %v", name, err)
	}
}

// Reads back through the store, so a test asserts on what the command would
// actually find rather than on a file it happens to know the shape of.
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

func TestCreateSwitchesToTheNewContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "staging", "--server", "https://staging.example.com")

	saved, _ := config.Load()
	if saved.ActiveName() != "staging" {
		t.Errorf("active context = %q, want staging", saved.ActiveName())
	}
	if saved.Active().Server != "https://staging.example.com" {
		t.Errorf("saved server = %q", saved.Active().Server)
	}
}

// The whole reason contexts exist: two brains, two addresses, neither
// overwriting the other. `config set server` alone could never do this.
func TestContextsDoNotOverwriteEachOther(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")

	saved, _ := config.Load()
	if got := saved.Contexts["local"].Server; got != "http://127.0.0.1:21120" {
		t.Errorf("local server = %q, want it untouched by creating prod", got)
	}
	if got := saved.Contexts["prod"].Server; got != "https://brain.example.com" {
		t.Errorf("prod server = %q", got)
	}
}

// `config set server` edits the active context, not a global. Writing it while
// prod is active must not reach local.
func TestSetServerOnlyTouchesTheActiveContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")
	clitest.Seed(t, commands, "config", "set", "server", "https://elsewhere.example.com")

	saved, _ := config.Load()
	if got := saved.Contexts["local"].Server; got != "http://127.0.0.1:21120" {
		t.Errorf("local server = %q, want it untouched while prod was active", got)
	}
	if got := saved.Contexts["prod"].Server; got != "https://elsewhere.example.com" {
		t.Errorf("prod server = %q, want the write to land on the active context", got)
	}
}

// Silently replacing one would discard a credential, and the message would say
// it succeeded.
func TestCreateRefusesAnExistingName(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")

	out, err := clitest.Execute(t, commands, "context", "create", "prod", "--server", "https://other.example.com")
	if err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	if !strings.Contains(out+err.Error(), "prod") {
		t.Errorf("the message does not name the context: %v", err)
	}

	saved, _ := config.Load()
	if got := saved.Contexts["prod"].Server; got != "https://brain.example.com" {
		t.Errorf("prod server = %q, want the rejected create to have changed nothing", got)
	}
}

// A context with no address resolves to the built-in one, so `create prod`
// with the flag mistyped would make something named prod that talks to
// localhost. Both the missing flag and an empty value have to be refused.
func TestCreateRequiresAServer(t *testing.T) {
	clitest.Isolate(t)

	for _, args := range [][]string{
		{"context", "create", "prod"},
		{"context", "create", "prod", "--server", "  "},
	} {
		if _, err := clitest.Execute(t, commands, args...); err == nil {
			t.Errorf("%v was accepted with no address", args)
		}
	}

	saved, _ := config.Load()
	if len(saved.Contexts) != 0 {
		t.Errorf("a rejected create wrote a context anyway: %+v", saved.Contexts)
	}
}

func TestUseSwitchesTheActiveContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")
	clitest.Seed(t, commands, "context", "use", "local")

	out, err := clitest.Execute(t, commands, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out, "http://127.0.0.1:21120") {
		t.Errorf("the resolved server did not follow the switch:\n%s", out)
	}
}

// A typo here would otherwise write Current pointing at nothing, and every
// later command would quietly fall back to the built-in address.
func TestUseRefusesAnUnknownContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")

	out, err := clitest.Execute(t, commands, "context", "use", "prd")
	if err == nil {
		t.Fatal("an unknown context was accepted")
	}
	if !strings.Contains(out+err.Error(), "prod") {
		t.Errorf("the message does not list what is available: %v", err)
	}

	saved, _ := config.Load()
	if saved.ActiveName() != "prod" {
		t.Errorf("active context = %q, want the rejected switch to have changed nothing", saved.ActiveName())
	}
}

// The whole reason rename exists rather than delete-then-create: the token
// has to survive, or renaming costs you a login.
func TestRenameKeepsTheAddressAndTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_keep_me"
	seedContext(t, "staging", "https://staging.example.com", secret)

	clitest.Seed(t, commands, "context", "rename", "staging", "preprod")

	saved, _ := config.Load()
	if _, ok := saved.Contexts["staging"]; ok {
		t.Error("the old name survived the rename")
	}

	moved := saved.Contexts["preprod"]
	if moved.Server != "https://staging.example.com" {
		t.Errorf("renamed server = %q", moved.Server)
	}

	// The credential lives in the store now, so the config file moving is only
	// half the rename. The other half is what this test was always about.
	if got := storedToken(t, "preprod"); got != secret {
		t.Errorf("the token was lost in the rename: %q", got)
	}
	if got := storedToken(t, "staging"); got != "" {
		t.Errorf("the credential is still readable under the old name: %q", got)
	}
}

// Renaming the active one must carry Current with it, or the rename silently
// deactivates the context you were working in.
func TestRenameFollowsTheActiveContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "staging", "--server", "https://staging.example.com")
	clitest.Seed(t, commands, "context", "rename", "staging", "preprod")

	saved, _ := config.Load()
	if saved.ActiveName() != "preprod" {
		t.Errorf("active context = %q, want it to follow the rename", saved.ActiveName())
	}
}

// Renaming onto a name in use would overwrite that context and its token,
// while reporting success.
func TestRenameRefusesAnExistingName(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "prod", "https://brain.example.com", "oa_live_prod")
	clitest.Seed(t, commands, "context", "create", "staging", "--server", "https://staging.example.com")

	if _, err := clitest.Execute(t, commands, "context", "rename", "staging", "prod"); err == nil {
		t.Fatal("a rename onto an existing context was accepted")
	}

	saved, _ := config.Load()
	if got := storedToken(t, "prod"); got != "oa_live_prod" {
		t.Errorf("prod token = %q, want the rejected rename to have changed nothing", got)
	}
	if _, ok := saved.Contexts["staging"]; !ok {
		t.Error("the rejected rename removed the source context")
	}
}

func TestRenameRefusesAnUnknownContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")

	if _, err := clitest.Execute(t, commands, "context", "rename", "prd", "production"); err == nil {
		t.Fatal("renaming a context that does not exist reported success")
	}
}

func TestRenameRefusesAnUnusableName(t *testing.T) {
	clitest.Isolate(t)

	seedContext(t, "prod", "https://brain.example.com", "oa_live_prod")

	for _, to := range []string{"", "  ", "two words"} {
		if _, err := clitest.Execute(t, commands, "context", "rename", "prod", to); err == nil {
			t.Errorf("%q was accepted as a name", to)
		}
	}

	saved, _ := config.Load()
	if _, ok := saved.Contexts["prod"]; !ok {
		t.Errorf("prod was disturbed by a rejected rename: %+v", saved.Contexts)
	}
	if got := storedToken(t, "prod"); got != "oa_live_prod" {
		t.Errorf("prod's credential was disturbed by a rejected rename: %q", got)
	}
}

func TestDeleteRemovesTheContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")
	clitest.Seed(t, commands, "context", "delete", "prod")

	saved, _ := config.Load()
	if _, ok := saved.Contexts["prod"]; ok {
		t.Error("prod survived delete")
	}
	if _, ok := saved.Contexts["local"]; !ok {
		t.Error("delete took the other context with it")
	}
}

// Deleting the active one must not leave Current naming a context that is
// gone — a dangling pointer resolves to the built-in address, so the next
// command goes somewhere nobody asked for.
func TestDeletingTheActiveContextLeavesNoDanglingPointer(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")
	clitest.Seed(t, commands, "context", "delete", "prod")

	saved, _ := config.Load()
	if saved.Current == "prod" {
		t.Error("Current still names the deleted context")
	}
	if saved.ActiveName() != "local" {
		t.Errorf("active context = %q, want the one that is left", saved.ActiveName())
	}
}

// The credential is the point. A deleted context that leaves its token in the
// file is a secret nobody believes is still there.
func TestDeleteTakesTheTokenWithIt(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_keep"
	seedContext(t, "prod", "https://brain.example.com", secret)

	clitest.Seed(t, commands, "context", "delete", "prod")

	path, _ := config.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), secret) {
		t.Errorf("the token is still in the file:\n%s", data)
	}
}

func TestDeleteRefusesAnUnknownContext(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "context", "delete", "prod"); err == nil {
		t.Fatal("deleting a context that does not exist reported success")
	}
}

func TestListMarksTheActiveContext(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "context", "create", "local", "--server", "http://127.0.0.1:21120")
	clitest.Seed(t, commands, "context", "create", "prod", "--server", "https://brain.example.com")

	out, err := clitest.Execute(t, commands, "context", "list")
	if err != nil {
		t.Fatalf("context list: %v", err)
	}
	for _, want := range []string{"local", "prod", "*"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is missing %q:\n%s", want, out)
		}
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "local") && strings.Contains(line, "*") {
			t.Errorf("the inactive context is marked active:\n%s", out)
		}
	}
}

// `oa context list` must never be the command that prints a credential.
func TestListNeverPrintsTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	seedContext(t, "prod", "https://brain.example.com", secret)

	out, err := clitest.Execute(t, commands, "context", "list")
	if err != nil {
		t.Fatalf("context list: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the token is in the output:\n%s", out)
	}
	if !strings.Contains(out, "token saved") {
		t.Errorf("the listing does not say the context has a credential:\n%s", out)
	}
}

func TestEveryContextSubcommandIsRegistered(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "context", "--help")
	if err != nil {
		t.Fatalf("context --help: %v", err)
	}
	for _, verb := range []string{"list", "use", "create", "rename", "delete"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}
