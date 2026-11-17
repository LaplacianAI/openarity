package mcpservers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func gmail(connection string) string {
	return `{"id":"` + serverID + `","team_id":"` + teamID + `","name":"gmail","url":"https://gmailmcp.example/mcp/v1",
  "auth_kind":"oauth","oauth_client_id":"client-1","oauth_scopes":["gmail.readonly"],"env":{},"bare":false,
  "connection":` + connection + `,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`
}

func connected(at time.Time) string {
	return `{"status":"connected","connected_at":"` + at.UTC().Format(time.RFC3339Nano) + `","scopes_granted":["gmail.readonly"]}`
}

const needsSignIn = `{"status":"needs_sign_in","reason":"the authorization server refused the refresh",
  "connected_at":"2026-10-02T10:00:00Z","scopes_granted":["gmail.readonly"]}`

func fastPolls(t *testing.T) {
	t.Helper()
	was := pollEvery
	pollEvery = 5 * time.Millisecond
	t.Cleanup(func() { pollEvery = was })
}

func TestCreatingAServerThatSignsInSendsItsClient(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers": {Status: http.StatusCreated, Body: gmail("null")},
	}))

	_, err := clitest.Execute(t, commands, "mcp-servers", "create", "platform", "gmail",
		"--url", "https://gmailmcp.example/mcp/v1", "--oauth", "--client-id", "client-1",
		"--client-secret-ref", "teams/"+teamID+"/mcp/google#secret", "--scope", "gmail.readonly", "--scope", "gmail.send")
	if err != nil {
		t.Fatal(err)
	}
	body := sent(t, script, http.MethodPost)
	scopes, _ := body["oauth_scopes"].([]any)
	if body["auth_kind"] != "oauth" || body["oauth_client_id"] != "client-1" ||
		body["oauth_client_secret_ref"] != "teams/"+teamID+"/mcp/google#secret" || len(scopes) != 2 || body["auth_secret_ref"] != nil {
		t.Errorf("sent %v", body)
	}
}

func TestSignInFlagsThatCannotGoTogetherAreRefusedBeforeAnyRequest(t *testing.T) {
	cases := map[string][]string{
		"a client without --oauth":    {"--url", "https://m.example/mcp", "--client-id", "c"},
		"a scope without --oauth":     {"--url", "https://m.example/mcp", "--scope", "s"},
		"--oauth with a static token": {"--url", "https://m.example/mcp", "--oauth", "--client-id", "c", "--auth-secret-ref", "teams/x/mcp/a#b"},
		"--oauth on a command server": {"--oauth", "--client-id", "c", "--", "npx", "server"},
	}
	for name, flags := range cases {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			args := append([]string{"mcp-servers", "create", "platform", "x"}, flags...)
			if _, err := clitest.Execute(t, commands, args...); err == nil {
				t.Fatal("accepted")
			}
			if n := script.Calls(http.MethodPost, "/teams/"+teamID+"/mcp-servers"); n != 0 {
				t.Errorf("sent %d requests", n)
			}
		})
	}
}

func TestListSaysHowEachServerIsReached(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":                  {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/mcp-servers": {Status: http.StatusOK, Body: `{"items":[` + gmail(needsSignIn) + `,` + github + `]}`},
	})

	out, err := clitest.Execute(t, commands, "mcp-servers", "list", "platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oauth: needs sign-in", "token"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestGetSaysWhyASignInNeedsDoingAgain(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: gmail(needsSignIn)},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "get", "platform", serverID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"client-1", "gmail.readonly", "needs signing in again", "refused the refresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestGetSaysAServerNeverConnectedIsNot(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: gmail("null")},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "get", "platform", serverID)
	if err != nil || !strings.Contains(out, "not connected") {
		t.Errorf("out = %s, %v", out, err)
	}
}

const startReply = `{"authorize_url":"https://accounts.example/o/oauth2/auth?state=s"}`

