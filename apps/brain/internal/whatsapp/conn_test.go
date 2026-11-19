package whatsapp

import (
	"bytes"
	"crypto/sha256"
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
	if l.ChannelID != ch || l.Holder != "test/1" || *l.Jid != "919000000005:3@s.whatsapp.net" || *l.Phone != "919000000005" {
		t.Fatalf("linked %+v; want the device's own address and its number, by this holder", l)
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

// The word comes with the lease: the owner's message holding it is taken,
// carried to the gateway with the channel's word, and one without it is not.
func TestTheOwnersCallByTheWordComesWithTheLease(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	r := row(uuid.New(), &linkedJID)
	r.AnswersIn, r.TriggerWord = "everywhere", new("@Fduv")
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.a.lease(t.Context())
	cli := f.client(t, 0)

	own := func(id types.MessageID, words string) *events.Message {
		m := incoming(id, &waE2E.Message{Conversation: proto.String(words)})
		m.Info.Sender, m.Info.IsFromMe = linkedJID, true
		return m
	}
	cli.emit(own("M1", "see you at six"))
	cli.emit(own("M2", "@Fduv when is the next train?"))

	eventually(t, "the call delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	f.receiver.mu.Lock()
	defer f.receiver.mu.Unlock()
	in, ch := f.receiver.got[0][0], f.receiver.chans[0]
	if in.ExternalID != "M2" || in.Session.Ref != priya.String() {
		t.Fatalf("delivered %+v; want only the call, in the chat with Priya", in)
	}
	if ch.TriggerWord == nil || *ch.TriggerWord != "@Fduv" {
		t.Errorf("the channel carried %v; want the word", ch.TriggerWord)
	}
}

// sharedFixture is a channel on a number another channel links too, the
// other's word given.
func sharedFixture(t *testing.T, word *string, others ...string) (*fixture, *fakeClient) {
	t.Helper()
	f := newFixture(t, newDevices(linkedJID))
	r := row(uuid.New(), &linkedJID)
	r.AnswersIn, r.TriggerWord, r.SharedWords = "everywhere", word, others
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.a.lease(t.Context())
	return f, f.client(t, 0)
}

func words(id types.MessageID, text string) *events.Message {
	return incoming(id, &waE2E.Message{Conversation: proto.String(text)})
}

// On a shared number a stranger reaches this channel only by its word: the
// same message reaches the other channel, which answers by its own.
func TestOnASharedNumberAStrangerReachesTheChannelByItsWord(t *testing.T) {
	f, cli := sharedFixture(t, new("@Dhars"), "@Fduv")
	cli.emit(words("M1", "any jobs?"))
	cli.emit(words("M2", "@Fduv hi"))
	cli.emit(words("M3", "@Dhars any jobs?"))

	eventually(t, "the call delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if got := f.receiver.delivered(); len(got) != 1 || got[0][0].ExternalID != "M3" {
		t.Fatalf("delivered %+v; want only the message with this channel's word", got)
	}
}

// Until every channel on the number has a word of its own, this one answers
// nothing: one without a word would answer what is meant for another.
func TestOnASharedNumberAChannelWaitsForEveryWord(t *testing.T) {
	cases := map[string]struct {
		word   *string
		others []string
	}{
		"no word of its own":    {nil, []string{"@Fduv"}},
		"the other has no word": {new("@Dhars"), []string{""}},
		"the two words are one": {new("@Dhars"), []string{"@dhars"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, cli := sharedFixture(t, c.word, c.others...)
			cli.emit(words("M1", "@Dhars any jobs?"))
			time.Sleep(50 * time.Millisecond)
			if n := len(f.receiver.delivered()); n != 0 {
				t.Errorf("delivered %d; want nothing until the words are set", n)
			}
		})
	}
}

// The history WhatsApp sends on linking comes before the next lease: the
// channel learns its number is shared on linking, so the history is filtered
// rather than handed to this team whole.
func TestHistoryOnLinkingASharedNumberIsFilteredByTheWord(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	r := row(uuid.New(), &linkedJID)
	r.TriggerWord = new("@Dhars")
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.store.shared = []string{"@Fduv"} // what the store says once linked; the lease said nothing
	f.a.lease(t.Context())
	cli := f.client(t, 0)

	cli.emit(&events.PairSuccess{ID: linkedJID})
	cli.emit(historyOf(priya, "what we said yesterday", "@Dhars any jobs?"))

	eventually(t, "the history delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	if got := f.receiver.delivered()[0]; len(got) != 1 || !strings.Contains(got[0].Text, "@Dhars") {
		t.Fatalf("history %+v; want only the message with this channel's word", got)
	}
}

func historyOf(chat types.JID, texts ...string) *events.HistorySync {
	msgs := make([]*waHistorySync.HistorySyncMsg, len(texts))
	for i, text := range texts {
		msgs[i] = &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{ID: proto.String("H" + string(rune('A'+i)))},
			Message: &waE2E.Message{Conversation: proto.String(text)},
		}}
	}
	return &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String(chat.String()), Messages: msgs},
	}}}
}

