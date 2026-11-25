package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func part(t *testing.T, p gwa.Part) gateway.OutboundRequest {
	t.Helper()
	reqs, err := gwa.New().(gateway.Replier).Reply(gateway.Channel{}, gateway.Outbound{ //nolint:errcheck // it is a Replier
		ID: "run-1", Session: gateway.Session{Ref: p.To, Kind: gateway.SessionDirect}, InReplyTo: p.Quote, Kind: "answer", Text: p.Text,
	}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return reqs[0]
}

// filePart is a file reply's part, built by the adapter as the outbox does.
func filePart(t *testing.T, to string, f *gateway.OutFile) gateway.OutboundRequest {
	t.Helper()
	reqs, err := gwa.New().(gateway.Replier).Reply(gateway.Channel{}, gateway.Outbound{ //nolint:errcheck // it is a Replier
		ID: "run-1", Session: gateway.Session{Ref: to, Kind: gateway.SessionDirect}, Kind: "file", Text: f.Name, File: f,
	}, nil, time.Time{})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Reply = %d requests, %v", len(reqs), err)
	}
	return reqs[0]
}

func TestAPartIsTypedThenSentAndRecorded(t *testing.T) {
	f, ch, cli := connected(t)
	if err := f.a.Post(t.Context(), ch, part(t, gwa.Part{To: priya.String(), Text: "It waited on the lock."})); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(cli.sent) != 1 || cli.sent[0].to.String() != priya.String() || cli.sent[0].msg.GetExtendedTextMessage().GetText() != "It waited on the lock." {
		t.Fatalf("sent %+v", cli.sent)
	}
	if len(cli.presences) != 2 || cli.presences[0] != "composing" || cli.presences[1] != "paused" {
		t.Errorf("presences %v; want typing, then stopped", cli.presences)
	}
	if want := typing("It waited on the lock.", 0); !containsDuration(f.slept, want) {
		t.Errorf("waited %v; want the typing time %v among them", f.slept, want)
	}
	if _, ok := f.store.sent[ch.String()+"/run-1:0"]; !ok {
		t.Error("the part was not recorded as sent")
	}
}

// A retry after a lost answer finds the part sent and sends nothing.
func TestAPartSentBeforeIsNotSentAgain(t *testing.T) {
	f, ch, cli := connected(t)
	req := part(t, gwa.Part{To: priya.String(), Text: "once"})
	for range 2 {
		if err := f.a.Post(t.Context(), ch, req); err != nil {
			t.Fatalf("Post: %v", err)
		}
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent %d times; want once", len(cli.sent))
	}
}

func TestTwoPartsToOneChatAreSpacedApart(t *testing.T) {
	f, ch, _ := connected(t)
	for _, text := range []string{"one", "two"} {
		req := part(t, gwa.Part{To: priya.String(), Text: text})
		req.Body = []byte(strings.Replace(string(req.Body), "run-1:0", "run-1:"+text, 1))
		if err := f.a.Post(t.Context(), ch, req); err != nil {
			t.Fatal(err)
		}
	}
	// The fake clock moves only by what was waited, so the second send
	// waits whatever the first one's typing left of the gap.
	var total time.Duration
	for _, d := range f.slept {
		total += d
	}
	if total < chatGap+typing("two", 0) {
		t.Fatalf("waited %v in all; want at least the %v gap and the typing", total, chatGap)
	}
}

func TestAPartThatCannotBeSentIsGivenUp(t *testing.T) {
	f, ch, _ := connected(t)
	for name, req := range map[string]gateway.OutboundRequest{
		"not a part":     {URL: "https://example.com", Body: []byte(`{}`)},
		"no such person": {URL: gwa.SendURL, Body: []byte(`{"id":"r:0","to":"@@@","text":"hi"}`)},
	} {
		if err := f.a.Post(t.Context(), ch, req); !errors.Is(err, gateway.ErrUnsendable) {
			t.Errorf("%s: Post = %v; want it unsendable", name, err)
		}
	}
}

func TestAPartThatCannotGoYetIsRetried(t *testing.T) {
	f, ch, cli := connected(t)
	req := part(t, gwa.Part{To: priya.String(), Text: "hi"})
	if err := f.a.Post(t.Context(), uuid.New(), req); err == nil || errors.Is(err, gateway.ErrUnsendable) {
		t.Errorf("a channel not held = %v; want a retry", err)
	}
	cli.Disconnect()
	if err := f.a.Post(t.Context(), ch, req); err == nil || errors.Is(err, gateway.ErrUnsendable) {
		t.Errorf("not connected = %v; want a retry", err)
	}
	if len(cli.sent) != 0 {
		t.Error("something was sent")
	}
}

