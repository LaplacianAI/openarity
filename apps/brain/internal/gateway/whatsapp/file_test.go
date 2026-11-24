package whatsapp_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
)

func fileOut(f *gateway.OutFile) gateway.Outbound {
	return gateway.Outbound{
		ID: "run-1:file:7", Session: gateway.Session{Ref: "447700900123@s.whatsapp.net", Kind: gateway.SessionDirect},
		InReplyTo: "Q1", Kind: "file", Text: "report.pdf", File: f,
	}
}

// A file is one part for the socket, naming the file and quoting nothing,
// and reads back as it was built.
func TestAFileIsOnePartTheSocketReadsBack(t *testing.T) {
	t.Parallel()

	f := &gateway.OutFile{Name: "report.pdf", MediaType: "application/pdf", Caption: "Q3", Data: []byte("%PDF-1.7\n")}
	reqs, err := whatsapp.New().(gateway.Replier).Reply(gateway.Channel{}, fileOut(f), nil, time.Time{}) //nolint:errcheck // a Replier
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Reply = %d requests, %v", len(reqs), err)
	}
	p, err := whatsapp.ReadPart(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "run-1:file:7:0" || p.To != "447700900123@s.whatsapp.net" || p.Text != "" || p.Quote != "" || p.Poll != nil {
		t.Errorf("part %+v", p)
	}
	if p.File == nil || p.File.Name != "report.pdf" || p.File.MediaType != "application/pdf" ||
		p.File.Caption != "Q3" || !bytes.Equal(p.File.Data, f.Data) {
		t.Errorf("file %+v", p.File)
	}
}

// A part is one thing: text, a poll or a file, and a file has bytes.
func TestAPartIsExactlyOneOfTextAPollAndAFile(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"text and a file": `{"id":"a","to":"b","text":"hi","file":{"name":"x","media_type":"text/plain","data":"eA=="}}`,
		"a poll and a file": `{"id":"a","to":"b","poll":{"question":"q","options":["y","n"]},` +
			`"file":{"name":"x","media_type":"text/plain","data":"eA=="}}`,
		"an empty file": `{"id":"a","to":"b","file":{"name":"x","media_type":"text/plain"}}`,
	} {
		if _, err := whatsapp.ReadPart(gateway.OutboundRequest{URL: whatsapp.SendURL, Body: []byte(body)}); err == nil {
			t.Errorf("%s was read as a part", name)
		}
	}
}

func TestAFileReplyWithNoFileIsRefused(t *testing.T) {
	t.Parallel()

	for _, f := range []*gateway.OutFile{nil, {Name: "e.txt", MediaType: "text/plain"}} {
		if _, err := whatsapp.New().(gateway.Replier).Reply(gateway.Channel{}, fileOut(f), nil, time.Time{}); err == nil { //nolint:errcheck // a Replier
			t.Errorf("file %+v built a request", f)
		}
	}
}

func TestWhatsAppSendsFiles(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(whatsapp.New())
	if err != nil {
		t.Fatal(err)
	}
	if !reg.SendsFiles(whatsapp.Name) {
		t.Error("whatsapp does not say it sends files")
	}
}
