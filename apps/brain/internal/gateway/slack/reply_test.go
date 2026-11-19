package slack_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

type sent struct {
	Channel     string `json:"channel"`
	ThreadTS    string `json:"thread_ts"`
	Markdown    string `json:"markdown_text"`
	UnfurlLinks *bool  `json:"unfurl_links"`
	UnfurlMedia *bool  `json:"unfurl_media"`
}

func replier(t *testing.T) gateway.Replier {
	t.Helper()
	r, ok := slack.New().(gateway.Replier)
	if !ok {
		t.Fatal("slack is not a Replier")
	}
	return r
}

func reply(t *testing.T, ref, text string) []gateway.OutboundRequest {
	t.Helper()
	reqs, err := replier(t).Reply(gateway.Channel{}, gateway.Outbound{
		ID: "run-1", Session: gateway.Session{Ref: ref}, Kind: "answer", Text: text,
	}, creds(), receivedAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	return reqs
}

func body(t *testing.T, r gateway.OutboundRequest) sent {
	t.Helper()
	var s sent
	if err := json.Unmarshal(r.Body, &s); err != nil {
		t.Fatalf("body %s: %v", r.Body, err)
	}
	return s
}

// A channel's answer goes into the thread its session names, with the token
// in a header, never the URL, and nothing unfurled.
func TestAnAnswerGoesIntoItsThread(t *testing.T) {
	reqs := reply(t, "C2147483705:1763544601.000300", "## Deploy\n\nIt is **green**.")
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.URL != "https://slack.com/api/chat.postMessage" || strings.Contains(r.URL, token) {
		t.Errorf("url %q", r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("authorization %q", r.Header.Get("Authorization"))
	}
	if r.Accepted == nil {
		t.Error("no check of Slack's answer")
	}
	s := body(t, r)
	if s.Channel != "C2147483705" || s.ThreadTS != "1763544601.000300" || s.Markdown != "## Deploy\n\nIt is **green**." {
		t.Errorf("body %+v", s)
	}
	if s.UnfurlLinks == nil || *s.UnfurlLinks || s.UnfurlMedia == nil || *s.UnfurlMedia {
		t.Errorf("unfurling is not off: %+v", s)
	}
}

func TestADirectAnswerGoesInPlace(t *testing.T) {
	s := body(t, reply(t, "D024BE91L", "hi")[0])
	if s.Channel != "D024BE91L" || s.ThreadTS != "" {
		t.Errorf("body %+v", s)
	}
}

// An answer over 12,000 characters is several messages, all in the thread.
func TestALongAnswerIsSeveralMessagesInOrder(t *testing.T) {
	para := strings.Repeat("word ", 1000)
	text := strings.Join([]string{para, para, para}, "\n\n")
	reqs := reply(t, "C2147483705:1763544601.000300", text)
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want two", len(reqs))
	}
	for i, r := range reqs {
		s := body(t, r)
		if len([]rune(s.Markdown)) > 12000 || s.ThreadTS != "1763544601.000300" {
			t.Errorf("part %d: %d characters in thread %q", i, len([]rune(s.Markdown)), s.ThreadTS)
		}
	}
}

func TestAReplyWithNowhereToGoIsRefused(t *testing.T) {
	r := replier(t)
	if _, err := r.Reply(gateway.Channel{}, gateway.Outbound{Session: gateway.Session{Ref: ":123"}, Text: "x"}, creds(), receivedAt); err == nil {
		t.Error("a ref with no channel was sent")
	}
	if _, err := r.Reply(gateway.Channel{}, gateway.Outbound{Session: gateway.Session{Ref: "D1"}, Text: " "}, creds(), receivedAt); err == nil {
		t.Error("an empty answer was sent")
	}
	if _, err := r.Reply(gateway.Channel{}, gateway.Outbound{Session: gateway.Session{Ref: "D1"}, Text: "x"}, gateway.Credentials{}, receivedAt); err == nil {
		t.Error("a reply with no token was built")
	}
}

// An answer can never notify a whole channel: a broadcast in it is broken,
// whatever its case, while code keeps it as written.
func TestAnAnswerCannotBroadcast(t *testing.T) {
	cases := map[string]string{
		"<!here> look":           "<\u2060!here> look",
		"<!channel|channel>":     "<\u2060!channel|channel>",
		"<!subteam^S0123|@ops>":  "<\u2060!subteam^S0123|@ops>",
		"hey @channel and @HERE": "hey @\u2060channel and @\u2060HERE",
		"@everyone":              "@\u2060everyone",
		"`<!here>` and @asha":    "`<!here>` and @asha",
		"```\n<!channel>\n```":   "```\n<!channel>\n```",
		"![x](https://e.x/?d=1)": "[x](https://e.x/?d=1)",
		"mail me at a@b.example": "mail me at a@b.example",
	}
	for in, want := range cases {
		if got := slack.Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Slack says no inside a 200. A refusal no retry fixes gives up at once;
// a rate limit or an outage is tried again.
func TestSlacksAnswerIsRead(t *testing.T) {
	check := reply(t, "D024BE91L", "hi")[0].Accepted
	if err := check(http.StatusOK, []byte(`{"ok":true,"ts":"1763544700.000100"}`)); err != nil {
		t.Errorf("ok: %v", err)
	}
	if err := check(http.StatusOK, []byte(`{"ok":false,"error":"not_in_channel"}`)); !errors.Is(err, gateway.ErrUnsendable) {
		t.Errorf("not_in_channel: %v, want it given up", err)
	}
	err := check(http.StatusOK, []byte(`{"ok":false,"error":"ratelimited"}`))
	if err == nil || errors.Is(err, gateway.ErrUnsendable) {
		t.Errorf("ratelimited: %v, want it retried", err)
	}
	if err := check(http.StatusOK, []byte(`not json`)); err == nil {
		t.Error("an unreadable answer was taken as sent")
	}
	err = check(http.StatusOK, []byte(`{"ok":false,"error":"bad\u001b[2J`+strings.Repeat("x", 200)+`"}`))
	if err == nil || strings.Contains(err.Error(), "\x1b") || len(err.Error()) > 100 {
		t.Errorf("an error code was passed on as written: %q", err)
	}
}

func notifier(t *testing.T) gateway.Notifier {
	t.Helper()
	n, ok := slack.New().(gateway.Notifier)
	if !ok {
		t.Fatal("slack is not a Notifier")
	}
	return n
}

// A stranger is named by a mention in the thread they wrote in; a ref that
// is not a user id is never put in one.
func TestAStrangerIsToldInTheirThread(t *testing.T) {
	n := gateway.Notice{
		ID: "n1", Kind: gateway.NoticeWaiting, Session: gateway.Session{Ref: "C2147483705:1763544601.000300", Kind: gateway.SessionThread},
		Sender: gateway.Author{Ref: "U2147483697", DisplayName: "asha"}, Text: gateway.NoticeText(gateway.NoticeWaiting),
	}
	r, err := notifier(t).Notice(gateway.Channel{}, n, creds())
	if err != nil {
		t.Fatalf("Notice: %v", err)
	}
	s := body(t, r)
	if s.Channel != "C2147483705" || s.ThreadTS != "1763544601.000300" || !strings.HasPrefix(s.Markdown, "<@U2147483697>, ") {
		t.Errorf("body %+v", s)
	}
	n.Sender.Ref = "!channel"
	r, err = notifier(t).Notice(gateway.Channel{}, n, creds())
	if err != nil {
		t.Fatalf("Notice: %v", err)
	}
	if s := body(t, r); !strings.HasPrefix(s.Markdown, "Hello, ") {
		t.Errorf("a ref that is not a user id was mentioned: %q", s.Markdown)
	}
	n.Text = ""
	if _, err := notifier(t).Notice(gateway.Channel{}, n, creds()); err == nil {
		t.Error("a notice with no words was built")
	}
}
