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
	in, ok := Map(m, us, "", false, false)
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
		if in, ok := Map(m, us, "", false, false); ok {
			t.Errorf("%s was kept as %+v", name, in)
		}
	}
}

func TestAHistoricalMessageIsMarkedSo(t *testing.T) {
	in, ok := Map(direct(text("from last week")), us, "", true, false)
	if !ok || !in.Historical {
		t.Fatalf("Map = %+v, %v; want it kept and marked historical", in, ok)
	}
}

// "Message yourself" is the owner asking the bot: kept, by their number
// whichever of their devices or names it came by, as one direct conversation.
func TestMessageYourselfIsTheOwnerAskingTheBot(t *testing.T) {
	byNumber := direct(text("summarise today"))
	byNumber.Info.Chat, byNumber.Info.Sender, byNumber.Info.IsFromMe = us.PN, types.NewADJID(us.PN.User, 0, 2), true
	byLID := direct(text("summarise today"))
	byLID.Info.Chat, byLID.Info.Sender, byLID.Info.IsFromMe = us.LID, types.NewADJID(us.LID.User, 0, 3), true

	for name, m := range map[string]*events.Message{"by number": byNumber, "by LID": byLID} {
		t.Run(name, func(t *testing.T) {
			in := mapped(t, m)
			if !in.ToSelf {
				t.Error("not marked as the owner writing to the account")
			}
			if in.Author.Ref != us.PN.String() || in.Session != (gateway.Session{Ref: us.PN.String(), Kind: gateway.SessionDirect}) {
				t.Errorf("author %q, session %+v; want the owner's number, a direct conversation", in.Author.Ref, in.Session)
			}
		})
	}
}

// The owner's own anywhere but "Message yourself" is them talking to someone,
// and from before the number was linked it may be the bot's old answer.
func TestTheOwnersOwnElsewhereIsStillDropped(t *testing.T) {
	toPriya := direct(text("see you at six"))
	toPriya.Info.IsFromMe = true
	inAGroup := inGroup(text("lunch?"))
	inAGroup.Info.IsFromMe, inAGroup.Info.Chat = true, us.PN // a group never is the number itself
	fromBefore := direct(text("an old note"))
	fromBefore.Info.Chat, fromBefore.Info.Sender, fromBefore.Info.IsFromMe = us.PN, us.PN, true
	byLIDOnly := direct(text("a note"))
	byLIDOnly.Info.Chat, byLIDOnly.Info.Sender, byLIDOnly.Info.IsFromMe = us.LID, us.LID, true

	for name, c := range map[string]struct {
		m          *events.Message
		self       Self
		historical bool
	}{
		"to someone else":           {toPriya, us, false},
		"in a group":                {inAGroup, us, false},
		"from before linking":       {fromBefore, us, true},
		"with the number not known": {fromBefore, Self{}, false},
		"with only the LID known":   {byLIDOnly, Self{LID: us.LID}, false},
	} {
		if in, ok := Map(c.m, c.self, "", c.historical, false); ok {
			t.Errorf("%s: kept %+v", name, in)
		}
	}
}

// Someone else's message never is the owner's, even in a chat named like it.
func TestOnlyTheOwnerIsToSelf(t *testing.T) {
	m := direct(text("hi"))
	m.Info.Chat = us.PN
	if in := mapped(t, m); in.ToSelf {
		t.Error("a message not from the owner was marked as the owner's to itself")
	}
}