// Another connection taking the device over ends this one: the account says
// so, rather than "linked" over a connection that is gone, and the keys are
// left for the sweep, which logs the device out.
func TestADeviceTakenOverIsUnpairedSayingSo(t *testing.T) {
	f, ch, cli := connected(t)
	cli.emit(&events.StreamReplaced{})

	eventually(t, "the account unpaired", func() bool { return len(f.store.snapshot().unpaired) == 1 })
	u := f.store.snapshot().unpaired[0]
	if u.ChannelID != ch || !strings.Contains(*u.Error, "took this channel's linked device over") {
		t.Fatalf("unpaired %+v", u)
	}
	if len(f.devices.deleted) != 0 || cli.discos == 0 {
		t.Errorf("deleted %v, disconnected %d times; want the keys kept and the client let go", f.devices.deleted, cli.discos)
	}
}

func vote(id types.MessageID) *events.Message {
	return incoming(id, &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{}})
}

func TestAPollVoteIsDecryptedAndHandedOverAsAnAnswer(t *testing.T) {
	f, _, cli := connected(t)
	want := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[0]))
	cli.votes = map[types.MessageID][][]byte{"V1": {want[:]}}
	cli.emit(vote("V1"))

	eventually(t, "the vote delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	in := f.receiver.delivered()[0][0]
	if in.ExternalID != "V1" || in.Author.Ref != priya.String() || in.Session.Ref != priya.String() {
		t.Fatalf("delivered %+v", in)
	}
	if in.Answer == nil || len(in.Answer.Hashes) != 1 || !bytes.Equal(in.Answer.Hashes[0], want[:]) {
		t.Fatalf("answer %+v; want the decrypted hash", in.Answer)
	}
}

// WhatsApp does not send a vote again, and a vote nobody can read answers
// nothing: it is logged, and the message after it still arrives.
func TestAVoteThatCannotBeDecryptedOrChoseNothingIsDropped(t *testing.T) {
	f, _, cli := connected(t)
	cli.votes = map[types.MessageID][][]byte{"EMPTY": {}}
	cli.emit(vote("UNKNOWN"))
	cli.emit(vote("EMPTY"))
	cli.emit(incoming("M1", &waE2E.Message{Conversation: proto.String("hi")}))

	eventually(t, "the message after them delivered", func() bool { return len(f.receiver.delivered()) == 1 })
	if in := f.receiver.delivered()[0][0]; in.Text != "hi" || in.Answer != nil {
		t.Fatalf("delivered %+v; the votes went first", in)
	}
	log := f.logText()
	if strings.Count(log, "not decrypted") != 1 || strings.Count(log, "chose nothing") != 1 {
		t.Errorf("log %q; want each drop said once, the unreadable vote not also taken for an empty one", log)
	}
}
