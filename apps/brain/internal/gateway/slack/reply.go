package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/markdown"
)

// messageLimits is what one message's markdown_text holds: 12,000
// characters. Slack counts no blocks in it.
var messageLimits = markdown.Limits{Runes: 12000, Blocks: 1 << 30}

// postMessage is chat.postMessage with markdown_text, which takes standard
// Markdown as written. Unfurling is off: Slack's servers would fetch a link
// in the answer to preview it, which sends whatever is in the URL somewhere
// without a click.
type postMessage struct {
	Channel     string `json:"channel"`
	ThreadTS    string `json:"thread_ts,omitempty"`
	Markdown    string `json:"markdown_text"`
	UnfurlLinks bool   `json:"unfurl_links"`
	UnfurlMedia bool   `json:"unfurl_media"`
}

// Reply sends the answer in the conversation it came from: a channel's in
// its thread. One too long for a message is several, in order.
func (provider) Reply(
	_ gateway.Channel, out gateway.Outbound, creds gateway.Credentials, _ time.Time,
) ([]gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return nil, errors.New("slack: no bot token")
	}
	channel, thread, err := target(out.Session.Ref)
	if err != nil {
		return nil, err
	}
	parts := markdown.Split(Sanitize(out.Text), messageLimits)
	if len(parts) == 0 {
		return nil, errors.New("slack: nothing to send")
	}
	reqs := make([]gateway.OutboundRequest, 0, len(parts))
	for _, part := range parts {
		req, err := post(token, postMessage{Channel: channel, ThreadTS: thread, Markdown: part})
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

// Notice tells a sender nobody has let in where they stand, in the thread or
// direct message they wrote in, naming them by a mention so nobody else takes
// it as theirs.
func (provider) Notice(_ gateway.Channel, n gateway.Notice, creds gateway.Credentials) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, errors.New("slack: no bot token")
	}
	if n.Text == "" {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: no words for a %q notice", n.Kind)
	}
	channel, thread, err := target(n.Session.Ref)
	if err != nil {
		return gateway.OutboundRequest{}, err
	}
	name := "Hello"
	if userPattern.MatchString(n.Sender.Ref) {
		name = "<@" + n.Sender.Ref + ">"
	}
	return post(token, postMessage{Channel: channel, ThreadTS: thread, Markdown: name + ", " + n.Text})
}

func post(token string, body postMessage) (gateway.OutboundRequest, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: encoding the message: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Authorization", "Bearer "+token)
	return gateway.OutboundRequest{URL: apiBase + "chat.postMessage", Header: h, Body: raw, Accepted: accepted}, nil
}

// target reads back the channel and thread sessionOf named.
func target(ref string) (channel, thread string, err error) {
	channel, thread, _ = strings.Cut(ref, ":")
	if channel == "" || strings.ContainsAny(channel, "/?#") {
		return "", "", fmt.Errorf("slack: session %q names no channel", ref)
	}
	return channel, thread, nil
}

// final is the refusals no retry fixes: the message or where it goes is
// wrong, or the app may not post there, and asking an hour later asks the
// same.
var final = []string{
	"channel_not_found", "not_in_channel", "is_archived", "msg_too_long", "no_text",
	"invalid_arguments", "markdown_text_conflict", "invalid_blocks", "restricted_action",
	"cannot_reply_to_message", "invalid_auth", "not_authed", "account_inactive",
	"token_revoked", "missing_scope",
}

// accepted reads chat.postMessage's answer: Slack says no with a 200 and
// "ok": false. Its error is a code, never the token, so it is passed on.
func accepted(_ int, body []byte) error {
	var a struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return errors.New("slack's answer could not be read")
	}
	if a.OK {
		return nil
	}
	code := clip(a.Error)
	if slices.Contains(final, code) {
		return fmt.Errorf("%w: slack said %s", gateway.ErrUnsendable, code)
	}
	return fmt.Errorf("slack said %s", code)
}

// clip keeps an error code to what a code is made of, so an answer cannot
// write anything else into the reply's last error.
func clip(code string) string {
	var b strings.Builder
	for _, r := range code {
		if b.Len() >= 64 {
			break
		}
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "no reason"
	}
	return b.String()
}

// wordJoiner is invisible and breaks the token Slack would read.
const wordJoiner = "\u2060"

// broadcasts are the words that notify a whole channel or workspace.
var broadcasts = []string{"here", "channel", "everyone"}

// Sanitize makes an answer safe to post. An agent's answer can carry text a
// prompt injection chose, so:
//
//   - an image becomes a link, which waits for the reader.
//   - a broadcast is broken: <!here>, <!channel>, <!everyone> and a user
//     group's <!subteam^…> become text, and so do @here, @channel and
//     @everyone, so an answer can never notify everyone in a channel.
//
// Code is left exactly as written, where both are only text.
func Sanitize(text string) string { return markdown.Rewrite(text, markdown.Images, noBroadcast) }

func noBroadcast(s string) (string, int, bool) {
	if strings.HasPrefix(s, "<!") {
		return "<" + wordJoiner + "!", 2, true
	}
	if s[0] != '@' {
		return "", 0, false
	}
	for _, w := range broadcasts {
		if len(s) >= 1+len(w) && strings.EqualFold(s[1:1+len(w)], w) {
			return "@" + wordJoiner + s[1:1+len(w)], 1 + len(w), true
		}
	}
	return "", 0, false
}
