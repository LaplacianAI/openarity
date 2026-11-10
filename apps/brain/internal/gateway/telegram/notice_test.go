package telegram_test

import (
	"encoding/json"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
)

func notice(t *testing.T, n gateway.Notice) (gateway.OutboundRequest, sent) {
	t.Helper()
	nt, ok := telegram.New().(gateway.Notifier)
	if !ok {
		t.Fatal("telegram sends no notices")
	}
	req, err := nt.Notice(gateway.Channel{}, n, creds())
	if err != nil {
		t.Fatalf("Notice: %v", err)
	}
	var b sent
	if err := json.Unmarshal(req.Body, &b); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return req, b
}

func waiting(session gateway.Session, name string) gateway.Notice {
	return gateway.Notice{
		ID: "notice:1", Kind: gateway.NoticeWaiting, Session: session,
		InReplyTo: session.Ref + ":7", Sender: gateway.Author{Ref: "4242", DisplayName: name},
		Text: gateway.NoticeText(gateway.NoticeWaiting),
	}
}

func TestAWaitingNoticeInAPrivateChatNamesTheSenderAndQuotesThem(t *testing.T) {
	t.Parallel()

	req, b := notice(t, waiting(gateway.Session{Ref: "4242", Kind: gateway.SessionDirect}, "Erik"))
	if req.URL != "https://api.telegram.org/bot"+token+"/sendRichMessage" {
		t.Errorf("URL = %s", req.URL)
	}
	want := "Erik, " + gateway.NoticeText(gateway.NoticeWaiting)
	if b.ChatID != 4242 || b.RichMessage.Markdown != want {
		t.Errorf("sent %d %q, want 4242 %q", b.ChatID, b.RichMessage.Markdown, want)
	}
	if b.ReplyParameters == nil || b.ReplyParameters.MessageID != 7 {
		t.Errorf("reply_parameters = %+v, want it to quote message 7", b.ReplyParameters)
	}
}

// In a group the sender is named by a link to their account, so nobody else
// in the room takes the notice as theirs.
func TestAWaitingNoticeInAGroupTagsTheSender(t *testing.T) {
	t.Parallel()

	_, b := notice(t, waiting(gateway.Session{Ref: "-1001234567890", Kind: gateway.SessionGroup}, "Erik"))
	want := "[Erik](tg://user?id=4242), " + gateway.NoticeText(gateway.NoticeWaiting)
	if b.RichMessage.Markdown != want {
		t.Errorf("markdown = %q, want %q", b.RichMessage.Markdown, want)
	}
}

func TestANoticeInATopicGoesToTheTopic(t *testing.T) {
	t.Parallel()

	_, b := notice(t, waiting(gateway.Session{Ref: "-1001234567890:topic:12", Kind: gateway.SessionThread}, "Erik"))
	if b.ChatID != -1001234567890 || b.MessageThreadID != 12 {
		t.Errorf("sent to %d topic %d, want -1001234567890 topic 12", b.ChatID, b.MessageThreadID)
	}
}

// A name is the sender's own text: written as a link or as emphasis, it must
// stay words, never become a link the bot posts.
func TestASendersNameIsEscaped(t *testing.T) {
	t.Parallel()

	_, b := notice(t, waiting(gateway.Session{Ref: "-100", Kind: gateway.SessionGroup}, "[click](https://evil.example) *now*"))
	want := `[\[click\]\(https\:\/\/evil\.example\) \*now\*](tg://user?id=4242), ` + gateway.NoticeText(gateway.NoticeWaiting)
	if b.RichMessage.Markdown != want {
		t.Errorf("markdown = %q, want %q", b.RichMessage.Markdown, want)
	}
}

func TestAnAdmittedNoticeWithNoQuoteQuotesNothing(t *testing.T) {
	t.Parallel()

	n := waiting(gateway.Session{Ref: "4242", Kind: gateway.SessionDirect}, "")
	n.Kind, n.Text, n.InReplyTo = gateway.NoticeAdmitted, gateway.NoticeText(gateway.NoticeAdmitted), ""
	_, b := notice(t, n)
	if b.ReplyParameters != nil {
		t.Errorf("reply_parameters = %+v, want none", b.ReplyParameters)
	}
	if want := "Hello, " + gateway.NoticeText(gateway.NoticeAdmitted); b.RichMessage.Markdown != want {
		t.Errorf("markdown = %q, want %q", b.RichMessage.Markdown, want)
	}
}

func TestANoticeWithNoTokenNoChatOrNoWordsIsRefused(t *testing.T) {
	t.Parallel()

	nt, _ := telegram.New().(gateway.Notifier)
	ok := waiting(gateway.Session{Ref: "4242", Kind: gateway.SessionDirect}, "Erik")
	noWords := ok
	noWords.Text = ""
	noChat := ok
	noChat.Session.Ref = "not-a-chat"

	for name, tc := range map[string]struct {
		n     gateway.Notice
		creds gateway.Credentials
	}{
		"no token": {ok, gateway.Credentials{}},
		"no words": {noWords, creds()},
		"no chat":  {noChat, creds()},
	} {
		if _, err := nt.Notice(gateway.Channel{}, tc.n, tc.creds); err == nil {
			t.Errorf("%s: Notice succeeded", name)
		}
	}
}
