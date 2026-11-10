package custom_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
)

var replyAt = time.Unix(1762600000, 0)

func replier(t *testing.T) gateway.Replier {
	t.Helper()
	reg, err := gateway.NewRegistry(custom.New())
	if err != nil {
		t.Fatal(err)
	}
	rp, ok := reg.Replier("custom")
	if !ok {
		t.Fatal("the registry did not find custom's Reply")
	}
	return rp
}

func outbound() gateway.Outbound {
	return gateway.Outbound{
		ID:        "0192f5c4-7d1e-7c3a-9b2e-4f6a8d0c1e23",
		Session:   gateway.Session{Ref: "D-asha", Kind: gateway.SessionDirect},
		InReplyTo: "m-1",
		Kind:      "answer",
		Text:      "The nightly export ran out of disk.",
	}
}

func to() gateway.Channel {
	return gateway.Channel{ID: uuid.New(), TeamID: uuid.New(), ReplyURL: "https://support.example.com/replies"}
}

func creds() gateway.Credentials { return gateway.Credentials{gateway.KeySigning: secret} }

// only is the one request a custom reply always is: a receiver takes a reply
// whole, so custom never splits one.
func only(t *testing.T, reqs []gateway.OutboundRequest) gateway.OutboundRequest {
	t.Helper()
	if len(reqs) != 1 {
		t.Fatalf("a custom reply is %d requests, want 1", len(reqs))
	}
	return reqs[0]
}

// What the receiver gets: the reply, at the URL, signed so they can check it
// with the secret they already hold.
func TestAReplyIsTheSignedPostOfTheAnswer(t *testing.T) {
	t.Parallel()

	reqs, err := replier(t).Reply(to(), outbound(), creds(), replyAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	req := only(t, reqs)
	if req.URL != "https://support.example.com/replies" {
		t.Errorf("URL = %q", req.URL)
	}
	ts := req.Header.Get(custom.HeaderTimestamp)
	if ts != strconv.FormatInt(replyAt.Unix(), 10) {
		t.Errorf("timestamp = %q, want the at it was given", ts)
	}
	if got, want := req.Header.Get(custom.HeaderSignature), custom.SignReply(secret, ts, req.Body); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	session, _ := body["session"].(map[string]any)
	if body["id"] != outbound().ID || body["in_reply_to"] != "m-1" || body["kind"] != "answer" ||
		body["text"] != outbound().Text || session["ref"] != "D-asha" || session["kind"] != "direct" {
		t.Errorf("body = %s", req.Body)
	}
}

// A reply that answers several messages answers none in particular.
func TestAReplyToSeveralMessagesNamesNone(t *testing.T) {
	t.Parallel()

	out := outbound()
	out.InReplyTo = ""
	reqs, err := replier(t).Reply(to(), out, creds(), replyAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	req := only(t, reqs)
	if bytes.Contains(req.Body, []byte("in_reply_to")) {
		t.Errorf("body %s names a message", req.Body)
	}
}

// A reply someone captured, posted back to the channel's webhook, must not
// pass as a message the team should answer: it would answer itself.
func TestACapturedReplyIsNotAValidMessage(t *testing.T) {
	t.Parallel()

	reqs, err := replier(t).Reply(to(), outbound(), creds(), replyAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	req := only(t, reqs)
	hook := gateway.WebhookRequest{
		Method: http.MethodPost, Header: req.Header, Body: req.Body, ReceivedAt: replyAt,
	}
	if err := custom.New().Verify(hook, creds()); err == nil {
		t.Error("a reply verified as an incoming message")
	}
}

// Reply is pure, like Parse: the same reply at the same moment is the same
// bytes, which is what lets the sender retry it.
func TestAReplyIsTheSameEveryTime(t *testing.T) {
	t.Parallel()

	rp, ch := replier(t), to()
	as, _ := rp.Reply(ch, outbound(), creds(), replyAt)
	bs, _ := rp.Reply(ch, outbound(), creds(), replyAt)
	a, b := only(t, as), only(t, bs)
	if !bytes.Equal(a.Body, b.Body) || a.Header.Get(custom.HeaderSignature) != b.Header.Get(custom.HeaderSignature) {
		t.Error("two identical replies differ")
	}
}

// Nothing to send to, or nothing to sign with, is not something a retry fixes.
func TestAReplyWithNowhereToGoOrNoSecretIsRefused(t *testing.T) {
	t.Parallel()

	noURL := to()
	noURL.ReplyURL = ""
	if _, err := replier(t).Reply(noURL, outbound(), creds(), replyAt); err == nil {
		t.Error("a channel with no reply URL produced a request")
	}
	if _, err := replier(t).Reply(to(), outbound(), gateway.Credentials{}, replyAt); err == nil {
		t.Error("a reply was signed with an empty secret")
	}
}

// custom posts to the address its channel names, so its channels take one.
func TestCustomTakesAReplyURL(t *testing.T) {
	t.Parallel()

	if _, ok := custom.New().(gateway.URLReplier); !ok {
		t.Error("custom does not take a reply URL")
	}
}
