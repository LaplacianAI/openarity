package channels

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	slackToken  = "xoxb-TEST-ONLY-NOT-A-TOKEN"
	slackSecret = "TEST-ONLY-signing-secret"
)

func withSlack() Providers { return providers{"slack": slack.New()} }

// knownSlackBot answers auth.test as Slack does for a bot token.
func knownSlackBot() *telegramAPI {
	return &telegramAPI{status: http.StatusOK, body: `{"ok":true,"team_id":"T061EG9R6","user_id":"U0BOT1234","bot_id":"B0BOT"}`}
}

// A Slack channel holds the secret Slack signs with, the token, and the bot's
// user id from auth.test, in one write. The secret is the person's, so it is
// never shown back, and nothing points Slack at the channel: the person
// pastes the Request URL.
func TestASlackChannelIsConnectedWithTheSecretSlackSignsWith(t *testing.T) {
	t.Parallel()

	api, s, sec := knownSlackBot(), &fakeStore{}, newFakeSecrets()
	rec := connect(t, api, withSlack(), s, sec,
		`{"provider":"slack","name":"acme","signing_secret":"`+slackSecret+`","credentials":{"bot_token":"`+slackToken+`"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	got := decodeCreated(t, rec)
	if got.SigningSecret != "" || got.Connected || !got.CanReply || got.HookURL == "" {
		t.Errorf("channel = %+v, want no secret shown, not connected, replies, and a hook URL", got)
	}
	if strings.Contains(rec.Body.String(), slackToken) || strings.Contains(rec.Body.String(), slackSecret) {
		t.Error("the response shows a credential")
	}
	if asked := api.methodsAsked(); len(asked) != 1 || asked[0] != "auth.test" {
		t.Fatalf("asked Slack %v, want auth.test alone", asked)
	}
	path := secrets.Path(got.TeamID, secrets.KindChannel, got.ID)
	want := map[string]string{gateway.KeySigning: slackSecret, slack.KeyBotToken: slackToken, slack.KeyBotUserID: "U0BOT1234"}
	if sec.writes != 1 || len(sec.put[path]) != len(want) {
		t.Fatalf("%d writes, stored %v; want one write of %v", sec.writes, sec.put[path], want)
	}
	for k, v := range want {
		if sec.put[path][k] != v {
			t.Errorf("stored %s = %q, want %q", k, sec.put[path][k], v)
		}
	}
}

// A secret the brain made would refuse every delivery Slack signs, so a Slack
// channel without Slack's is refused before Slack is asked anything, and
// nothing is created.
func TestASlackChannelWithoutSlacksSecretCreatesNothing(t *testing.T) {
	t.Parallel()

	api, s, sec := knownSlackBot(), &fakeStore{}, newFakeSecrets()
	rec := connect(t, api, withSlack(), s, sec,
		`{"provider":"slack","name":"acme","credentials":{"bot_token":"`+slackToken+`"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "signing_secret is required") {
		t.Errorf("POST = %d %s, want 400 asking for the signing secret", rec.Code, rec.Body)
	}
	if len(s.created) != 0 || sec.writes != 0 || len(api.methodsAsked()) != 0 {
		t.Errorf("created %d, wrote %d, asked %v; want nothing", len(s.created), sec.writes, api.methodsAsked())
	}
}
