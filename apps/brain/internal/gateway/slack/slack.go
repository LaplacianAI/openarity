// Package slack reads a Slack app's Events API deliveries and answers in the
// conversation a message came from: a direct message in place, a channel in
// a thread under the message that asked.
package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

const (
	// KeyBotToken is the app's Bot User OAuth Token; a person supplies it.
	KeyBotToken = "bot_token"
	// KeyBotUserID comes from auth.test when the channel is connected: Parse
	// needs it to tell a message to our bot, and our bot's own messages,
	// from everyone else's.
	KeyBotUserID = "bot_user_id"

	SignatureHeader = "X-Slack-Signature"
	TimestampHeader = "X-Slack-Request-Timestamp"
)

// window is how far a signed timestamp may be from when the delivery
// arrived, either way: Slack's own five minutes.
const window = 5 * time.Minute

// apiBase is fixed: a bot token is only ever sent to Slack.
const apiBase = "https://slack.com/api/"

var (
	tokenPattern = regexp.MustCompile(`^xoxb-[A-Za-z0-9-]+$`)
	// userPattern is a Slack user id, which is all a notice puts in a
	// mention: anything else in one could be a broadcast.
	userPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	mentioned   = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]*)?>`)
)

type provider struct{}

func New() gateway.Provider { return provider{} }

func (provider) Name() string { return "slack" }

// Routes are the Events API's Request URL and, beside it, the Interactivity
// Request URL, which Slack posts a button tap and a modal submission to.
func (provider) Routes() []gateway.Route {
	return []gateway.Route{{Method: http.MethodPost}, {Method: http.MethodPost, Suffix: InteractivitySuffix}}
}

func (provider) Keys() []string {
	return []string{gateway.KeySigning, KeyBotToken, KeyBotUserID}
}

// IssuesSigningSecret: Slack signs with the secret in the app's Basic
// Information, so a person supplies it rather than the brain making one.
func (provider) IssuesSigningSecret() {}

// RepliesWithEvents: each of a run's steps is a message of its own in the
// thread, sent as the answer is.
func (provider) RepliesWithEvents() {}

// Verify checks Slack's v0 signature over the timestamp and the raw body, and
// that the timestamp is within five minutes of arrival either way. A channel
// missing any credential takes nothing: without the bot's user id, no message
// could be told apart as addressed to it.
func (p provider) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	for _, k := range p.Keys() {
		if creds.Get(k) == "" {
			return fmt.Errorf("slack: no %s", k)
		}
	}
	ts := req.Header.Get(TimestampHeader)
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("slack: no request timestamp")
	}
	if d := req.ReceivedAt.Sub(time.Unix(sent, 0)); d > window || d < -window {
		return errors.New("slack: the request timestamp is too far from now")
	}
	mac := hmac.New(sha256.New, []byte(creds.Get(gateway.KeySigning)))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(req.Body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(req.Header.Get(SignatureHeader)), []byte(want)) {
		return errors.New("slack: bad signature")
	}
	return nil
}

type envelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Event     *event `json:"event"`
}

type event struct {
	Type        string   `json:"type"`
	Subtype     string   `json:"subtype"`
	Channel     string   `json:"channel"`
	ChannelType string   `json:"channel_type"`
	User        string   `json:"user"`
	BotID       string   `json:"bot_id"`
	Text        string   `json:"text"`
	TS          string   `json:"ts"`
	ThreadTS    string   `json:"thread_ts"`
	Files       []file   `json:"files"`
	Profile     *profile `json:"user_profile"`
}

// profile is what Slack sends about the author beside a message they typed.
// It is not in the Events API reference, so a message without one is a
// sender with no name, never a refused message.
type profile struct {
	DisplayName string `json:"display_name"`
	RealName    string `json:"real_name"`
}

type file struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	Size     int64  `json:"size"`
	Download string `json:"url_private_download"`
	Mode     string `json:"mode"`
}

// maxChallenge bounds the challenge echoed back: Slack's are short.
const maxChallenge = 256

// Parse reads the URL check Slack sends when a Request URL is saved, and new
// messages; on the interactivity route, a tap on an approval's button and the
// decline modal's submission. Edits, deletes, joins and our own bot's messages are nothing to
// do, and so is a message at the top of a channel that does not name the
// bot: a mention there starts a thread, so no conversation would ever read
// what was said around it.
func (provider) Parse(req gateway.WebhookRequest, creds gateway.Credentials) (gateway.Result, error) {
	if req.Suffix == InteractivitySuffix {
		return parseInteraction(req)
	}
	var env envelope
	if err := json.Unmarshal(req.Body, &env); err != nil {
		return gateway.Result{}, fmt.Errorf("slack: %w", err)
	}
	switch env.Type {
	case "url_verification":
		if env.Challenge == "" || len(env.Challenge) > maxChallenge {
			return gateway.Result{}, errors.New("slack: a url_verification with no usable challenge")
		}
		return gateway.Result{Ack: []byte(env.Challenge)}, nil
	case "event_callback":
	default:
		return gateway.Result{}, nil
	}
	e := env.Event
	if e == nil || e.Type != "message" || e.User == "" || e.Channel == "" || e.TS == "" {
		return gateway.Result{}, nil
	}
	switch e.Subtype {
	case "", "file_share", "thread_broadcast":
	default:
		return gateway.Result{}, nil
	}
	bot := creds.Get(KeyBotUserID)
	if e.User == bot {
		return gateway.Result{}, nil
	}

	mentions := mentionsOf(e.Text, bot)
	session, ok := sessionOf(e, mentions)
	if !ok {
		return gateway.Result{}, nil
	}
	sent, err := sentAt(e.TS)
	if err != nil {
		return gateway.Result{}, err
	}
	in := gateway.Inbound{
		ExternalID:  e.Channel + ":" + e.TS,
		Author:      gateway.Author{Ref: e.User, DisplayName: e.Profile.name(), IsBot: e.BotID != ""},
		Session:     session,
		Text:        e.Text,
		Mentions:    mentions,
		SentAt:      sent,
		Attachments: attachmentsOf(e.Files),
	}
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		in.ReplyTo = e.Channel + ":" + e.ThreadTS
	}
	return gateway.Result{Messages: []gateway.Inbound{in}}, nil
}

func (p *profile) name() string {
	if p == nil {
		return ""
	}
	if n := strings.TrimSpace(p.DisplayName); n != "" {
		return n
	}
	return strings.TrimSpace(p.RealName)
}

// sessionOf names the conversation. A direct message is one conversation,
// threads in it included, answered in place. In a channel, a group or a group
// DM, a reply in a thread is that thread's, and a message at the top that
// names the bot starts the thread it is answered in; one that does not is
// not a conversation with anyone.
func sessionOf(e *event, mentions []gateway.Mention) (gateway.Session, bool) {
	switch e.ChannelType {
	case "im":
		return gateway.Session{Ref: e.Channel, Kind: gateway.SessionDirect}, true
	case "channel", "group", "mpim":
	default:
		return gateway.Session{}, false
	}
	if e.ThreadTS != "" {
		return gateway.Session{Ref: e.Channel + ":" + e.ThreadTS, Kind: gateway.SessionThread}, true
	}
	for _, m := range mentions {
		if m.IsUs {
			return gateway.Session{Ref: e.Channel + ":" + e.TS, Kind: gateway.SessionThread}, true
		}
	}
	return gateway.Session{}, false
}

// mentionsOf is who a message names: Slack writes a mention as <@U123> or
// <@U123|name> in the text.
func mentionsOf(text, bot string) []gateway.Mention {
	var out []gateway.Mention
	for _, m := range mentioned.FindAllStringSubmatch(text, -1) {
		out = append(out, gateway.Mention{SenderRef: m[1], DisplayName: "@" + m[1], IsUs: bot != "" && m[1] == bot})
	}
	return out
}

// sentAt reads a message's ts, seconds and microseconds since the epoch.
func sentAt(ts string) (time.Time, error) {
	secs, micros, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("slack: the message ts %q is not a time", ts)
	}
	var us int64
	if micros != "" {
		if us, err = strconv.ParseInt(micros, 10, 64); err != nil || len(micros) != 6 {
			return time.Time{}, fmt.Errorf("slack: the message ts %q is not a time", ts)
		}
	}
	return time.Unix(s, us*int64(time.Microsecond)).UTC(), nil
}

// attachmentsOf names the files a message carries by the address they
// download from. A file Slack has hidden, or one held elsewhere, has none.
func attachmentsOf(files []file) []gateway.Attachment {
	var out []gateway.Attachment
	for _, f := range files {
		if f.Download == "" || f.Mode == "tombstone" || f.Mode == "hidden_by_limit" || f.Mode == "external" {
			continue
		}
		out = append(out, gateway.Attachment{
			Ref: f.Download, ClaimedFilename: f.Name, ClaimedMediaType: f.Mimetype, ClaimedSize: f.Size,
		})
	}
	return out
}
