package slack_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

type blockBody struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
	Markdown string `json:"markdown_text"`
	Blocks   []struct {
		Type     string `json:"type"`
		BlockID  string `json:"block_id"`
		Text     string `json:"text"`
		Elements []struct {
			Type     string          `json:"type"`
			Text     json.RawMessage `json:"text"`
			ActionID string          `json:"action_id"`
			Value    string          `json:"value"`
		} `json:"elements"`
	} `json:"blocks"`
}

func pr(id string, ask bool) approval.PromptRequest {
	return approval.PromptRequest{
		ID: id, Code: "K7Q2", Tool: "github__create_issue", Summary: "open an issue", Ask: ask,
		ExpiresAt: receivedAt.Add(time.Hour),
	}
}

func approvalReply(t *testing.T, text string, reqs ...approval.PromptRequest) []gateway.OutboundRequest {
	t.Helper()
	out := gateway.Outbound{ID: "ap-out", Session: gateway.Session{Ref: thread}, Kind: "approval", Text: text}
	if reqs != nil {
		out.Prompt = &approval.Prompt{Requests: reqs}
	}
	reqs2, err := replier(t).Reply(gateway.Channel{}, out, creds(), receivedAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	return reqs2
}

func blocks(t *testing.T, r gateway.OutboundRequest) blockBody {
	t.Helper()
	var b blockBody
	if err := json.Unmarshal(r.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAnApprovalAsksWithThreeButtons(t *testing.T) {
	reqs := approvalReply(t, "May I?\n\nfooter", pr("ap-1", true))
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	b := blocks(t, reqs[0])
	if b.Markdown != "" {
		t.Errorf("markdown_text %q is sent beside blocks, which Slack refuses", b.Markdown)
	}
	if b.Channel != "C2147483705" || b.ThreadTS != "1763544601.000300" || b.Text == "" {
		t.Errorf("body %+v", b)
	}
	if len(b.Blocks) != 2 || b.Blocks[0].Type != "markdown" || b.Blocks[0].Text != "May I?\n\nfooter" || b.Blocks[1].Type != "actions" {
		t.Fatalf("blocks %+v", b.Blocks)
	}
	var got [][3]string
	for _, e := range b.Blocks[1].Elements {
		var label struct{ Text string }
		_ = json.Unmarshal(e.Text, &label)
		got = append(got, [3]string{label.Text, e.ActionID, e.Value})
		if e.Type != "button" {
			t.Errorf("element %+v", e)
		}
	}
	want := [][3]string{
		{"Approve", "openarity_approve", "ap-1"},
		{"Approve for this run", "openarity_approve_run", "ap-1"},
		{"Decline…", "openarity_decline", "ap-1"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("buttons %v, want %v", got, want)
	}
	if reqs[0].Accepted == nil || reqs[0].Header.Get("Authorization") != "Bearer "+token {
		t.Error("not sent as a message under the token, with its answer read")
	}
}

// A request that went to an approver is not the person's to answer here.
func TestARequestNotAskedHereDrawsNoButtons(t *testing.T) {
	b := blocks(t, approvalReply(t, "words", pr("ap-1", false))[0])
	if len(b.Blocks) != 0 || b.Markdown != "words" {
		t.Errorf("body %+v: want the text alone", b)
	}
	// Mixed: only the one asked gets a row, and it is named.
	b = blocks(t, approvalReply(t, "words", pr("ap-1", false), pr("ap-2", true))[0])
	rows := 0
	for _, bl := range b.Blocks {
		if bl.Type == "actions" {
			rows++
			if bl.Elements[0].Value != "ap-2" {
				t.Errorf("buttons for %q", bl.Elements[0].Value)
			}
		}
	}
	if rows != 1 {
		t.Errorf("%d rows of buttons in %+v", rows, b.Blocks)
	}
}

func TestAnApprovalWithNoPromptIsTheTextAlone(t *testing.T) {
	b := blocks(t, approvalReply(t, "words")[0])
	if len(b.Blocks) != 0 || b.Markdown != "words" {
		t.Errorf("body %+v", b)
	}
}

// Only an approval draws buttons, whatever else carries a prompt.
func TestAnAnswerDrawsNoButtons(t *testing.T) {
	out := gateway.Outbound{
		ID: "x", Session: gateway.Session{Ref: thread}, Kind: "answer", Text: "hi",
		Prompt: &approval.Prompt{Requests: []approval.PromptRequest{pr("ap-1", true)}},
	}
	reqs, err := replier(t).Reply(gateway.Channel{}, out, creds(), receivedAt)
	if err != nil || len(blocks(t, reqs[0]).Blocks) != 0 {
		t.Errorf("%v %s", err, reqs[0].Body)
	}
}

func TestSeveralRequestsEachGetARowNamedForIt(t *testing.T) {
	b := blocks(t, approvalReply(t, "words", pr("ap-1", true), pr("ap-2", true))[0])
	if len(b.Blocks) != 5 {
		t.Fatalf("%d blocks: %+v", len(b.Blocks), b.Blocks)
	}
	if b.Blocks[1].Type != "context" || !strings.Contains(string(b.Blocks[1].Elements[0].Text), "create_issue on github") ||
		b.Blocks[2].BlockID != "approval:ap-1" || b.Blocks[4].BlockID != "approval:ap-2" {
		t.Errorf("blocks %+v", b.Blocks)
	}
}

func TestNoRowsPastWhatAMessageHolds(t *testing.T) {
	var many []approval.PromptRequest
	for i := range 40 {
		many = append(many, pr(fmt.Sprintf("ap-%d", i), true))
	}
	if n := len(blocks(t, approvalReply(t, "words", many...)[0]).Blocks); n > 50 {
		t.Errorf("%d blocks, Slack takes 50", n)
	}
}

func TestAnIDTooLongForAValueDrawsNoButtons(t *testing.T) {
	b := blocks(t, approvalReply(t, "words", pr(strings.Repeat("a", 201), true), pr("", true))[0])
	if len(b.Blocks) != 0 {
		t.Errorf("blocks %+v", b.Blocks)
	}
}

// The buttons come with the last part, so the question is what they sit under.
func TestALongApprovalEndsInTheButtons(t *testing.T) {
	long := strings.Repeat("word ", 5000)
	reqs := approvalReply(t, long, pr("ap-1", true))
	if len(reqs) < 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, r := range reqs {
		b := blocks(t, r)
		if last := i == len(reqs)-1; last != (len(b.Blocks) > 0) {
			t.Errorf("part %d has blocks %v", i, len(b.Blocks) > 0)
		}
	}
}

// An agent's words cannot call a channel in through the new message.
func TestAnApprovalsWordsAreSanitized(t *testing.T) {
	b := blocks(t, approvalReply(t, "<!channel> ![x](http://e/x.png)", pr("ap-1", true))[0])
	if strings.Contains(b.Blocks[0].Text, "<!channel>") || strings.Contains(b.Text, "<!channel>") || strings.Contains(b.Blocks[0].Text, "![") {
		t.Errorf("text %q", b.Blocks[0].Text)
	}
}

// ---- notices

func noticeReply(t *testing.T, n gateway.ReplyNotice, text string) ([]gateway.OutboundRequest, error) {
	t.Helper()
	out := gateway.Outbound{ID: "n1", Session: gateway.Session{Ref: thread}, Kind: "notice", Text: text, Notice: &n}
	return replier(t).Reply(gateway.Channel{}, out, creds(), receivedAt)
}

func handleFor(t *testing.T) json.RawMessage {
	t.Helper()
	return tap(t, tapPayload("openarity_decline", "ap-1")).Answer.Handle
}

type responseBody struct {
	ResponseType    string `json:"response_type"`
	ReplaceOriginal bool   `json:"replace_original"`
	ThreadTS        string `json:"thread_ts"`
	Text            string `json:"text"`
	Blocks          []struct {
		Type, Text string
	} `json:"blocks"`
}

func TestASettledNoticeReplacesTheMessage(t *testing.T) {
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Settled: true}, "Approved by <@U2147483697>")
	if err != nil || len(reqs) != 1 {
		t.Fatalf("%v %d", err, len(reqs))
	}
	r := reqs[0]
	var b responseBody
	if err := json.Unmarshal(r.Body, &b); err != nil {
		t.Fatal(err)
	}
	if r.URL != hookURL || !b.ReplaceOriginal || b.ResponseType == "ephemeral" || b.Text == "" ||
		len(b.Blocks) != 1 || b.Blocks[0].Type != "markdown" || b.Blocks[0].Text != "Approved by <@U2147483697>" {
		t.Errorf("%s %+v", r.URL, b)
	}
	if got := r.Header.Get("Authorization"); got != "" {
		t.Errorf("the bot's token is sent to a response_url: %q", got)
	}
}

func TestAPrivateNoticeIsEphemeralInTheThread(t *testing.T) {
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Private: true}, "Only Asha can answer this.")
	if err != nil {
		t.Fatal(err)
	}
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	if reqs[0].URL != hookURL || b.ResponseType != "ephemeral" || b.ReplaceOriginal || b.ThreadTS != "1763544601.000300" {
		t.Errorf("%+v", b)
	}
	if reqs[0].Header.Get("Authorization") != "" {
		t.Error("the bot's token is sent to a response_url")
	}
}

