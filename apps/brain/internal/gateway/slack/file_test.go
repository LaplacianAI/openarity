package slack_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

const uploadAnswer = `{"ok":true,"upload_url":"https://files.slack.com/upload/v1/abcdefghi","file_id":"F012AB3CDE4"}`

func uploader(t *testing.T) gateway.FileUploader {
	t.Helper()
	u, ok := slack.New().(gateway.FileUploader)
	if !ok {
		t.Fatal("slack is not a FileUploader")
	}
	return u
}

func fileOut(ref, caption string) gateway.Outbound {
	return gateway.Outbound{
		ID: "run-1:file:7", Session: gateway.Session{Ref: ref}, Kind: "file", Text: "report.pdf",
		File: &gateway.OutFile{Name: "report.pdf", MediaType: "application/pdf", Caption: caption, Size: 9},
	}
}

func step(t *testing.T, out gateway.Outbound, answers ...string) (gateway.OutboundRequest, bool) {
	t.Helper()
	var as [][]byte
	for _, a := range answers {
		as = append(as, []byte(a))
	}
	req, final, err := uploader(t).FileRequest(gateway.Channel{}, out, creds(), as)
	if err != nil {
		t.Fatalf("FileRequest after %d answers: %v", len(answers), err)
	}
	return req, final
}

func uploadForm(t *testing.T, r gateway.OutboundRequest) url.Values {
	t.Helper()
	if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %q", got)
	}
	v, err := url.ParseQuery(string(r.Body))
	if err != nil {
		t.Fatalf("body %s: %v", r.Body, err)
	}
	return v
}

// The upload is three steps: where the bytes go, the bytes, then the file
// shared in the thread with its caption. Only the last is final.
func TestAFileIsUploadedInThreeSteps(t *testing.T) {
	out := fileOut("C2147483705:1763544601.000300", "The Q3 numbers")

	ask, final := step(t, out)
	v := uploadForm(t, ask)
	if ask.URL != "https://slack.com/api/files.getUploadURLExternal" || final ||
		v.Get("filename") != "report.pdf" || v.Get("length") != "9" {
		t.Fatalf("step 1: %s %s, final %v", ask.URL, ask.Body, final)
	}
	if ask.Header.Get("Authorization") != "Bearer "+token || strings.Contains(ask.URL, token) || ask.Accepted == nil {
		t.Error("step 1 carries the token in its header and reads Slack's answer")
	}

	bytesUp, final := step(t, out, uploadAnswer)
	if bytesUp.URL != "https://files.slack.com/upload/v1/abcdefghi" || final ||
		len(bytesUp.Body) != 0 || bytesUp.Stream == nil || bytesUp.Stream.Size != 9 || len(bytesUp.Stream.Tail) != 0 ||
		bytesUp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("step 2: %s %q, final %v", bytesUp.URL, bytesUp.Body, final)
	}
	if bytesUp.Header.Get("Authorization") != "" {
		t.Error("the bytes carry the token, which the upload address does not need")
	}

	share, final := step(t, out, uploadAnswer, "OK - 9")
	v = uploadForm(t, share)
	if share.URL != "https://slack.com/api/files.completeUploadExternal" || !final || share.Accepted == nil {
		t.Fatalf("step 3: %s, final %v", share.URL, final)
	}
	var files []struct{ ID, Title string }
	if err := json.Unmarshal([]byte(v.Get("files")), &files); err != nil || len(files) != 1 ||
		files[0].ID != "F012AB3CDE4" || files[0].Title != "report.pdf" {
		t.Errorf("files %q: %v", v.Get("files"), err)
	}
	if v.Get("channel_id") != "C2147483705" || v.Get("thread_ts") != "1763544601.000300" ||
		v.Get("initial_comment") != "The Q3 numbers" {
		t.Errorf("step 3 form %v", v)
	}
}

// A direct message's file goes in place, with no thread and, with no
// caption, no comment.
func TestADirectFileGoesInPlace(t *testing.T) {
	share, _ := step(t, fileOut("D024BE91L", ""), uploadAnswer, "OK - 9")
	v := uploadForm(t, share)
	if v.Get("channel_id") != "D024BE91L" || v.Has("thread_ts") || v.Has("initial_comment") {
		t.Errorf("form %v", v)
	}
}

// A caption is the run's words, which a tool's content may have chosen: it
// cannot notify a whole channel.
func TestACaptionCannotBroadcast(t *testing.T) {
	share, _ := step(t, fileOut("C1:1.2", "<!channel> look @here"), uploadAnswer, "OK - 9")
	got := uploadForm(t, share).Get("initial_comment")
	if strings.Contains(got, "<!channel>") || strings.Contains(got, "@here") {
		t.Errorf("initial_comment %q broadcasts", got)
	}
}