// An answer quotes the message it answers when the connection still has it,
// and quotes nothing rather than an empty box when it does not.
func TestAnAnswerQuotesTheMessageItRemembers(t *testing.T) {
	f, ch, cli := connected(t)
	cli.emit(incoming("Q1", &waE2E.Message{Conversation: proto.String("why did it fail?")}))
	eventually(t, "the message taken", func() bool { return len(f.receiver.delivered()) == 1 })

	if err := f.a.Post(t.Context(), ch, part(t, gwa.Part{To: priya.String(), Text: "the lock", Quote: "Q1"})); err != nil {
		t.Fatal(err)
	}
	ci := cli.sent[0].msg.GetExtendedTextMessage().GetContextInfo()
	if ci.GetStanzaID() != "Q1" || ci.GetParticipant() != priya.String() || ci.GetQuotedMessage().GetConversation() != "why did it fail?" {
		t.Fatalf("quote %+v; want Q1, its sender and its words", ci)
	}

	req := part(t, gwa.Part{To: priya.String(), Text: "and", Quote: "FORGOTTEN"})
	req.Body = []byte(strings.Replace(string(req.Body), "run-1:0", "run-1:9", 1))
	if err := f.a.Post(t.Context(), ch, req); err != nil {
		t.Fatal(err)
	}
	if cli.sent[1].msg.GetExtendedTextMessage().GetContextInfo() != nil {
		t.Error("a message the connection does not remember was quoted")
	}
}

func containsDuration(ds []time.Duration, want time.Duration) bool {
	for _, d := range ds {
		if d == want {
			return true
		}
	}
	return false
}

var _ = db.LeaseWhatsAppAccountsRow{}

func TestAPollPartIsSentAsASingleChoicePoll(t *testing.T) {
	f, ch, cli := connected(t)
	options := gateway.PollOptions("7K2F")
	body := `{"id":"run-1:1","to":"` + priya.String() + `","poll":{"question":"Allow create_issue on github? (7K2F)","options":["` + options[0] + `","` + options[1] + `"]}}`
	if err := f.a.Post(t.Context(), ch, gateway.OutboundRequest{URL: gwa.SendURL, Body: []byte(body)}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent %+v", cli.sent)
	}
	poll := cli.sent[0].msg.GetPollCreationMessage()
	if poll == nil || poll.GetName() != "Allow create_issue on github? (7K2F)" || poll.GetSelectableOptionsCount() != 1 {
		t.Fatalf("sent %+v; want a single-choice poll", cli.sent[0].msg)
	}
	var got []string
	for _, o := range poll.GetOptions() {
		got = append(got, o.GetOptionName())
	}
	if len(got) != 2 || got[0] != options[0] || got[1] != options[1] {
		t.Errorf("options %q; want %q verbatim, as the vote is matched by their hash", got, options)
	}
	if len(cli.sent[0].msg.GetMessageContextInfo().GetMessageSecret()) != 32 {
		t.Error("the poll carries no message secret, so a vote on it could not be decrypted")
	}
	if _, ok := f.store.sent[ch.String()+"/run-1:1"]; !ok {
		t.Error("the poll was not recorded as sent")
	}
}

// A file is uploaded, then sent as a document naming the upload, with the
// run's name for it, its type and its caption.
func TestAFilePartIsUploadedAndSentAsADocument(t *testing.T) {
	f, ch, cli := connected(t)
	req := filePart(t, priya.String(), &gateway.OutFile{
		Name: "report.pdf", MediaType: "application/pdf", Caption: "The Q3 numbers", Data: []byte("%PDF-1.7\n"),
	})
	if err := f.a.Post(t.Context(), ch, req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(cli.uploads) != 1 || string(cli.uploads[0]) != "%PDF-1.7\n" || len(cli.sent) != 1 {
		t.Fatalf("uploaded %q, sent %+v", cli.uploads, cli.sent)
	}
	doc := cli.sent[0].msg.GetDocumentMessage()
	if doc == nil || doc.GetFileName() != "report.pdf" || doc.GetMimetype() != "application/pdf" ||
		doc.GetCaption() != "The Q3 numbers" || doc.GetDirectPath() != "/v/t62/upload" || doc.GetFileLength() != 9 {
		t.Fatalf("sent %+v; want a document naming the upload", cli.sent[0].msg)
	}
	if _, ok := f.store.sent[ch.String()+"/run-1:0"]; !ok {
		t.Error("the document was not recorded as sent")
	}
}

// An upload that fails sends nothing, and is the outbox's to retry.
func TestAFileWhoseUploadFailsSendsNothing(t *testing.T) {
	f, ch, cli := connected(t)
	cli.uploadErr = errors.New("media server unreachable")
	req := filePart(t, priya.String(), &gateway.OutFile{Name: "a.pdf", MediaType: "application/pdf", Data: []byte("x")})
	err := f.a.Post(t.Context(), ch, req)
	if err == nil || errors.Is(err, gateway.ErrUnsendable) {
		t.Fatalf("err = %v; want one the outbox retries", err)
	}
	if len(cli.sent) != 0 {
		t.Errorf("sent %+v after a failed upload", cli.sent)
	}
	if _, ok := f.store.sent[ch.String()+"/run-1:0"]; ok {
		t.Error("a part never sent was recorded as sent")
	}
}

// The upload runs under a deadline by its size, as an HTTP post does: one
// that hangs would hold up every channel this process sends for.
func TestAFileUploadHasADeadline(t *testing.T) {
	f, ch, cli := connected(t)
	req := filePart(t, priya.String(), &gateway.OutFile{Name: "a.pdf", MediaType: "application/pdf", Data: []byte("x")})
	before := time.Now()
	if err := f.a.Post(t.Context(), ch, req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if cli.uploadDeadline.IsZero() {
		t.Fatal("the upload had no deadline")
	}
	if left := cli.uploadDeadline.Sub(before); left > gateway.PostTimeout(len(req.Body))+time.Second {
		t.Errorf("deadline %v away, want at most %v", left, gateway.PostTimeout(len(req.Body)))
	}
}