// Connect prints the address and waits for a sign-in made since it began:
// the old one, still connected while the person reads the consent screen,
// is not it.
func TestConnectWaitsForTheNewSignIn(t *testing.T) {
	fastPolls(t)
	old := time.Now().Add(-time.Hour)
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers/{serverID}/oauth/start": {Status: http.StatusOK, Body: startReply},
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: gmail(connected(old)), Then: []clitest.Reply{
			{Status: http.StatusOK, Body: gmail(connected(old))},
			{Status: http.StatusOK, Body: gmail(connected(time.Now().Add(time.Minute)))},
		}},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "connect", "platform", serverID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://accounts.example/o/oauth2/auth?state=s") || !strings.Contains(out, "connected") {
		t.Errorf("out:\n%s", out)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/mcp-servers/"+serverID); n < 3 {
		t.Errorf("polled %d times; the old sign-in was taken for the new one", n)
	}
}

func TestConnectWithoutWaitingOnlyPrintsTheAddress(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers/{serverID}/oauth/start": {Status: http.StatusOK, Body: startReply},
	}))

	out, err := clitest.Execute(t, commands, "-o", "json", "mcp-servers", "connect", "platform", serverID, "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "authorize_url") || !strings.Contains(out, "open https://accounts.example") {
		t.Errorf("out:\n%s", out)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/mcp-servers/"+serverID); n != 0 {
		t.Errorf("polled %d times with --no-wait", n)
	}
}

func TestConnectStopsWhenAskedTo(t *testing.T) {
	fastPolls(t)
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers/{serverID}/oauth/start": {Status: http.StatusOK, Body: startReply},
		"GET /teams/{id}/mcp-servers/{serverID}":              {Status: http.StatusOK, Body: gmail("null")},
	}))

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := clitest.ExecuteContext(ctx, t, commands, "mcp-servers", "connect", "platform", serverID)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the wait stopped", err)
	}
}

func TestConnectSaysWhenTheSignInHasExpired(t *testing.T) {
	fastPolls(t)
	was := signInLasts
	signInLasts = 50 * time.Millisecond
	t.Cleanup(func() { signInLasts = was })
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers/{serverID}/oauth/start": {Status: http.StatusOK, Body: startReply},
		"GET /teams/{id}/mcp-servers/{serverID}":              {Status: http.StatusOK, Body: gmail("null")},
	}))

	_, err := clitest.Execute(t, commands, "mcp-servers", "connect", "platform", serverID)
	if !errors.Is(err, errSignInTimedOut) {
		t.Errorf("err = %v, want the sign-in's expiry named", err)
	}
}

func TestDisconnectCallsTheDisconnect(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"DELETE /teams/{id}/mcp-servers/{serverID}/oauth": {Status: http.StatusNoContent},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "disconnect", "platform", serverID)
	if err != nil || !strings.Contains(out, "disconnected") {
		t.Fatalf("out = %s, %v", out, err)
	}
	if n := script.Calls(http.MethodDelete, "/teams/"+teamID+"/mcp-servers/"+serverID+"/oauth"); n != 1 {
		t.Errorf("disconnect sent %d times", n)
	}
}

func TestConnectAndDisconnectNameToolWriteWhenRefused(t *testing.T) {
	for _, verb := range []string{"connect", "disconnect"} {
		t.Run(verb, func(t *testing.T) {
			clitest.Routes(t, routes(map[string]clitest.Reply{
				"POST /teams/{id}/mcp-servers/{serverID}/oauth/start": {Status: http.StatusForbidden, Body: "forbidden", ContentType: "text/plain"},
				"DELETE /teams/{id}/mcp-servers/{serverID}/oauth":     {Status: http.StatusForbidden, Body: "forbidden", ContentType: "text/plain"},
			}))
			_, err := clitest.Execute(t, commands, "mcp-servers", verb, "platform", serverID)
			if err == nil || !strings.Contains(err.Error(), "tool:write") {
				t.Errorf("err = %v, want it to name tool:write", err)
			}
		})
	}
}
