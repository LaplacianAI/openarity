package slack_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

func lookups(t *testing.T) interface {
	gateway.DirectOpener
	gateway.ChannelLister
} {
	t.Helper()
	p, ok := slack.New().(interface {
		gateway.DirectOpener
		gateway.ChannelLister
	})
	if !ok {
		t.Fatal("slack opens no direct conversation or lists no channel")
	}
	return p
}

func form(t *testing.T, body []byte) url.Values {
	t.Helper()
	v, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("the body is not a form: %v", err)
	}
	return v
}

// The maker's direct conversation is opened under the bot's token, with the
// user named and nothing else.
func TestADirectConversationIsOpenedForTheUser(t *testing.T) {
	t.Parallel()
	p := lookups(t)
	req, err := p.DirectRequest(creds(), "U0MAKER12")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL, "/conversations.open") || req.Header.Get("Authorization") != "Bearer "+token ||
		form(t, req.Body).Get("users") != "U0MAKER12" || req.Accepted != nil {
		t.Errorf("request = %s %v %q", req.URL, req.Header, req.Body)
	}
	if _, err := p.DirectRequest(creds(), "C0CHANNEL"); err == nil {
		t.Error("a channel id was taken for a user")
	}
	if _, err := p.DirectRequest(gateway.Credentials{}, "U0MAKER12"); err == nil {
		t.Error("no token, and a request was built")
	}
}

func TestADirectConversationsRefusalSaysWhatToDo(t *testing.T) {
	t.Parallel()
	p := lookups(t)
	ref, err := p.DirectRef([]byte(`{"ok":true,"channel":{"id":"D0DM12345"}}`))
	if err != nil || ref != "D0DM12345" {
		t.Errorf("DirectRef = %q, %v", ref, err)
	}
	for body, says := range map[string]string{
		`{"ok":false,"error":"missing_scope"}`:       "im:write",
		`{"ok":false,"error":"user_not_found"}`:      "does not know you",
		`{"ok":false,"error":"rate<script>limited"}`: "ratescriptlimited",
		`{"ok":true,"channel":{"id":"not an id"}}`:   "no id",
		`not json`: "could not be read",
	} {
		if _, err := p.DirectRef([]byte(body)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("DirectRef(%s) = %v, want it to say %q", body, err, says)
		}
	}
}

func TestAChannelIsLookedUpAsTheBotSeesIt(t *testing.T) {
	t.Parallel()
	p := lookups(t)
	req, err := p.ChannelRequest(creds(), "C0RELEASE")
	if err != nil || !strings.HasSuffix(req.URL, "/conversations.info") || form(t, req.Body).Get("channel") != "C0RELEASE" {
		t.Fatalf("ChannelRequest = %+v, %v", req, err)
	}
	for _, bad := range []string{"D0DM12345", "general", "C0X/../"} {
		if _, err := p.ChannelRequest(creds(), bad); err == nil {
			t.Errorf("%q was taken for a channel", bad)
		}
	}
	name, err := p.ChannelName([]byte(`{"ok":true,"channel":{"name":"releases","is_member":true}}`))
	if err != nil || name != "releases" {
		t.Errorf("ChannelName = %q, %v", name, err)
	}
	for body, says := range map[string]string{
		`{"ok":true,"channel":{"name":"releases","is_member":false}}`:                  "/invite",
		`{"ok":true,"channel":{"name":"old","is_member":true,"is_archived":true}}`:     "archived",
		`{"ok":true,"channel":{"name":"x","is_member":true,"is_im":true}}`:             "direct message",
		`{"ok":true,"channel":{"name":"mpdm-a--b-1","is_member":true,"is_mpim":true}}`: "direct message",
		`{"ok":false,"error":"channel_not_found"}`:                                     "invite the app",
		`{"ok":false,"error":"missing_scope"}`:                                         "channels:read",
	} {
		if _, err := p.ChannelName([]byte(body)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ChannelName(%s) = %v, want it to say %q", body, err, says)
		}
	}
}

func TestChannelsAreListedAPageAtATime(t *testing.T) {
	t.Parallel()
	p := lookups(t)
	req, err := p.ListRequest(creds(), "")
	v := form(t, req.Body)
	if err != nil || !strings.HasSuffix(req.URL, "/conversations.list") || v.Get("types") != "public_channel,private_channel" ||
		v.Get("exclude_archived") != "true" || v.Has("cursor") {
		t.Fatalf("ListRequest = %s %q, %v", req.URL, req.Body, err)
	}
	if req, _ := p.ListRequest(creds(), "dGVhbTpDMDYx"); form(t, req.Body).Get("cursor") != "dGVhbTpDMDYx" {
		t.Errorf("the next page's cursor was not sent: %q", req.Body)
	}
	got, next, err := p.ListPage([]byte(`{"ok":true,"channels":[
		{"id":"C0RELEASE","name":"releases","is_private":false,"is_member":true,"num_members":14},
		{"id":"G0SECRET1","name":"secret","is_private":true,"is_member":true,"num_members":3},
		{"id":"C0ARCHIVE","name":"old","is_archived":true},
		{"id":"not-an-id","name":"bad"}
	],"response_metadata":{"next_cursor":"bmV4dA=="}}`))
	if err != nil || next != "bmV4dA==" {
		t.Fatalf("ListPage = %v, %q, %v", got, next, err)
	}
	want := []gateway.PlatformChannel{
		{Ref: "C0RELEASE", Name: "releases", Member: true, Members: 14},
		{Ref: "G0SECRET1", Name: "secret", Private: true, Member: true, Members: 3},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("channels = %+v, want %+v (archived and malformed left out)", got, want)
	}
	if _, _, err := p.ListPage([]byte(`{"ok":false,"error":"missing_scope"}`)); err == nil ||
		!strings.Contains(err.Error(), "channels:read and groups:read") {
		t.Errorf("a missing scope = %v, want it named", err)
	}
}

// A message's landing place is read from chat.postMessage's answer, and only
// a channel and a timestamp are kept.
func TestWhereAPostLandedIsRead(t *testing.T) {
	t.Parallel()
	rp, ok := slack.New().(gateway.Replier)
	if !ok {
		t.Fatal("slack sends no replies")
	}
	reqs, err := rp.Reply(gateway.Channel{}, gateway.Outbound{
		ID: "r1", Session: gateway.Session{Ref: "C0RELEASE", Kind: gateway.SessionGroup}, Kind: "answer", Text: "Shipped.",
	}, creds(), receivedAt)
	if err != nil || len(reqs) == 0 || reqs[0].Posted == nil {
		t.Fatalf("Reply = %d requests, %v; want the first to read where it landed", len(reqs), err)
	}
	for body, want := range map[string]string{
		`{"ok":true,"channel":"C0RELEASE","ts":"1700000000.000100"}`:   "C0RELEASE:1700000000.000100",
		`{"ok":false,"error":"not_in_channel"}`:                        "",
		`{"ok":true,"channel":"C0RELEASE:x","ts":"1700000000.000100"}`: "",
		`{"ok":true,"channel":"C0RELEASE","ts":"soon"}`:                "",
	} {
		if got := reqs[0].Posted([]byte(body)); got != want {
			t.Errorf("Posted(%s) = %q, want %q", body, got, want)
		}
	}
}