// With a trigger word the owner calls the bot from any chat: their own
// message holding the word is kept, by their number, in the conversation it
// was typed in; a direct chat's is the other person's conversation, so the
// answer goes to them.
func TestTheOwnerCallsTheBotByItsWordFromAnyChat(t *testing.T) {
	inDirect := direct(text("@Fduv when is the next train?"))
	inDirect.Info.Sender, inDirect.Info.IsFromMe = types.NewADJID(us.PN.User, 0, 2), true
	byLID := direct(text("@fduv and after that?"))
	byLID.Info.Chat, byLID.Info.RecipientAlt = types.NewJID("2222", types.HiddenUserServer), priya
	byLID.Info.Sender, byLID.Info.IsFromMe = us.LID, true
	inAGroup := inGroup(text("@Fduv what did we decide?"))
	inAGroup.Info.Sender, inAGroup.Info.IsFromMe = us.PN, true

	for name, c := range map[string]struct {
		m       *events.Message
		session gateway.Session
	}{
		"in a direct chat":    {inDirect, gateway.Session{Ref: priya.String(), Kind: gateway.SessionDirect}},
		"in one named by LID": {byLID, gateway.Session{Ref: priya.String(), Kind: gateway.SessionDirect}},
		"in a group":          {inAGroup, gateway.Session{Ref: group.String(), Kind: gateway.SessionGroup}},
	} {
		in, ok := Map(c.m, us, "@Fduv", false, false)
		if !ok {
			t.Fatalf("%s: dropped", name)
		}
		if in.Author.Ref != us.PN.String() || in.Session != c.session || in.ToSelf {
			t.Errorf("%s: author %q, session %+v, to self %v; want the owner, in that chat", name, in.Author.Ref, in.Session, in.ToSelf)
		}
	}
}

// Without the word the owner is talking to someone, and nobody asked the
// bot: not kept, nor are the owner's from before, nor any of theirs when the
// channel has no word.
func TestTheOwnersOwnWithoutTheWordIsNotKept(t *testing.T) {
	plain := direct(text("see you at six"))
	plain.Info.IsFromMe = true
	nearly := direct(text("@Fduvx nearly"))
	nearly.Info.IsFromMe = true
	called := direct(text("@Fduv summarise"))
	called.Info.IsFromMe = true

	for name, c := range map[string]struct {
		m          *events.Message
		trigger    string
		historical bool
	}{
		"without the word":          {plain, "@Fduv", false},
		"with a longer word":        {nearly, "@Fduv", false},
		"from before linking":       {called, "@Fduv", true},
		"on a channel with no word": {called, "", false},
	} {
		if in, ok := Map(c.m, us, c.trigger, c.historical, false); ok {
			t.Errorf("%s: kept %+v", name, in)
		}
	}
}

// Once a channel has a word, "Message yourself" is called by it like every
// other chat: a note without it is the owner's own, and not kept.
func TestMessageYourselfNeedsTheWordOnceOneIsSet(t *testing.T) {
	note := func(words string) *events.Message {
		m := direct(text(words))
		m.Info.Chat, m.Info.Sender, m.Info.IsFromMe = us.PN, us.PN, true
		return m
	}
	if in, ok := Map(note("buy milk"), us, "@Fduv", false, false); ok {
		t.Errorf("a note without the word was kept: %+v", in)
	}
	in, ok := Map(note("@Fduv summarise today"), us, "@Fduv", false, false)
	if !ok || !in.ToSelf || in.Session.Ref != us.PN.String() {
		t.Fatalf("a note with the word = %+v, %v; want it kept, to self", in, ok)
	}
	if _, ok := Map(note("buy milk"), us, "", false, false); !ok {
		t.Error("with no word, a note to self was dropped; want it kept, as before")
	}
}

// On a number other channels link too, everyone's message must hold the
// word, the history from linking included: each channel hears every
// message, and one without a word would answer what was meant for another.
func TestOnASharedNumberEveryMessageMustHoldTheWord(t *testing.T) {
	cases := []struct {
		name       string
		m          *events.Message
		historical bool
		kept       bool
	}{
		{"a stranger, with the word", direct(text("@Dhars any jobs?")), false, true},
		{"a stranger, without it", direct(text("any jobs?")), false, false},
		{"a stranger, with another channel's word", direct(text("@Fduv hi")), false, false},
		{"history, without the word", direct(text("from last week")), true, false},
		{"history, with it", direct(text("@Dhars from last week")), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := Map(c.m, us, "@Dhars", c.historical, true); ok != c.kept {
				t.Errorf("kept = %v, want %v", ok, c.kept)
			}
		})
	}
	// A number of its own is as it was: a stranger needs no word.
	if _, ok := Map(direct(text("any jobs?")), us, "@Dhars", false, false); !ok {
		t.Error("a stranger on a number of its own was asked for the word")
	}
}
