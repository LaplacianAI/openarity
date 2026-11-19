package channels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const (
	slackToken  = "xoxb-TEST-ONLY-NOT-A-TOKEN"
	slackSecret = "TEST-ONLY-signing-secret"
)

const createdSlack = `{
  "id": "` + channelID + `", "team_id": "` + teamID + `",
  "provider": "slack", "name": "acme", "can_reply": true, "takes_reply_url": false,
  "hook_url": "https://hooks.example.com/hooks/slack/` + channelID + `"
}`

func secretFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Slack needs two secrets and there is one stdin: the token comes on stdin
// and the signing secret from a file. Neither is echoed, and the step Slack
// leaves to the person is said.
func TestASlackChannelTakesItsSecretFromAFile(t *testing.T) {
	script := clitest.Routes(t, telegramRoutes(createdSlack))

	out, err := clitest.ExecuteWithStdin(t, commands, slackToken+"\n",
		"channels", "create", "platform", "acme", "--provider", "slack",
		"--credential-stdin", "bot_token", "--secret-file", secretFile(t, slackSecret+"\n"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	body := postBody(t, script)
	if !strings.Contains(body, `"signing_secret":"`+slackSecret+`"`) || !strings.Contains(body, `"bot_token":"`+slackToken+`"`) {
		t.Errorf("body = %s, want both secrets, trimmed", body)
	}
	if strings.Contains(out, slackToken) || strings.Contains(out, slackSecret) {
		t.Errorf("a secret was echoed:\n%s", out)
	}
	if !strings.Contains(out, "Event Subscriptions") || !strings.Contains(out, "/hooks/slack/"+channelID) {
		t.Errorf("the Request URL step is not said:\n%s", out)
	}
}

func TestTheSigningSecretComesFromOnePlace(t *testing.T) {
	script := clitest.Routes(t, telegramRoutes(createdSlack))

	_, err := clitest.ExecuteWithStdin(t, commands, "x\n",
		"channels", "create", "platform", "acme", "--provider", "slack",
		"--secret-stdin", "--secret-file", secretFile(t, slackSecret))
	if err == nil || !strings.Contains(err.Error(), "--secret-file") {
		t.Fatalf("err = %v, want both sources refused", err)
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/channels"); n != 0 {
		t.Errorf("a channel was created: %d calls", n)
	}
}

func TestAnEmptyOrMissingSecretFileCreatesNothing(t *testing.T) {
	for name, path := range map[string]string{
		"empty":   secretFile(t, " \n"),
		"missing": filepath.Join(t.TempDir(), "nope"),
	} {
		script := clitest.Routes(t, telegramRoutes(createdSlack))
		_, err := clitest.ExecuteWithStdin(t, commands, slackToken,
			"channels", "create", "platform", "acme", "--provider", "slack",
			"--credential-stdin", "bot_token", "--secret-file", path)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if n := script.Calls("POST", "/teams/"+teamID+"/channels"); n != 0 {
			t.Errorf("%s: a channel was created: %d calls", name, n)
		}
	}
}
