package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

var (
	us     = Self{PN: types.NewJID("919000000001", types.DefaultUserServer), LID: types.NewJID("111111", types.HiddenUserServer)}
	priya  = types.NewJID("919876543210", types.DefaultUserServer)
	group  = types.NewJID("120363041234567890", types.GroupServer)
	sentAt = time.Unix(1_763_190_000, 0)
)

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func direct(m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: priya, Sender: priya},
			ID:            "3EB0C767D82B8A1E", PushName: "Priya", Timestamp: sentAt,
		},
		Message: m,
	}
}

func inGroup(m *waE2E.Message) *events.Message {
	e := direct(m)
	e.Info.Chat, e.Info.IsGroup = group, true
	return e
}

func mapped(t *testing.T, m *events.Message) gateway.Inbound {
	t.Helper()
	in, ok := Map(m, us, false)
	if !ok {
		t.Fatal("the message was dropped")
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("the Inbound is not valid: %v", err)
	}
	return in
}

func TestADirectMessageIsTheirConversation(t *testing.T) {
	in := mapped(t, direct(text("why did the deploy fail?")))

	want := gateway.Inbound{
		ExternalID: "3EB0C767D82B8A1E",
		Author:     gateway.Author{Ref: "919876543210@s.whatsapp.net", DisplayName: "Priya"},
		Session:    gateway.Session{Ref: "919876543210@s.whatsapp.net", Kind: gateway.SessionDirect},
		Text:       "why did the deploy fail?",
		SentAt:     sentAt,
	}
	if in.ExternalID != want.ExternalID || in.Author != want.Author || in.Session != want.Session ||
		in.Text != want.Text || !in.SentAt.Equal(sentAt) || in.Historical || len(in.Mentions) != 0 {
		t.Fatalf("Map = %+v\nwant %+v", in, want)
	}
}

// A person reached by LID is the same person by number: one sender, and one
// direct conversation, however the message was addressed.
func TestASenderByLIDIsNamedByTheirNumber(t *testing.T) {
	m := direct(text("hi"))
	lid := types.NewJID("2222222", types.HiddenUserServer)
	m.Info.Chat, m.Info.Sender, m.Info.SenderAlt = lid, lid, priya

	in := mapped(t, m)
	if in.Author.Ref != priya.String() || in.Session.Ref != priya.String() {
		t.Fatalf("author %q, session %q; want both %q", in.Author.Ref, in.Session.Ref, priya)
	}
}

// A sender's device is not a second sender.
func TestASendersDeviceIsDropped(t *testing.T) {
	m := direct(text("hi"))
	m.Info.Sender = types.JID{User: priya.User, Server: priya.Server, Device: 7}

	if in := mapped(t, m); in.Author.Ref != priya.String() {
		t.Fatalf("author %q; want %q, the person not the device", in.Author.Ref, priya)
	}
}

func TestAGroupMentionOfUsByNumberOrLIDAsksUs(t *testing.T) {
	for _, mention := range []string{us.PN.String(), us.LID.String()} {
		m := inGroup(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String("@bot why?"),
			ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{mention, "919555@s.whatsapp.net"}, StanzaID: proto.String("3EB0AAAA")},
		}})

		in := mapped(t, m)
		if in.Session != (gateway.Session{Ref: group.String(), Kind: gateway.SessionGroup}) {
			t.Fatalf("session = %+v; want the group", in.Session)
		}
		if len(in.Mentions) != 2 || !in.Mentions[0].IsUs || in.Mentions[1].IsUs {
			t.Fatalf("mentions = %+v; want us, then someone else", in.Mentions)
		}
		if in.ReplyTo != "3EB0AAAA" {
			t.Errorf("ReplyTo = %q; want the quoted message", in.ReplyTo)
		}
	}
}

func TestAGroupMessageThatMentionsSomeoneElseDoesNotAskUs(t *testing.T) {
	m := inGroup(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("@ravi look"), ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{"919555@s.whatsapp.net", "not a jid@@"}},
	}})

	in := mapped(t, m)
	if len(in.Mentions) != 1 || in.Mentions[0].IsUs {
		t.Fatalf("mentions = %+v; want ravi alone, not us, and nothing for what is not an address", in.Mentions)
	}
}

func TestAFileIsNamedForTheConnectionToFetch(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		want gateway.Attachment
		text string
	}{
		{
			"photo", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("this one"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(81234)}},
			gateway.Attachment{Ref: "3EB0C767D82B8A1E:0", ClaimedFilename: "photo-3EB0C767D82B8A1E.jpg", ClaimedMediaType: "image/jpeg", ClaimedSize: 81234},
			"this one",
		},
		{
			"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("crash.log"), Mimetype: proto.String("text/plain"), FileLength: proto.Uint64(40)}},
			gateway.Attachment{Ref: "3EB0C767D82B8A1E:0", ClaimedFilename: "crash.log", ClaimedMediaType: "text/plain", ClaimedSize: 40},
			"",
		},
		{
			"voice note", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"), FileLength: proto.Uint64(9000)}},
			gateway.Attachment{Ref: "3EB0C767D82B8A1E:0", ClaimedFilename: "audio-3EB0C767D82B8A1E.ogg", ClaimedMediaType: "audio/ogg; codecs=opus", ClaimedSize: 9000},
			"",
		},
	}
	for _, tc := range cases {
		in := mapped(t, direct(tc.msg))
		if len(in.Attachments) != 1 || in.Attachments[0] != tc.want || in.Text != tc.text {
			t.Errorf("%s: files %+v, text %q; want %+v, %q", tc.name, in.Attachments, in.Text, tc.want, tc.text)
		}
	}
}

func TestWhatIsNotAConversationIsDropped(t *testing.T) {
	own := direct(text("typed on the phone"))
	own.Info.IsFromMe = true
	edit := direct(text("fixed a typo"))
	edit.IsEdit = true
	status := direct(text("my story"))
	status.Info.Chat = types.StatusBroadcastJID
	channel := direct(text("news"))
	channel.Info.Chat = types.NewJID("1203630", types.NewsletterServer)

	for name, m := range map[string]*events.Message{
		"the owner's own":   own,
		"an edit":           edit,
		"a status":          status,
		"a channel post":    channel,
		"a reaction":        direct(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}),
		"a deletion":        direct(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}),
		"a sticker":         direct(&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}),
		"nothing but space": direct(text("  \n ")),
		"no message":        {Info: direct(nil).Info},
		"nil":               nil,
	} {
		if in, ok := Map(m, us, false); ok {
			t.Errorf("%s was kept as %+v", name, in)
		}
	}
}

func TestAHistoricalMessageIsMarkedSo(t *testing.T) {
	in, ok := Map(direct(text("from last week")), us, true)
	if !ok || !in.Historical {
		t.Fatalf("Map = %+v, %v; want it kept and marked historical", in, ok)
	}
}
