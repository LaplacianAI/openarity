package slack_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/providertest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

const (
	secret = "TEST-ONLY-signing-secret"
	token  = "xoxb-TEST-ONLY-NOT-A-TOKEN"
	botID  = "U0BOT1234"
)

var receivedAt = time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)

func creds() gateway.Credentials {
	return gateway.Credentials{
		gateway.KeySigning: secret,
		slack.KeyBotToken:  token,
		slack.KeyBotUserID: botID,
	}
}

// signed is a delivery as Slack posts it: signed over v0:<timestamp>:<body>
// with the app's signing secret, sent at ts.
func signed(body string, ts time.Time) gateway.WebhookRequest {
	stamp := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + stamp + ":" + body))
	h := http.Header{}
	h.Set(slack.TimestampHeader, stamp)
	h.Set(slack.SignatureHeader, "v0="+hex.EncodeToString(mac.Sum(nil)))
	h.Set("Content-Type", "application/json")
	return gateway.WebhookRequest{Method: http.MethodPost, Header: h, Body: []byte(body), ReceivedAt: receivedAt}
}

func request(body string) gateway.WebhookRequest { return signed(body, receivedAt) }

// envelope wraps an event as the Events API delivers it.
func envelope(event string) string {
	return `{"token":"Jhj5dZrVaK7ZwHHjRyZWjbDl","team_id":"T061EG9R6","api_app_id":"A0PNCHHK2",` +
		`"event":` + event + `,"type":"event_callback","event_id":"Ev0PV52K21","event_time":1763544600}`
}

// A direct message to the bot.
var direct = envelope(`{"type":"message","channel":"D024BE91L","user":"U2147483697","text":"Why did the nightly export fail?",` +
	`"ts":"1763544600.000200","event_ts":"1763544600.000200","channel_type":"im",` +
	`"user_profile":{"display_name":"asha","real_name":"Asha Rao"}}`)

// A message at the top of a channel that names the bot.
var mention = envelope(`{"type":"message","channel":"C2147483705","user":"U2147483697",` +
	`"text":"<@U0BOT1234> can you check the deploy?","ts":"1763544601.000300","channel_type":"channel"}`)

// A reply in the thread that mention started, naming nobody.
var threadReply = envelope(`{"type":"message","channel":"C2147483705","user":"U2147483697","text":"and the logs too",` +
	`"ts":"1763544700.000400","thread_ts":"1763544601.000300","channel_type":"channel"}`)

// A message at the top of a channel that names someone else.
var chatter = envelope(`{"type":"message","channel":"C2147483705","user":"U2147483697",` +
	`"text":"<@U0OTHER99> lunch?","ts":"1763544602.000500","channel_type":"channel"}`)

// A file shared in a direct message.
var shared = envelope(`{"type":"message","subtype":"file_share","channel":"D024BE91L","user":"U2147483697",` +
	`"text":"what is wrong here?","ts":"1763544603.000600","channel_type":"im","files":[` +
	`{"id":"F0S43PZDF","name":"report.pdf","mimetype":"application/pdf","size":48210,"mode":"hosted",` +
	`"url_private_download":"https://files.slack.com/files-pri/T061EG9R6-F0S43PZDF/download/report.pdf"},` +
	`{"id":"F0HIDDEN1","name":"old.png","mode":"hidden_by_limit"}]}`)

// Slack signs a timestamp with the body and refuses neither: the suite asks
// for both a tampered body and a stale delivery to be refused. Its files are
// downloaded, so it names them by their download address.
func TestSlackPassesTheConformanceSuite(t *testing.T) {
	providertest.Run(t, slack.New(), providertest.Fixtures{
		Creds:             creds(),
		Request:           request(direct),
		WantExternalID:    "D024BE91L:1763544600.000200",
		EnforcesFreshness: true,
		AttachmentRequest: request(shared),
	})
}

func parse(t *testing.T, body string) gateway.Result {
	t.Helper()
	got, err := slack.New().Parse(request(body), creds())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got
}

func one(t *testing.T, body string) gateway.Inbound {
	t.Helper()
	got := parse(t, body)
	if len(got.Messages) != 1 {
		t.Fatalf("messages %+v, want one", got.Messages)
	}
	return got.Messages[0]
}