// A word meant for the tapper alone never replaces what everybody reads.
func TestAPrivateNoticeIsNeverWrittenIntoTheMessage(t *testing.T) {
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Private: true, Settled: true}, "just for you")
	if err != nil {
		t.Fatal(err)
	}
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	if b.ResponseType != "ephemeral" || b.ReplaceOriginal {
		t.Errorf("%+v", b)
	}
}

func TestANoticeWithAHandleAndNothingElseIsSaidInTheThread(t *testing.T) {
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t)}, "noted")
	if err != nil {
		t.Fatal(err)
	}
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	if b.ResponseType != "in_channel" || b.ReplaceOriginal || b.ThreadTS != "1763544601.000300" {
		t.Errorf("%+v", b)
	}
}

// A word from the brain cannot call a channel in either.
func TestANoticeIsSanitized(t *testing.T) {
	reqs, _ := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Settled: true}, "<!channel> ok")
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	if strings.Contains(b.Text, "<!channel>") || strings.Contains(b.Blocks[0].Text, "<!channel>") {
		t.Errorf("%+v", b)
	}
}

func TestAFormNoticeOpensTheModal(t *testing.T) {
	form := pr("ap-1", true)
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Form: &form}, "")
	if err != nil || len(reqs) != 1 {
		t.Fatalf("%v %d", err, len(reqs))
	}
	r := reqs[0]
	if r.URL != "https://slack.com/api/views.open" || r.Header.Get("Authorization") != "Bearer "+token || r.Accepted == nil {
		t.Errorf("%s %v", r.URL, r.Header)
	}
	var b struct {
		TriggerID string `json:"trigger_id"`
		View      struct {
			Type            string
			CallbackID      string `json:"callback_id"`
			PrivateMetadata string `json:"private_metadata"`
			Title           struct{ Text string }
			Blocks          []struct {
				Type     string
				BlockID  string `json:"block_id"`
				Optional bool
				Label    struct{ Text string }
				Element  struct {
					Type      string
					ActionID  string `json:"action_id"`
					Multiline bool
				}
			}
		}
	}
	if err := json.Unmarshal(r.Body, &b); err != nil {
		t.Fatal(err)
	}
	v := b.View
	if b.TriggerID != "13345224609.738474920.8088930838d88f008e0" || v.Type != "modal" || v.CallbackID != "openarity_decline_reason" ||
		len(v.Title.Text) > 24 || len(v.Blocks) != 1 {
		t.Fatalf("%s", r.Body)
	}
	in := v.Blocks[0]
	if in.Type != "input" || !in.Optional || in.Label.Text != "Reason" || in.BlockID != "reason" ||
		in.Element.Type != "plain_text_input" || !in.Element.Multiline || in.Element.ActionID != "reason" {
		t.Errorf("input %+v", in)
	}
	// What it carries is what the submission is read back from.
	var m map[string]string
	if err := json.Unmarshal([]byte(v.PrivateMetadata), &m); err != nil || m["ref"] != thread || m["kind"] != "thread" ||
		m["approval"] != "ap-1" || m["url"] != hookURL {
		t.Errorf("private_metadata %q", v.PrivateMetadata)
	}
	// ... and it round-trips: the submission of this very modal is a decline.
	sub := modalPayload(v.PrivateMetadata, "openarity_decline_reason", "too risky")
	a := tap(t, sub).Answer
	if a.Decision != gateway.Decline || a.ApprovalID != "ap-1" || a.Reason != "too risky" {
		t.Errorf("answer %+v", a)
	}
}

