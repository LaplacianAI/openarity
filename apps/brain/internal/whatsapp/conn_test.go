package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var priya = types.NewJID("919876543210", types.DefaultUserServer)

// connected is a fixture holding one channel whose device is linked.
func connected(t *testing.T) (*fixture, uuid.UUID, *fakeClient) {
	t.Helper()
	f := newFixture(t, newDevices(linkedJID))
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(ch, &linkedJID)}
	f.a.lease(t.Context())
	return f, ch, f.client(t, 0)
}

func incoming(id types.MessageID, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: priya, Sender: priya}, ID: id, PushName: "Priya"},
		Message: m,
	}
}

func TestAMessageIsHandedToTheGatewayWithItsFile(t *testing.T) {
	f, _, cli := connected(t)
	cli.download = []byte("a photo's bytes")
	cli.emit(incoming("M1", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("this one"), Mimetype: proto.String("image/jpeg"),
	}}))

	eventually(t, "the message delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	in := f.receiver.delivered()[0][0]
	if in.Text != "this one" || in.Author.Ref != priya.String() || in.Historical {
		t.Fatalf("delivered %+v", in)
	}
	f.receiver.mu.Lock()
	defer f.receiver.mu.Unlock()
	if len(f.receiver.files[0]) != 1 || f.receiver.files[0][0].SizeBytes != int64(len("a photo's bytes")) {
		t.Errorf("files kept %+v; want the photo, read from the connection", f.receiver.files[0])
	}
}

func TestTheOwnersOwnMessageIsNotHandedOver(t *testing.T) {
	f, _, cli := connected(t)
	own := incoming("M0", &waE2E.Message{Conversation: proto.String("typed on the phone")})
	own.Info.IsFromMe = true
	cli.emit(own)
	cli.emit(incoming("M1", &waE2E.Message{Conversation: proto.String("hi")}))

	eventually(t, "the second message delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	if got := f.receiver.delivered()[0][0].Text; got != "hi" {
		t.Fatalf("delivered %q; the owner's own message went first", got)
	}
}

// WhatsApp sends a message once; when the gateway fails, it is offered again
// a few times, and then said to be lost, never silently dropped.
func TestAMessageTheGatewayFailsIsTriedAgainThenLogged(t *testing.T) {
	f, _, cli := connected(t)
	f.receiver.errs = []error{errors.New("db down"), nil}
	cli.emit(incoming("M1", &waE2E.Message{Conversation: proto.String("hi")}))
	eventually(t, "delivered on the second try", func() bool { return len(f.receiver.delivered()) == 1 })

	f.receiver.mu.Lock()
	f.receiver.errs = []error{errors.New("db down"), errors.New("db down"), errors.New("db down")}
	f.receiver.mu.Unlock()
	cli.emit(incoming("M2", &waE2E.Message{Conversation: proto.String("again")}))
	eventually(t, "the loss logged", func() bool { return strings.Contains(f.logText(), "a message was not taken") })
	f.receiver.mu.Lock()
	defer f.receiver.mu.Unlock()
	if f.receiver.calls != 2+receiveTries {
		t.Errorf("offered %d times in all; want 2, then %d", f.receiver.calls, receiveTries)
	}
}

func TestATeamDeletedIsNotTriedAgain(t *testing.T) {
	f, _, cli := connected(t)
	f.receiver.errs = []error{gateway.ErrTeamDeleted}
	cli.emit(incoming("M1", &waE2E.Message{Conversation: proto.String("hi")}))
	eventually(t, "offered once", func() bool {
		f.receiver.mu.Lock()
		defer f.receiver.mu.Unlock()
		return f.receiver.calls == 1
	})
	if strings.Contains(f.logText(), "a message was not taken") {
		t.Error("a deleted team's message was logged as lost")
	}
}

// History is stored, never answered, without its files, and only the newest
// of each chat.
func TestHistoryIsKeptAsHistoricalNewestFirstAndBounded(t *testing.T) {
	f, _, cli := connected(t)
	var msgs []*waHistorySync.HistorySyncMsg
	for i := range historyPerChat + 10 {
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{ID: proto.String("H" + string(rune('A'+i%26)) + string(rune('a'+i/26)))},
			Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("old"), Mimetype: proto.String("image/jpeg")}},
		}})
	}
	cli.emit(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String(priya.String()), Messages: msgs},
		{ID: proto.String("@@@"), Messages: msgs[:1]},
	}}})

	eventually(t, "history delivered", func() bool { return len(f.receiver.delivered()) >= 1 })
	time.Sleep(50 * time.Millisecond) // a second batch, for the chat that names nobody, would be here by now
	if n := len(f.receiver.delivered()); n != 1 {
		t.Fatalf("%d chats' history delivered; want the one chat that names someone", n)
	}
	got := f.receiver.delivered()[0]
	if len(got) != historyPerChat {
		t.Fatalf("kept %d of a chat's history; want %d", len(got), historyPerChat)
	}
	for _, in := range got {
		if !in.Historical || len(in.Attachments) != 0 {
			t.Fatalf("history %+v; want historical, without files", in)
		}
	}
}

