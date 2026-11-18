package mcpservers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const mailRoom = `{"id":"7b6f0c1e-1111-4222-8333-944455556666","team_id":"3f0c9a52-1111-4222-8333-944455556666",
  "name":"mail","command":["npx","-y","mcp-mail-server"],"env":{"IMAP_PASS":"teams/t/mcp/mail#pass"},
  "vars":{"IMAP_PORT":"993","IMAP_HOST":"imap.gmail.com"},
  "egress":[{"host":"imap.gmail.com","port":993},{"host":"registry.npmjs.org","port":443}],
  "bare":false,"auth_kind":"none","created_at":"2026-11-18T09:00:00Z","updated_at":"2026-11-18T09:00:00Z"}`

func TestCreatingACommandServerSendsItsVarsAndEgress(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers": {Status: http.StatusCreated, Body: mailRoom},
	}))

	_, err := clitest.Execute(t, commands, "mcp-servers", "create", "platform", "mail",
		"--var", "IMAP_HOST=imap.gmail.com", "--var", "GREETING=a=b ",
		"--egress", "imap.gmail.com:993", "--egress", " registry.npmjs.org:443",
		"--", "npx", "-y", "mcp-mail-server")
	if err != nil {
		t.Fatal(err)
	}
	body := sent(t, script, http.MethodPost)
	vars, _ := body["vars"].(map[string]any)
	// A value is the person's, spaces and = included; only the name is trimmed.
	if vars["IMAP_HOST"] != "imap.gmail.com" || vars["GREETING"] != "a=b " {
		t.Errorf("vars = %v", body["vars"])
	}
	egress, _ := body["egress"].([]any)
	if len(egress) != 2 {
		t.Fatalf("egress = %v", body["egress"])
	}
	second, _ := egress[1].(map[string]any)
	if second["host"] != "registry.npmjs.org" || second["port"] != float64(443) {
		t.Errorf("egress[1] = %v", egress[1])
	}
}

// Left out, they are absent rather than empty, as a request before them was.
func TestACommandServerWithoutVarsSendsNone(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers": {Status: http.StatusCreated, Body: mailRoom},
	}))

	if _, err := clitest.Execute(t, commands, "mcp-servers", "create", "platform", "mail", "--", "npx"); err != nil {
		t.Fatal(err)
	}
	body := sent(t, script, http.MethodPost)
	for _, k := range []string{"vars", "egress"} {
		if _, ok := body[k]; ok {
			t.Errorf("sent %s = %v", k, body[k])
		}
	}
}

func TestAServerShowsItsVarsInOrderAndItsEgress(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: mailRoom},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "get", "platform", "7b6f0c1e-1111-4222-8333-944455556666")
	if err != nil {
		t.Fatal(err)
	}
	host, port := strings.Index(out, "IMAP_HOST"), strings.Index(out, "IMAP_PORT")
	if host < 0 || port < 0 || host > port {
		t.Errorf("vars missing or out of order:\n%s", out)
	}
	for _, want := range []string{"imap.gmail.com:993", "registry.npmjs.org:443"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