// A notice with no handle is a typed reply's: words in the thread.
func TestANoticeWithNoHandleIsPostedInTheThread(t *testing.T) {
	reqs, err := noticeReply(t, gateway.ReplyNotice{Private: true, Settled: true}, "Approved.")
	if err != nil || len(reqs) != 1 {
		t.Fatalf("%v %d", err, len(reqs))
	}
	b := blocks(t, reqs[0])
	if reqs[0].URL != "https://slack.com/api/chat.postMessage" || b.Channel != "C2147483705" ||
		b.ThreadTS != "1763544601.000300" || b.Markdown != "Approved." {
		t.Errorf("%s %+v", reqs[0].URL, b)
	}
	out := gateway.Outbound{ID: "n", Session: gateway.Session{Ref: thread}, Kind: "notice", Text: "Approved."}
	if _, err := replier(t).Reply(gateway.Channel{}, out, creds(), receivedAt); err != nil {
		t.Errorf("no Notice at all: %v", err)
	}
}

func TestANoticeWithAHandleThatCannotBeUsedIsRefused(t *testing.T) {
	form := pr("ap-1", true)
	noID := pr("", true)
	good := string(handleFor(t))
	swap := func(old, new string) json.RawMessage { return json.RawMessage(strings.Replace(good, old, new, 1)) }
	for name, tc := range map[string]gateway.ReplyNotice{
		"not json":              {Handle: json.RawMessage(`nope`), Settled: true},
		"another host":          {Handle: swap("hooks.slack.com", "evil.example"), Settled: true},
		"http":                  {Handle: swap("https://", "http://"), Private: true},
		"no url":                {Handle: swap(hookURL, ""), Settled: true},
		"a ref of no channel":   {Handle: swap("C2147483705", "x/y"), Settled: true},
		"a form, no trigger":    {Handle: swap("13345224609.738474920.8088930838d88f008e0", ""), Form: &form},
		"a form, no approval":   {Handle: json.RawMessage(good), Form: &noID},
		"userinfo before slack": {Handle: swap("https://hooks", "https://u:p@hooks"), Settled: true},
		"a form, bad url":       {Handle: swap("hooks.slack.com", "evil.example"), Form: &form},
	} {
		_, err := noticeReply(t, tc, "words")
		if !errors.Is(err, gateway.ErrUnsendable) {
			t.Errorf("%s: %v, want an unsendable reply", name, err)
		}
	}
	if _, err := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Settled: true}, "  "); err == nil {
		t.Error("a notice with no words was sent")
	}
}