// Saving a Request URL sends a challenge, which goes back as it came.
func TestTheURLCheckIsAnsweredWithItsChallenge(t *testing.T) {
	got := parse(t, `{"token":"Jhj5dZrVaK7ZwHHjRyZWjbDl","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","type":"url_verification"}`)
	if string(got.Ack) != "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P" || len(got.Messages) != 0 {
		t.Errorf("result %+v, want the challenge and no message", got)
	}
	for _, body := range []string{
		`{"type":"url_verification"}`,
		`{"type":"url_verification","challenge":"` + strings.Repeat("a", 257) + `"}`,
	} {
		if _, err := slack.New().Parse(request(body), creds()); err == nil {
			t.Errorf("Parse(%s) took a challenge it should not echo", body[:min(len(body), 60)])
		}
	}
}

// Each kind of conversation is named as the design table says.
func TestEachConversationIsNamedForWhereItIsAnswered(t *testing.T) {
	cases := []struct {
		name, body string
		ref        string
		kind       gateway.SessionKind
		replyTo    string
	}{
		{"a direct message", direct, "D024BE91L", gateway.SessionDirect, ""},
		{"a mention at the top of a channel starts its thread", mention, "C2147483705:1763544601.000300", gateway.SessionThread, ""},
		{"a reply in a thread is that thread's", threadReply, "C2147483705:1763544601.000300", gateway.SessionThread, "C2147483705:1763544601.000300"},
		{"a thread in a direct message is the direct message", strings.Replace(direct, `"channel_type":"im"`, `"thread_ts":"1763544000.000100","channel_type":"im"`, 1), "D024BE91L", gateway.SessionDirect, "D024BE91L:1763544000.000100"},
		{"a group DM is a group's thread", strings.Replace(mention, `"channel_type":"channel"`, `"channel_type":"mpim"`, 1), "C2147483705:1763544601.000300", gateway.SessionThread, ""},
		{"a private channel too", strings.Replace(mention, `"channel_type":"channel"`, `"channel_type":"group"`, 1), "C2147483705:1763544601.000300", gateway.SessionThread, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := one(t, c.body)
			if in.Session.Ref != c.ref || in.Session.Kind != c.kind || in.ReplyTo != c.replyTo {
				t.Errorf("session %+v reply to %q, want %s %s reply to %q", in.Session, in.ReplyTo, c.ref, c.kind, c.replyTo)
			}
		})
	}
}

// A message is named by its channel and ts, since a ts repeats across
// channels.
func TestAMessageIsNamedByItsChannelAndTS(t *testing.T) {
	in := one(t, mention)
	if in.ExternalID != "C2147483705:1763544601.000300" {
		t.Errorf("external id %q", in.ExternalID)
	}
	if want := time.Date(2025, 11, 19, 9, 30, 1, 300000, time.UTC); !in.SentAt.Equal(want) {
		t.Errorf("sent at %v, want %v", in.SentAt, want)
	}
	if in.Author.Ref != "U2147483697" || in.Author.IsBot {
		t.Errorf("author %+v", in.Author)
	}
}

// A mention of our bot is ours; another is somebody else's.
func TestAMentionIsOursOnlyWhenItNamesOurBot(t *testing.T) {
	in := one(t, envelope(`{"type":"message","channel":"C2147483705","user":"U2147483697",`+
		`"text":"<@U0BOT1234|openarity> and <@U0OTHER99> look","ts":"1763544601.000300","channel_type":"channel"}`))
	if len(in.Mentions) != 2 || !in.Mentions[0].IsUs || in.Mentions[1].IsUs || in.Mentions[1].SenderRef != "U0OTHER99" {
		t.Errorf("mentions %+v", in.Mentions)
	}
}