// The bytes go only to Slack's own upload host, over TLS, for a file it
// named: an answer that says otherwise sends nothing.
func TestAnUploadAddressThatIsNotSlacksIsRefused(t *testing.T) {
	for name, answer := range map[string]string{
		"plain http":    `{"ok":true,"upload_url":"http://files.slack.com/upload/v1/a","file_id":"F1"}`,
		"another host":  `{"ok":true,"upload_url":"https://files.slack.com.evil.example/upload","file_id":"F1"}`,
		"a port":        `{"ok":true,"upload_url":"https://files.slack.com:8443/upload","file_id":"F1"}`,
		"user info":     `{"ok":true,"upload_url":"https://x@files.slack.com/upload","file_id":"F1"}`,
		"no file":       `{"ok":true,"upload_url":"https://files.slack.com/upload/v1/a"}`,
		"a bad file id": `{"ok":true,"upload_url":"https://files.slack.com/upload/v1/a","file_id":"F1\",\"x"}`,
		"not json":      `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, n := range []int{1, 2} {
				answers := [][]byte{[]byte(answer), []byte("OK - 9")}[:n]
				if req, _, err := uploader(t).FileRequest(gateway.Channel{}, fileOut("C1", ""), creds(), answers); err == nil {
					t.Errorf("step %d went to %s", n+1, req.URL)
				}
			}
		})
	}
}

// An upload is three steps: a fourth is a mistake, never a request.
func TestAFourthStepIsRefused(t *testing.T) {
	answers := [][]byte{[]byte(uploadAnswer), []byte("OK - 9"), []byte(`{"ok":true}`)}
	if _, _, err := uploader(t).FileRequest(gateway.Channel{}, fileOut("C1", ""), creds(), answers); err == nil {
		t.Error("a fourth step was built")
	}
}

// What cannot be uploaded builds nothing: no token, no file, nowhere to go.
func TestAFileWithNothingToSendIsRefused(t *testing.T) {
	empty := fileOut("C1", "")
	empty.File.Size = 0
	nameless := fileOut(":1.2", "")
	for name, c := range map[string]struct {
		out   gateway.Outbound
		creds gateway.Credentials
	}{
		"no token":   {fileOut("C1", ""), gateway.Credentials{}},
		"no file":    {gateway.Outbound{Session: gateway.Session{Ref: "C1"}, Kind: "file"}, creds()},
		"no bytes":   {empty, creds()},
		"no channel": {nameless, creds()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := uploader(t).FileRequest(gateway.Channel{}, c.out, c.creds, nil); err == nil {
				t.Error("a request was built")
			}
		})
	}
}

// The same inputs build the same request, so a retry asks the same thing.
func TestAnUploadStepIsTheSameEveryTime(t *testing.T) {
	out := fileOut("C1:1.2", "x")
	for n := range 3 {
		answers := []string{uploadAnswer, "OK - 9"}[:n]
		a, _ := step(t, out, answers...)
		b, _ := step(t, out, answers...)
		if a.URL != b.URL || !bytes.Equal(a.Body, b.Body) {
			t.Errorf("step %d built two ways", n+1)
		}
	}
}

// Slack refuses inside a 200. A refusal no retry fixes, as a missing scope
// or uploads turned off, gives the file up; any other is tried again.
func TestAnUploadsRefusalIsReadAsFinalOrNot(t *testing.T) {
	ask, _ := step(t, fileOut("C1", ""))
	for code, final := range map[string]bool{
		"missing_scope": true, "file_uploads_disabled": true, "not_in_channel": true, "ratelimited": false,
	} {
		err := ask.Accepted(200, []byte(`{"ok":false,"error":"`+code+`"}`))
		if err == nil || errors.Is(err, gateway.ErrUnsendable) != final {
			t.Errorf("%s: err %v, want final %v", code, err, final)
		}
	}
}

// A file is not a reply Reply builds: it goes in steps, and a wiring that
// sent it through Reply fails rather than posting its name as text.
func TestReplyRefusesAFile(t *testing.T) {
	if reqs, err := replier(t).Reply(gateway.Channel{}, fileOut("C1", ""), creds(), receivedAt); err == nil {
		t.Errorf("Reply built %d requests for a file", len(reqs))
	}
}

// Slack registers as a provider that sends files, in steps, and still
// replies as before.
func TestSlackSendsFilesInSteps(t *testing.T) {
	reg, err := gateway.NewRegistry(slack.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Uploader("slack"); !ok || !reg.SendsFiles("slack") {
		t.Error("slack is not registered as sending files in steps")
	}
	if _, ok := reg.Replier("slack"); !ok {
		t.Error("slack no longer replies")
	}
}
