package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// Supplied is what a person gives when connecting a Slack channel, beside
// the signing secret.
func (provider) Supplied() []string { return []string{KeyBotToken} }

// SetupRequest asks Slack whose token it is. A token not shaped like a bot's
// is refused before it is sent anywhere.
func (provider) SetupRequest(supplied gateway.Credentials) (gateway.OutboundRequest, error) {
	token := supplied.Get(KeyBotToken)
	if !tokenPattern.MatchString(token) {
		return gateway.OutboundRequest{}, errors.New("bot_token is not a Slack bot token: it starts with xoxb-")
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	return gateway.OutboundRequest{URL: apiBase + "auth.test", Header: h, Body: []byte{}}, nil
}

// revoked is the auth.test refusals that mean Slack does not know the token.
var revoked = map[string]bool{
	"invalid_auth": true, "not_authed": true, "account_inactive": true, "token_revoked": true, "token_expired": true,
}

// Complete reads auth.test. What it returns about a refusal never echoes the
// token: it is shown to whoever connected the channel.
func (provider) Complete(status int, body []byte) (gateway.Credentials, []string, error) {
	if status != http.StatusOK {
		return nil, nil, fmt.Errorf("asked about the bot, Slack answered %d", status)
	}
	var me struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, nil, errors.New("the answer Slack gave about the bot could not be read")
	}
	switch {
	case !me.OK && revoked[me.Error]:
		return nil, nil, errors.New("the bot token is not one Slack knows")
	case !me.OK:
		return nil, nil, fmt.Errorf("slack refused the bot token: %s", clip(me.Error))
	case me.BotID == "" || !userPattern.MatchString(me.UserID):
		return nil, nil, errors.New("the token belongs to an account that is not a bot")
	}
	return gateway.Credentials{KeyBotUserID: me.UserID}, nil, nil
}

// fileHost is where Slack serves a file a bot may download.
const fileHost = "files.slack.com"

// DownloadRequest fetches a file in one request, under the bot's token. Only
// an https address on Slack's file host is asked for: the token goes with it.
func (provider) DownloadRequest(ref string, _ []byte, creds gateway.Credentials) (gateway.OutboundRequest, bool, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, false, errors.New("slack: no bot token")
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "https" || u.Host != fileHost || u.User != nil {
		return gateway.OutboundRequest{}, false, errors.New("slack: a file that is not on Slack's file host")
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return gateway.OutboundRequest{Method: http.MethodGet, URL: u.String(), Header: h, Accepted: notSignIn}, true, nil
}

// notSignIn refuses the page Slack serves in place of a file the token may
// not read: it is a 200, and HTML.
func notSignIn(_ int, body []byte) error {
	head := bytes.ToLower(bytes.TrimSpace(body[:min(len(body), 512)]))
	if bytes.HasPrefix(head, []byte("<!doctype html")) || bytes.HasPrefix(head, []byte("<html")) {
		return errors.New("slack sent a sign-in page instead of the file: the app needs the files:read scope")
	}
	return nil
}
