package whatsapp

import (
	"reflect"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

func notice(t *testing.T, n gateway.Notice) Part {
	t.Helper()
	req, err := notifier(t).Notice(gateway.Channel{}, n, nil)
	if err != nil {
		t.Fatalf("Notice: %v", err)
	}
	p, err := ReadPart(req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestANoticeInAGroupMentionsItsSender(t *testing.T) {
	got := notice(t, gateway.Notice{
		ID: "notice:4", Kind: gateway.NoticeWaiting, InReplyTo: "3EB0",
		Session: gateway.Session{Ref: "120363041234567890@g.us", Kind: gateway.SessionGroup},
		Sender:  gateway.Author{Ref: "919876543210@s.whatsapp.net", DisplayName: "Priya"},
		Text:    gateway.NoticeText(gateway.NoticeWaiting),
	})
	want := Part{
		ID: "notice:4", To: "120363041234567890@g.us", Quote: "3EB0",
		Text:     "@919876543210 " + gateway.NoticeText(gateway.NoticeWaiting),
		Mentions: []string{"919876543210@s.whatsapp.net"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notice = %+v\nwant %+v", got, want)
	}
}

func TestANoticeInADirectChatUsesTheirName(t *testing.T) {
	n := gateway.Notice{
		ID: "notice:5", Kind: gateway.NoticeAdmitted,
		Session: gateway.Session{Ref: "919876543210@s.whatsapp.net", Kind: gateway.SessionDirect},
		Sender:  gateway.Author{Ref: "919876543210@s.whatsapp.net", DisplayName: "Priya"},
		Text:    gateway.NoticeText(gateway.NoticeAdmitted),
	}
	if got := notice(t, n); got.Text != "Priya, "+n.Text || got.Mentions != nil {
		t.Fatalf("notice = %+v; want their name and no mention", got)
	}
	n.Sender.DisplayName = ""
	if got := notice(t, n); got.Text != "Hello, "+n.Text {
		t.Errorf("a nameless sender's notice = %q", got.Text)
	}
}

func TestANoticeWithNoWordsIsRefused(t *testing.T) {
	_, err := notifier(t).Notice(gateway.Channel{}, gateway.Notice{
		ID: "notice:6", Session: gateway.Session{Ref: "919876543210@s.whatsapp.net"},
	}, nil)
	if err == nil {
		t.Fatal("a notice with no words was built")
	}
}
