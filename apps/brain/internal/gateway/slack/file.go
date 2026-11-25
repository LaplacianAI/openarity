package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// RepliesWithFiles: a file a run sends is uploaded into the thread, in the
// three steps FileRequest builds. Reply builds none of them.
func (provider) RepliesWithFiles() {}

// uploadHost is where Slack takes a file's bytes. An upload_url anywhere else
// is refused, so an answer that is not Slack's cannot send the file elsewhere.
const uploadHost = "files.slack.com"

// fileID is the id Slack names an upload by, as completeUploadExternal is
// handed it back.
// Only its shape is checked, letters, digits, '-' and '_', not Slack's own
// format, which may change: it goes back inside JSON, never into a URL.
var fileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// FileRequest is Slack's upload, a step at a time: ask where the bytes go,
// send them there, then share the file in the thread with its caption. Slack
// shares a file only when its upload completes, so one abandoned part way is
// never seen, and a retry starts again from the first step.
func (provider) FileRequest(
	_ gateway.Channel, out gateway.Outbound, creds gateway.Credentials, answers [][]byte,
) (gateway.OutboundRequest, bool, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, false, errors.New("slack: no bot token")
	}
	if out.File == nil || len(out.File.Data) == 0 {
		return gateway.OutboundRequest{}, false, errors.New("slack: the reply carries no file")
	}
	channel, thread, err := target(out.Session.Ref)
	if err != nil {
		return gateway.OutboundRequest{}, false, err
	}
	switch len(answers) {
	case 0:
		return form(token, "files.getUploadURLExternal", url.Values{
			"filename": {out.File.Name},
			"length":   {strconv.Itoa(len(out.File.Data))},
		}), false, nil
	case 1:
		up, err := uploadOf(answers[0])
		if err != nil {
			return gateway.OutboundRequest{}, false, err
		}
		h := http.Header{}
		h.Set("Content-Type", "application/octet-stream")
		return gateway.OutboundRequest{URL: up.url, Header: h, Body: out.File.Data}, false, nil
	case 2:
		up, err := uploadOf(answers[0])
		if err != nil {
			return gateway.OutboundRequest{}, false, err
		}
		files, err := json.Marshal([]struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}{{up.fileID, out.File.Name}})
		if err != nil {
			return gateway.OutboundRequest{}, false, fmt.Errorf("slack: encoding the file: %w", err)
		}
		v := url.Values{"files": {string(files)}, "channel_id": {channel}}
		if thread != "" {
			v.Set("thread_ts", thread)
		}
		if out.File.Caption != "" {
			v.Set("initial_comment", Sanitize(out.File.Caption))
		}
		return form(token, "files.completeUploadExternal", v), true, nil
	default:
		return gateway.OutboundRequest{}, false, fmt.Errorf("slack: an upload is three steps, not %d", len(answers)+1)
	}
}

type slot struct{ url, fileID string }

// uploadOf reads getUploadURLExternal's answer, which accepted has already
// found ok: where the bytes go, and the file they make.
func uploadOf(answer []byte) (slot, error) {
	var a struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if err := json.Unmarshal(answer, &a); err != nil {
		return slot{}, errors.New("slack: the upload address could not be read")
	}
	u, err := url.Parse(a.UploadURL)
	if err != nil || u.Scheme != "https" || u.Host != uploadHost || u.User != nil {
		return slot{}, fmt.Errorf("slack: the upload address is not on %s", uploadHost)
	}
	if !fileID.MatchString(a.FileID) {
		return slot{}, errors.New("slack: the upload names no file")
	}
	return slot{url: u.String(), fileID: a.FileID}, nil
}

// form is a Web API call with a form body, as the upload methods take.
func form(token, method string, v url.Values) gateway.OutboundRequest {
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Authorization", "Bearer "+token)
	return gateway.OutboundRequest{URL: apiBase + method, Header: h, Body: []byte(v.Encode()), Accepted: accepted}
}
