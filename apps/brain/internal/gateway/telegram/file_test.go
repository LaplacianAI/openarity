package telegram_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
)

func fileOut(ref string, f *gateway.OutFile) gateway.Outbound {
	return gateway.Outbound{
		ID: "run-1:file:7", Session: gateway.Session{Ref: ref, Kind: gateway.SessionDirect},
		Kind: "file", Text: f.Name, File: f,
	}
}

func fileReply(t *testing.T, out gateway.Outbound) []gateway.OutboundRequest {
	t.Helper()
	reqs, err := telegram.New().(gateway.Replier).Reply(gateway.Channel{}, out, creds(), time.Time{}) //nolint:errcheck // a Replier
	if err != nil {
		t.Fatal(err)
	}
	return reqs
}

// outFile is a file of data, as the sender hands one over: its size, never
// its bytes.
func outFile(name, mediaType, caption string, data []byte) *gateway.OutFile {
	return &gateway.OutFile{Name: name, MediaType: mediaType, Caption: caption, Size: int64(len(data))}
}

// form reads a sendDocument body back as Telegram does, with data streamed
// where the request says: its fields, and the document's part.
func form(t *testing.T, req gateway.OutboundRequest, file []byte) (map[string]string, *multipart.Part, []byte) {
	t.Helper()
	media, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		t.Fatalf("content type %q: %v", req.Header.Get("Content-Type"), err)
	}
	if req.Stream == nil || req.Stream.Size != int64(len(file)) {
		t.Fatalf("the request streams %+v, want the file's %d bytes", req.Stream, len(file))
	}
	whole := append(append(append([]byte{}, req.Body...), file...), req.Stream.Tail...)
	if req.Length() != int64(len(whole)) {
		t.Fatalf("Length = %d, the body is %d", req.Length(), len(whole))
	}
	r := multipart.NewReader(bytes.NewReader(whole), params["boundary"])
	fields := map[string]string{}
	var doc *multipart.Part
	var data []byte
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		if p.FormName() == "document" {
			doc, data = p, b
			continue
		}
		fields[p.FormName()] = string(b)
	}
	if doc == nil {
		t.Fatal("no document part")
	}
	return fields, doc, data
}

// A file is one sendDocument to the chat, with its bytes, its name, its type
// and its caption: a document, never a photo, so its bytes arrive as kept.
func TestAFileIsADocumentInTheChat(t *testing.T) {
	t.Parallel()

	png := []byte("\x89PNG\r\n\x1a\n...")
	reqs := fileReply(t, fileOut("-1001234567890:topic:3", outFile("chart.png", "image/png", "Sign-ups by week", png)))
	if len(reqs) != 1 || reqs[0].URL != "https://api.telegram.org/bot"+token+"/sendDocument" {
		t.Fatalf("requests %+v", reqs)
	}
	fields, doc, data := form(t, reqs[0], png)
	if fields["chat_id"] != "-1001234567890" || fields["message_thread_id"] != "3" || fields["caption"] != "Sign-ups by week" {
		t.Errorf("fields %v", fields)
	}
	if doc.FileName() != "chart.png" || doc.Header.Get("Content-Type") != "image/png" || !bytes.Equal(data, png) {
		t.Errorf("document %q %q, %d bytes", doc.FileName(), doc.Header.Get("Content-Type"), len(data))
	}
}

// No caption and the General topic send neither field; the same reply is
// the same bytes on every attempt.
func TestAFileWithNoCaptionIsTheSameEveryTime(t *testing.T) {
	t.Parallel()

	csv := []byte("a,b\n")
	out := fileOut("-1001234567890:topic:1", outFile("a.csv", "text/csv", "", csv))
	a, b := fileReply(t, out), fileReply(t, out)
	if !bytes.Equal(a[0].Body, b[0].Body) || !bytes.Equal(a[0].Stream.Tail, b[0].Stream.Tail) ||
		a[0].Header.Get("Content-Type") != b[0].Header.Get("Content-Type") {
		t.Error("the same file reply built two bodies")
	}
	fields, _, _ := form(t, a[0], csv)
	if _, ok := fields["caption"]; ok {
		t.Errorf("fields %v carry an empty caption", fields)
	}
	if _, ok := fields["message_thread_id"]; ok {
		t.Errorf("fields %v name the General topic, which Telegram refuses", fields)
	}
}

// A name with a quote, a backslash or a line break still names one file and
// cannot start a header of its own.
func TestAFilesNameCannotBreakItsHeader(t *testing.T) {
	t.Parallel()

	reqs := fileReply(t, fileOut("4242", outFile("q\"3\\r\r\nX-Evil: 1.pdf", "application/pdf", "", []byte("%PDF"))))
	_, doc, _ := form(t, reqs[0], []byte("%PDF"))
	if doc.FileName() != `q"3\rX-Evil: 1.pdf` || doc.Header.Get("X-Evil") != "" {
		t.Errorf("name %q, header %v", doc.FileName(), doc.Header)
	}
}

func TestAFileReplyWithNoFileIsRefused(t *testing.T) {
	t.Parallel()

	for name, f := range map[string]*gateway.OutFile{
		"no file": nil,
		"no size": {Name: "empty.txt", MediaType: "text/plain"},
	} {
		out := fileOut("4242", &gateway.OutFile{})
		out.File = f
		if _, err := telegram.New().(gateway.Replier).Reply(gateway.Channel{}, out, creds(), time.Time{}); err == nil { //nolint:errcheck // a Replier
			t.Errorf("%s built a request", name)
		}
	}
}

func TestTelegramSendsFiles(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(telegram.New())
	if err != nil {
		t.Fatal(err)
	}
	if !reg.SendsFiles("telegram") {
		t.Error("telegram does not say it sends files")
	}
}

// The boundary is the token's and the reply's: the same reply parts the
// same way on every attempt, and nobody without the token, as whoever wrote
// the file, can know it to end the document's part and add a field.
func TestADocumentsBoundaryIsTheTokensAndTheReplys(t *testing.T) {
	t.Parallel()

	boundary := func(tok, id string) string {
		out := fileOut("4242", outFile("a.txt", "text/plain", "", []byte("one")))
		out.ID = id
		c := creds()
		c[telegram.KeyBotToken] = tok
		reqs, err := telegram.New().(gateway.Replier).Reply(gateway.Channel{}, out, c, time.Time{}) //nolint:errcheck // a Replier
		if err != nil {
			t.Fatal(err)
		}
		_, params, err := mime.ParseMediaType(reqs[0].Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		return params["boundary"]
	}
	same := boundary(token, "run-1:file:7")
	if again := boundary(token, "run-1:file:7"); again != same {
		t.Errorf("one reply parted %q, then %q", same, again)
	}
	if other := boundary(token+"x", "run-1:file:7"); other == same {
		t.Errorf("two tokens share the boundary %q", same)
	}
	if other := boundary(token, "run-1:file:8"); other == same {
		t.Errorf("two replies share the boundary %q", same)
	}
}

// Telegram takes a bot's files up to 50 MB.
func TestTelegramTakesFilesUpTo50MB(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(telegram.New())
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.MaxFile("telegram"); got != 50_000_000 {
		t.Errorf("MaxFile = %d", got)
	}
}
