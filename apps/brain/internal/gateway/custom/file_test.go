package custom_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
)

// A file is a reply of kind "file" with the file inline, signed like every
// reply, so the receiver checks it as it checks an answer.
func TestAFileIsASignedReplyCarryingIt(t *testing.T) {
	t.Parallel()

	out := outbound()
	out.Kind, out.Text, out.InReplyTo = "file", "report.pdf", ""
	out.File = &gateway.OutFile{Name: "report.pdf", MediaType: "application/pdf", Caption: "Q3", Data: []byte("%PDF-1.7\n")}
	req := only(t, mustReply(t, out))

	var got struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
		File struct {
			Filename  string `json:"filename"`
			MediaType string `json:"media_type"`
			Size      int    `json:"size"`
			Caption   string `json:"caption"`
			Data      string `json:"data"`
		} `json:"file"`
	}
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(got.File.Data)
	if err != nil || string(data) != "%PDF-1.7\n" {
		t.Fatalf("data %q: %v", got.File.Data, err)
	}
	if got.Kind != "file" || got.Text != "report.pdf" || got.File.Filename != "report.pdf" ||
		got.File.MediaType != "application/pdf" || got.File.Size != 9 || got.File.Caption != "Q3" {
		t.Errorf("body %+v", got)
	}
	ts := req.Header.Get(custom.HeaderTimestamp)
	if req.Header.Get(custom.HeaderSignature) != custom.SignReply(secret, ts, req.Body) {
		t.Error("the file reply is not signed over its body")
	}
}

// Only a file reply carries a file.
func TestAnAnswerCarriesNoFile(t *testing.T) {
	t.Parallel()

	var got map[string]json.RawMessage
	if err := json.Unmarshal(only(t, mustReply(t, outbound())).Body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["file"]; ok {
		t.Errorf("an answer carries a file: %s", got["file"])
	}
}

func TestAFileReplyWithNoFileIsRefused(t *testing.T) {
	t.Parallel()

	out := outbound()
	out.Kind = "file"
	for name, f := range map[string]*gateway.OutFile{"no file": nil, "no bytes": {Name: "e.txt", MediaType: "text/plain"}} {
		out.File = f
		if _, err := replier(t).Reply(to(), out, creds(), replyAt); err == nil {
			t.Errorf("%s built a request", name)
		}
	}
}

func TestCustomSendsFiles(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(custom.New())
	if err != nil {
		t.Fatal(err)
	}
	if !reg.SendsFiles("custom") {
		t.Error("custom does not say it sends files")
	}
}

func mustReply(t *testing.T, out gateway.Outbound) []gateway.OutboundRequest {
	t.Helper()
	reqs, err := replier(t).Reply(to(), out, creds(), replyAt)
	if err != nil {
		t.Fatal(err)
	}
	return reqs
}

// A custom channel takes files up to 25 MiB, whole: its reply is signed over
// the file's bytes.
func TestACustomChannelTakesWholeFilesUpTo25MiB(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(custom.New())
	if err != nil {
		t.Fatal(err)
	}
	if reg.MaxFile("custom") != 25<<20 || !reg.WholeFiles("custom") {
		t.Errorf("MaxFile = %d, whole %v", reg.MaxFile("custom"), reg.WholeFiles("custom"))
	}
}
