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

// form reads a sendDocument body back as Telegram does: its fields, and the
// document's part.
func form(t *testing.T, req gateway.OutboundRequest) (map[string]string, *multipart.Part, []byte) {
	t.Helper()
	media, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		t.Fatalf("content type %q: %v", req.Header.Get("Content-Type"), err)
	}
	r := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"])
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
	reqs := fileReply(t, fileOut("-1001234567890:topic:3", &gateway.OutFile{
		Name: "chart.png", MediaType: "image/png", Caption: "Sign-ups by week", Data: png,
	}))
	if len(reqs) != 1 || reqs[0].URL != "https://api.telegram.org/bot"+token+"/sendDocument" {
		t.Fatalf("requests %+v", reqs)
	}
	fields, doc, data := form(t, reqs[0])
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

	out := fileOut("-1001234567890:topic:1", &gateway.OutFile{Name: "a.csv", MediaType: "text/csv", Data: []byte("a,b\n")})
	a, b := fileReply(t, out), fileReply(t, out)
	if !bytes.Equal(a[0].Body, b[0].Body) || a[0].Header.Get("Content-Type") != b[0].Header.Get("Content-Type") {
		t.Error("the same file reply built two bodies")
	}
	fields, _, _ := form(t, a[0])
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

	reqs := fileReply(t, fileOut("4242", &gateway.OutFile{
		Name: "q\"3\\r\r\nX-Evil: 1.pdf", MediaType: "application/pdf", Data: []byte("%PDF"),
	}))
	_, doc, _ := form(t, reqs[0])
	if doc.FileName() != `q"3\rX-Evil: 1.pdf` || doc.Header.Get("X-Evil") != "" {
		t.Errorf("name %q, header %v", doc.FileName(), doc.Header)
	}
}

func TestAFileReplyWithNoFileIsRefused(t *testing.T) {
	t.Parallel()

	for name, f := range map[string]*gateway.OutFile{
		"no file":  nil,
		"no bytes": {Name: "empty.txt", MediaType: "text/plain"},
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

// The boundary is the reply's and its bytes': two files under one reply id
// part differently, so a file cannot be written to hold its own boundary.
func TestADocumentsBoundaryIsItsBytes(t *testing.T) {
	t.Parallel()

	boundary := func(data string) string {
		reqs := fileReply(t, fileOut("4242", &gateway.OutFile{Name: "a.txt", MediaType: "text/plain", Data: []byte(data)}))
		_, params, err := mime.ParseMediaType(reqs[0].Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		return params["boundary"]
	}
	if a, b := boundary("one"), boundary("two"); a == b {
		t.Errorf("two files share the boundary %q", a)
	}
}
