package channels

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const botToken = "123456:TEST-ONLY-NOT-A-TOKEN"

const createdTelegram = `{
  "id": "` + channelID + `", "team_id": "` + teamID + `",
  "provider": "telegram", "name": "bot", "can_reply": true, "takes_reply_url": false,
  "hook_url": "https://hooks.example.com/hooks/telegram/` + channelID + `",
  "signing_secret": "oawh_generated-by-the-brain"
}`

func telegramRoutes(body string) map[string]clitest.Reply {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: body}
	return routes
}

func TestCreateSendsTheCredentialAndNeverEchoesIt(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		script := clitest.Routes(t, telegramRoutes(createdTelegram))

		out, err := clitest.ExecuteWithStdin(t, commands, botToken+"\n",
			"channels", "create", "platform", "bot", "--provider", "telegram",
			"--credential-stdin", "bot_token", "-o", format)
		if err != nil {
			t.Fatalf("%s: %v\n%s", format, err, out)
		}
		if body := postBody(t, script); !strings.Contains(body, `"credentials":{"bot_token":"`+botToken+`"}`) {
			t.Errorf("%s: body = %s, want the trimmed credential", format, body)
		}
		if strings.Contains(out, botToken) {
			t.Errorf("%s: the bot token was echoed:\n%s", format, out)
		}
	}
}

func TestCreateRefusesTwoThingsOnStdin(t *testing.T) {
	script := clitest.Routes(t, telegramRoutes(createdTelegram))

	_, err := clitest.ExecuteWithStdin(t, commands, "x\n",
		"channels", "create", "platform", "bot", "--provider", "telegram",
		"--secret-stdin", "--credential-stdin", "bot_token")
	if err == nil {
		t.Fatal("both stdin flags were accepted, though there is one stdin")
	}
	for _, want := range []string{"--secret-stdin", "--credential-stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/channels"); n != 0 {
		t.Errorf("a channel was created: %d calls", n)
	}
}

func TestCreateRefusesAnEmptyCredential(t *testing.T) {
	script := clitest.Routes(t, telegramRoutes(createdTelegram))

	_, err := clitest.ExecuteWithStdin(t, commands, "  \n",
		"channels", "create", "platform", "bot", "--provider", "telegram",
		"--credential-stdin", "bot_token")
	if err == nil {
		t.Fatal("an empty credential was accepted")
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/channels"); n != 0 {
		t.Errorf("a channel was created with no token: %d calls", n)
	}
}

func TestTelegramCreateShowsTheSetWebhookStep(t *testing.T) {
	clitest.Routes(t, telegramRoutes(createdTelegram))

	out, err := clitest.ExecuteWithStdin(t, commands, botToken,
		"channels", "create", "platform", "bot", "--provider", "telegram",
		"--credential-stdin", "bot_token")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	for _, want := range []string{
		"bot<bot token>/setWebhook",
		"url=https://hooks.example.com/hooks/telegram/" + channelID,
		"secret_token=oawh_generated-by-the-brain",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output is missing %q:\n%s", want, out)
		}
	}
}

func TestCustomCreateHasNoSetWebhookStep(t *testing.T) {
	clitest.Routes(t, telegramRoutes(`{
  "id": "`+channelID+`", "team_id": "`+teamID+`", "provider": "custom", "name": "support",
  "hook_url": "https://hooks.example.com/hooks/custom/`+channelID+`",
  "signing_secret": "oawh_generated-by-the-brain"
}`))

	out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "support", "--provider", "custom")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if strings.Contains(out, "setWebhook") {
		t.Errorf("a custom channel was told to call Telegram:\n%s", out)
	}
}

func TestJSONOutputIsOnlyTheObject(t *testing.T) {
	clitest.Routes(t, telegramRoutes(createdTelegram))

	out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "bot", "--provider", "telegram", "-o", "json")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if strings.Contains(out, "setWebhook") {
		t.Errorf("json output carries the hint:\n%s", out)
	}
}

func TestARefusedBotTokenSaysWhy(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{
		Status: http.StatusBadRequest,
		Body:   "Telegram refused the bot token",
	}
	clitest.Routes(t, routes)

	_, err := clitest.ExecuteWithStdin(t, commands, botToken,
		"channels", "create", "platform", "bot", "--provider", "telegram", "--credential-stdin", "bot_token")
	if err == nil || !strings.Contains(err.Error(), "Telegram refused") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}

func TestARefusedReplyURLOnTelegramSaysWhy(t *testing.T) {
	clitest.Routes(t, updateRoutes(clitest.Reply{
		Status: http.StatusBadRequest,
		Body:   "a telegram channel replies in the chat and takes no reply_url",
	}))

	_, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--reply-url", repliesTo)
	if err == nil || !strings.Contains(err.Error(), "takes no reply_url") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}

const connectedTelegram = `{
  "id": "` + channelID + `", "team_id": "` + teamID + `",
  "provider": "telegram", "name": "bot", "can_reply": true, "takes_reply_url": false,
  "hook_url": "https://hooks.example.com/hooks/telegram/` + channelID + `",
  "signing_secret": "oawh_generated-by-the-brain", "connected": true
}`

// When the brain pointed the bot at the channel itself there is no step
// left: the curl is not printed, and the person is told it is done.
func TestAConnectedTelegramChannelHasNoStepLeft(t *testing.T) {
	clitest.Routes(t, telegramRoutes(connectedTelegram))

	out, err := clitest.ExecuteWithStdin(t, commands, botToken,
		"channels", "create", "platform", "bot", "--provider", "telegram",
		"--credential-stdin", "bot_token")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if strings.Contains(out, "setWebhook") {
		t.Errorf("a connected channel still shows the setWebhook step:\n%s", out)
	}
	if !strings.Contains(out, "telegram now sends this channel's messages to the brain") {
		t.Errorf("the output does not say the bot is connected:\n%s", out)
	}
}

// The brain's warning reaches the person who connected the bot, and a
// terminal escape in it does not reach their terminal.
func TestAWarningAboutTheBotIsShown(t *testing.T) {
	warned := strings.Replace(connectedTelegram, `"connected": true`,
		`"connected": true, "warnings": ["This bot's privacy mode is on.\u001b[2J"]`, 1)
	clitest.Routes(t, telegramRoutes(warned))

	out, err := clitest.ExecuteWithStdin(t, commands, botToken,
		"channels", "create", "platform", "bot", "--provider", "telegram", "--credential-stdin", "bot_token")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "privacy mode is on") {
		t.Errorf("the output does not carry the warning:\n%s", out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("the warning's escape sequence reached the terminal:\n%q", out)
	}
}

func TestNoWarningIsShownWhenThereIsNone(t *testing.T) {
	clitest.Routes(t, telegramRoutes(connectedTelegram))

	out, err := clitest.ExecuteWithStdin(t, commands, botToken,
		"channels", "create", "platform", "bot", "--provider", "telegram", "--credential-stdin", "bot_token")
	if err != nil || strings.Contains(out, "warning") {
		t.Errorf("create = %v, output:\n%s", err, out)
	}
}