// Slack says no with a 200 and "ok": false, which is a failed send; a
// response_url says "ok" in plain text, which is not.
func TestTheAnswersToNoticesAreRead(t *testing.T) {
	form := pr("ap-1", true)
	open, _ := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Form: &form}, "")
	if err := open[0].Accepted(200, []byte(`{"ok":true}`)); err != nil {
		t.Errorf("views.open ok: %v", err)
	}
	if err := open[0].Accepted(200, []byte(`{"ok":false,"error":"invalid_arguments"}`)); !errors.Is(err, gateway.ErrUnsendable) {
		t.Errorf("views.open refused: %v", err)
	}
	for _, code := range []string{"expired_trigger_id", "invalid_trigger_id", "trigger_exchanged"} {
		if err := open[0].Accepted(200, []byte(`{"ok":false,"error":"`+code+`"}`)); !errors.Is(err, gateway.ErrUnsendable) {
			t.Errorf("%s: %v, a retry cannot fix it", code, err)
		}
	}
	settled, _ := noticeReply(t, gateway.ReplyNotice{Handle: handleFor(t), Settled: true}, "done")
	if err := settled[0].Accepted(200, []byte("ok")); err != nil {
		t.Errorf("response_url ok: %v", err)
	}
	if err := settled[0].Accepted(200, []byte(`{"ok":false,"error":"used_url"}`)); err == nil {
		t.Error("a refusal from a response_url was taken as sent")
	}
}