func TestPairingSuccessRecordsTheNumber(t *testing.T) {
	f, ch, cli := connected(t)
	cli.emit(&events.PairSuccess{ID: types.JID{User: "919000000005", Server: types.DefaultUserServer, Device: 3}})
	eventually(t, "the number recorded", func() bool { return len(f.store.snapshot().linked) == 1 })
	l := f.store.snapshot().linked[0]
	if l.ChannelID != ch || l.Holder != "test/1" || *l.Jid != "919000000005@s.whatsapp.net" || *l.Phone != "919000000005" {
		t.Fatalf("linked %+v; want the number, without its device, by this holder", l)
	}
}

// Logged out on the phone: the keys are deleted and the account unpaired,
// saying so. The channel stays held, to be paired again here.
func TestLoggedOutOnThePhoneUnpairsAndDeletesTheKeys(t *testing.T) {
	f, ch, cli := connected(t)
	cli.emit(&events.LoggedOut{})
	eventually(t, "unpaired", func() bool { return len(f.store.snapshot().unpaired) == 1 })

	if u := f.store.snapshot().unpaired[0]; u.ChannelID != ch || !strings.Contains(*u.Error, "logged this device out") {
		t.Fatalf("unpaired %+v", u)
	}
	if len(f.devices.deleted) != 1 || f.devices.deleted[0] != linkedJID.String() {
		t.Errorf("deleted %v; want the linked device's keys", f.devices.deleted)
	}
	if len(f.a.Held()) != 1 {
		t.Error("the channel was let go; it should stay held to pair again")
	}
}

func (f *fixture) logText() string {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	return f.log.b.String()
}

// The gateway decides whether a message asks anything from the channel's own
// settings, so the connection hands them over, and the ones a person changed
// since, at the next renewal.
func TestAMessageCarriesTheChannelsSettingsAsTheyAreNow(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	ch := uuid.New()
	r := row(ch, &linkedJID)
	r.NotifyStrangers, r.AnswersIn = true, "everywhere"
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.a.lease(t.Context())
	cli := f.client(t, 0)
	cli.emit(incoming("M1", &waE2E.Message{Conversation: proto.String("one")}))
	eventually(t, "the first message", func() bool { return len(f.receiver.delivered()) == 1 })

	r.NotifyStrangers, r.AnswersIn = false, "groups"
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.a.lease(t.Context())
	cli.emit(incoming("M2", &waE2E.Message{Conversation: proto.String("two")}))
	eventually(t, "the second message", func() bool { return len(f.receiver.delivered()) == 2 })

	f.receiver.mu.Lock()
	defer f.receiver.mu.Unlock()
	first, second := f.receiver.chans[0], f.receiver.chans[1]
	if first.ID != ch || !first.NotifyStrangers || first.AnswersIn != "everywhere" {
		t.Errorf("the first message's channel = %+v; want notices on, everywhere", first)
	}
	if second.NotifyStrangers || second.AnswersIn != "groups" {
		t.Errorf("after the change, the channel = %+v; want notices off, groups", second)
	}
}

func toSelf(id types.MessageID, words string) *events.Message {
	m := incoming(id, &waE2E.Message{Conversation: proto.String(words)})
	m.Info.Chat, m.Info.Sender, m.Info.IsFromMe = linkedJID.ToNonAD(), linkedJID, true
	return m
}

// In "Message yourself" the bot's answer arrives as the owner's, like the
// owner's own: the one it sent is not taken as a question, the owner's is.
func TestMessageYourselfAnswersTheOwnerAndNotTheBotsOwnAnswer(t *testing.T) {
	f, ch, cli := connected(t)
	if err := f.a.Post(t.Context(), ch, part(t, gwa.Part{To: linkedJID.ToNonAD().String(), Text: "Here is today."})); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(cli.sent) != 1 || cli.sent[0].id == "" {
		t.Fatalf("sent %+v; want one message, named before it went", cli.sent)
	}
	cli.emit(toSelf(cli.sent[0].id, "Here is today."))
	cli.emit(toSelf("M1", "and tomorrow?"))

	eventually(t, "the owner's message delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	in := f.receiver.delivered()[0][0]
	if in.Text != "and tomorrow?" || !in.ToSelf || in.Author.Ref != linkedJID.ToNonAD().String() {
		t.Fatalf("delivered %+v; want only the owner's question, as theirs to themselves", in)
	}
}
