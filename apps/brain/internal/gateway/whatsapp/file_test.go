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

	f := &gateway.OutFile{Name: "report.pdf", MediaType: "application/pdf", Caption: "Q3", Size: 9}
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
		p.File.Caption != "Q3" || p.File.Size != 9 {
		t.Errorf("file %+v", p.File)
	}
	// The part names the file; its bytes are the request's stream.
	if s := reqs[0].Stream; s == nil || s.Size != 9 || len(s.Tail) != 0 || bytes.Contains(reqs[0].Body, []byte("PDF")) {
		t.Errorf("stream %+v, body %s", s, reqs[0].Body)
	}
}

// A part is one thing: text, a poll or a file, and a file has a size and
// is the request's stream, of that size.
func TestAPartIsExactlyOneOfTextAPollAndAFile(t *testing.T) {
	t.Parallel()

	file := &gateway.Stream{Size: 1}
	for name, req := range map[string]gateway.OutboundRequest{
		"text and a file": {Stream: file, Body: []byte(`{"id":"a","to":"b","text":"hi","file":{"name":"x","media_type":"text/plain","size":1}}`)},
		"a poll and a file": {Stream: file, Body: []byte(`{"id":"a","to":"b","poll":{"question":"q","options":["y","n"]},` +
			`"file":{"name":"x","media_type":"text/plain","size":1}}`)},
		"an empty file":       {Stream: file, Body: []byte(`{"id":"a","to":"b","file":{"name":"x","media_type":"text/plain"}}`)},
		"a file not streamed": {Body: []byte(`{"id":"a","to":"b","file":{"name":"x","media_type":"text/plain","size":1}}`)},
		"a file streamed at another size": {
			Stream: &gateway.Stream{Size: 2},
			Body:   []byte(`{"id":"a","to":"b","file":{"name":"x","media_type":"text/plain","size":1}}`),
		},
	} {
		req.URL = whatsapp.SendURL
		if _, err := whatsapp.ReadPart(req); err == nil {
			t.Errorf("%s was read as a part", name)
		}
	}
	ok := gateway.OutboundRequest{
		URL: whatsapp.SendURL, Stream: file,
		Body: []byte(`{"id":"a","to":"b","file":{"name":"x","media_type":"text/plain","size":1}}`),
	}
	if _, err := whatsapp.ReadPart(ok); err != nil {
		t.Errorf("a file part: %v", err)
	}
}

func TestAFileReplyWithNoFileIsRefused(t *testing.T) {
	t.Parallel()

	for name, f := range map[string]*gateway.OutFile{"no file": nil, "no size": {Name: "e.txt", MediaType: "text/plain"}} {
		if _, err := whatsapp.New().(gateway.Replier).Reply(gateway.Channel{}, fileOut(f), nil, time.Time{}); err == nil { //nolint:errcheck // a Replier
			t.Errorf("%s built a request", name)
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