// The agent's words stay, with the outcome under them and no buttons.
func TestASettledNoticeKeepsTheAgentsWords(t *testing.T) {
	payload := strings.Replace(tapPayload("openarity_approve", "ap-1"), `"message":{`,
		`"message":{"text":"fallback","blocks":[{"type":"markdown","text":"May I open an issue?\n\nfooter"},{"type":"actions"}],`, 1)
	h := tap(t, payload).Answer.Handle
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: h, Settled: true}, "Approved.")
	if err != nil {
		t.Fatal(err)
	}
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	want := "May I open an issue?\n\nfooter\n\nApproved."
	if !b.ReplaceOriginal || len(b.Blocks) != 1 || b.Blocks[0].Text != want || b.Text != want {
		t.Errorf("%+v", b)
	}
	// With no markdown block, the text of the message itself is kept.
	payload = strings.Replace(tapPayload("openarity_approve", "ap-1"), `"message":{`, `"message":{"text":"plain words",`, 1)
	reqs, _ = noticeReply(t, gateway.ReplyNotice{Handle: tap(t, payload).Answer.Handle, Settled: true}, "Approved.")
	_ = json.Unmarshal(reqs[0].Body, &b)
	if b.Text != "plain words\n\nApproved." {
		t.Errorf("text %q", b.Text)
	}
	// A private word never carries the agent's words along.
	reqs, _ = noticeReply(t, gateway.ReplyNotice{Handle: h, Private: true, Settled: true}, "Only Asha.")
	_ = json.Unmarshal(reqs[0].Body, &b)
	if b.Text != "Only Asha." {
		t.Errorf("private text %q", b.Text)
	}
}

func TestTheKeptWordsAreClipped(t *testing.T) {
	long := strings.Repeat("w", 5000)
	payload := strings.Replace(tapPayload("openarity_approve", "ap-1"), `"message":{`, `"message":{"text":"`+long+`",`, 1)
	if h := tap(t, payload).Answer.Handle; len(h) > 4000 {
		t.Errorf("handle is %d bytes", len(h))
	}
}

// A modal submission has no message: the outcome stands alone.
func TestASettledNoticeAfterAModalIsTheOutcomeAlone(t *testing.T) {
	h := tap(t, modalPayload(goodMeta, "openarity_decline_reason", "no")).Answer.Handle
	reqs, err := noticeReply(t, gateway.ReplyNotice{Handle: h, Settled: true}, "Declined.")
	if err != nil {
		t.Fatal(err)
	}
	var b responseBody
	_ = json.Unmarshal(reqs[0].Body, &b)
	if b.Text != "Declined." || !b.ReplaceOriginal {
		t.Errorf("%+v", b)
	}
}