// What is nothing to do: a channel's chatter that names no bot, our own
// bot's message (the loop), an edit, a delete, a join, another event, and a
// message with no person behind it.
func TestWhatIsNothingToDo(t *testing.T) {
	cases := map[string]string{
		"chatter at the top of a channel": chatter,
		"our own bot":                     strings.Replace(direct, `"user":"U2147483697"`, `"user":"U0BOT1234","bot_id":"B0BOT"`, 1),
		"an edit":                         strings.Replace(direct, `"type":"message",`, `"type":"message","subtype":"message_changed",`, 1),
		"a delete":                        strings.Replace(direct, `"type":"message",`, `"type":"message","subtype":"message_deleted",`, 1),
		"a join":                          strings.Replace(direct, `"type":"message",`, `"type":"message","subtype":"channel_join",`, 1),
		"a reaction":                      envelope(`{"type":"reaction_added","user":"U2147483697","reaction":"thumbsup"}`),
		"no user":                         strings.Replace(direct, `"user":"U2147483697",`, ``, 1),
		"an app rate limit notice":        `{"type":"app_rate_limited","team_id":"T061EG9R6","minute_rate_limited":1518467820}`,
		"a channel we do not know":        strings.Replace(direct, `"channel_type":"im"`, `"channel_type":"app_home"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got := parse(t, body); len(got.Messages) != 0 || got.Ack != nil {
				t.Errorf("result %+v, want nothing", got)
			}
		})
	}
}

// Another app's bot is somebody: it is kept, marked as a bot.
func TestAnotherBotIsKeptAsABot(t *testing.T) {
	in := one(t, strings.Replace(direct, `"user":"U2147483697"`, `"user":"U0OTHERBOT","bot_id":"B0OTHER"`, 1))
	if !in.Author.IsBot {
		t.Errorf("author %+v, want a bot", in.Author)
	}
}

// The name is the one the person chose, then their full name, then none.
func TestTheSenderIsNamedFromTheirProfile(t *testing.T) {
	if got := one(t, direct).Author.DisplayName; got != "asha" {
		t.Errorf("name %q, want the display name", got)
	}
	noDisplay := strings.Replace(direct, `"display_name":"asha",`, `"display_name":" ",`, 1)
	if got := one(t, noDisplay).Author.DisplayName; got != "Asha Rao" {
		t.Errorf("name %q, want the real name", got)
	}
	if got := one(t, mention).Author.DisplayName; got != "" {
		t.Errorf("name %q, want none without a profile", got)
	}
}

// A file Slack hosts is named by where it downloads from; a hidden one is not.
func TestAFileIsNamedByItsDownloadAddress(t *testing.T) {
	in := one(t, shared)
	if len(in.Attachments) != 1 {
		t.Fatalf("attachments %+v, want the hosted file alone", in.Attachments)
	}
	a := in.Attachments[0]
	if a.Ref != "https://files.slack.com/files-pri/T061EG9R6-F0S43PZDF/download/report.pdf" ||
		a.ClaimedFilename != "report.pdf" || a.ClaimedMediaType != "application/pdf" || a.ClaimedSize != 48210 {
		t.Errorf("attachment %+v", a)
	}
}

func TestAMessageWhoseTSIsNotATimeIsRefused(t *testing.T) {
	for _, ts := range []string{"later", "1763544600.12", "1763544600.abcdef"} {
		body := strings.Replace(direct, `"ts":"1763544600.000200"`, `"ts":"`+ts+`"`, 1)
		if _, err := slack.New().Parse(request(body), creds()); err == nil {
			t.Errorf("ts %q was taken", ts)
		}
	}
}

// Freshness is symmetric: a delivery signed five minutes ahead is as stale as
// one five minutes behind.
func TestATimestampTooFarEitherWayIsRefused(t *testing.T) {
	p := slack.New()
	for _, d := range []time.Duration{-5*time.Minute - time.Second, 5*time.Minute + time.Second} {
		if err := p.Verify(signed(direct, receivedAt.Add(d)), creds()); err == nil {
			t.Errorf("a delivery signed %v from arrival was taken", d)
		}
	}
	for _, d := range []time.Duration{-5 * time.Minute, 5 * time.Minute} {
		if err := p.Verify(signed(direct, receivedAt.Add(d)), creds()); err != nil {
			t.Errorf("a delivery signed %v from arrival was refused: %v", d, err)
		}
	}
	req := request(direct)
	req.Header.Set(slack.TimestampHeader, "soon")
	if err := p.Verify(req, creds()); err == nil {
		t.Error("a timestamp that is not a number was taken")
	}
}

// The signature covers the timestamp too: moving it is a forgery.
func TestTheSignatureCoversTheTimestamp(t *testing.T) {
	req := request(direct)
	req.Header.Set(slack.TimestampHeader, strconv.FormatInt(receivedAt.Unix()+1, 10))
	if err := slack.New().Verify(req, creds()); err == nil {
		t.Error("a timestamp the signature does not cover was taken")
	}
}
