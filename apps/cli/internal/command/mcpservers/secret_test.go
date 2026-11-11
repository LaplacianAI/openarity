package mcpservers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const secretRoute = "PUT /teams/{id}/mcp-secrets/{name}"

func TestSecretWritesTheValueFromStdinAndNeverPrintsIt(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{secretRoute: {Status: http.StatusNoContent}})

	out, err := clitest.ExecuteWithStdin(t, commands, "sk-live-123\n", "mcp-servers", "secret", teamID, "stripe", "token")
	if err != nil {
		t.Fatalf("secret: %v\n%s", err, out)
	}
	got := script.All()[0]
	if !strings.HasSuffix(got.Path, "/mcp-secrets/stripe") || got.Body != `{"values":{"token":"sk-live-123"}}` {
		t.Errorf("sent %s %s; want the value, its newline trimmed, under the name", got.Path, got.Body)
	}
	if strings.Contains(out, "sk-live-123") || !strings.Contains(out, "stripe#token") {
		t.Errorf("output %q; want the reference and never the value", out)
	}
}

func TestSecretWithNothingOnStdinSendsNothing(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{secretRoute: {Status: http.StatusNoContent}})

	for name, in := range map[string]string{"empty": "", "a newline": "\n", "too long": strings.Repeat("x", maxSecretValue+1)} {
		if _, err := clitest.ExecuteWithStdin(t, commands, in, "mcp-servers", "secret", teamID, "stripe", "token"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestSecretRefusedNamesThePermission(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{secretRoute: {Status: http.StatusForbidden, Body: "forbidden"}})

	_, err := clitest.ExecuteWithStdin(t, commands, "v", "mcp-servers", "secret", teamID, "stripe", "token")
	if err == nil || !strings.Contains(err.Error(), "tool:write") {
		t.Errorf("err = %v, want it to name tool:write", err)
	}
}
