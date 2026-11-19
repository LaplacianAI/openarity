package telegram_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
)

const (
	reqA = "0192f5c4-7d1e-7c3a-9b2e-4f6a8d0c1e23"
	reqB = "0192f5c4-7d1e-7c3a-9b2e-4f6a8d0c1e24"
)

func tapBody(data string) string {
	raw, _ := json.Marshal(data)
	return `{"update_id": 9, "callback_query": {"id": "4417709230112", "chat_instance": "x",
	  "from": {"id": 4242, "is_bot": false, "first_name": "Asha", "last_name": "Rao"},
	  "data": ` + string(raw) + `,
	  "message": {"message_id": 31, "date": 1762594200, "from": {"id": 7012345678, "is_bot": true, "first_name": "Openarity"},
	    "chat": {"id": 4242, "type": "private"}, "text": "May I?"}}}`
}

type keyboardBody struct {
	sent
	ReplyMarkup *struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

func prompt(reqs ...approval.PromptRequest) gateway.Outbound {
	out := outbound("4242", "May I run it?\n\nfooter")
	out.Kind = "approval"
	out.Prompt = &approval.Prompt{Requests: reqs}
	return out
}

func request1(id string, ask bool) approval.PromptRequest {
	return approval.PromptRequest{
		ID: id, Code: "7k2f", Tool: "github__create_issue", Summary: "s",
		ExpiresAt: receivedAt.Add(time.Hour), Ask: ask,
	}
}

func replyKeyboard(t *testing.T, out gateway.Outbound) []keyboardBody {
	t.Helper()
	reqs, _ := reply(t, out)
	bodies := make([]keyboardBody, len(reqs))
	for i, r := range reqs {
		if err := json.Unmarshal(r.Body, &bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	return bodies
}

// A request the chat asks gets Approve, For this run and Decline, each naming
// it in at most the 64 bytes Telegram allows.
func TestAnApprovalCarriesAKeyboardForEachRequestAsked(t *testing.T) {
	t.Parallel()

	got := replyKeyboard(t, prompt(request1(reqA, true)))
	if len(got) != 1 || got[0].ReplyMarkup == nil || len(got[0].ReplyMarkup.InlineKeyboard) != 1 {
		t.Fatalf("keyboard = %+v", got)
	}
	row := got[0].ReplyMarkup.InlineKeyboard[0]
	want := [][2]string{{"Approve", "a:" + reqA}, {"For this run", "r:" + reqA}, {"Decline", "d:" + reqA}}
	if len(row) != 3 {
		t.Fatalf("row = %+v", row)
	}
	for i, b := range row {
		if b.Text != want[i][0] || b.CallbackData != want[i][1] || len(b.CallbackData) > 64 {
			t.Errorf("button %d = %+v, want %v", i, b, want[i])
		}
	}
	if !strings.Contains(got[0].RichMessage.Markdown, "May I run it?") {
		t.Errorf("text = %q", got[0].RichMessage.Markdown)
	}
}

// With several requests each button names its request by its code.
func TestSeveralRequestsAreToldApartByTheirCodes(t *testing.T) {
	t.Parallel()

	b := request1(reqB, true)
	b.Code = "9m3x"
	got := replyKeyboard(t, prompt(request1(reqA, true), b))
	rows := got[0].ReplyMarkup.InlineKeyboard
	if len(rows) != 2 || rows[0][0].Text != "Approve 7k2f" || rows[1][2].Text != "Decline 9m3x" ||
		rows[1][1].CallbackData != "r:"+reqB {
		t.Errorf("rows = %+v", rows)
	}
}

// A request the chat does not ask is only said to have gone to an approver, so
// it has no buttons; with none to ask, the text goes alone.
func TestARequestNotAskedHereHasNoButtons(t *testing.T) {
	t.Parallel()

	got := replyKeyboard(t, prompt(request1(reqA, false)))
	if len(got) != 1 || got[0].ReplyMarkup != nil {
		t.Errorf("keyboard = %+v, want none", got)
	}
	got = replyKeyboard(t, prompt(request1(reqA, false), request1(reqB, true)))
	if rows := got[0].ReplyMarkup.InlineKeyboard; len(rows) != 1 || rows[0][0].CallbackData != "a:"+reqB {
		t.Errorf("rows = %+v, want only the one asked", rows)
	}
}

func TestAnApprovalWithNoPromptIsTheTextAlone(t *testing.T) {
	t.Parallel()

	out := prompt()
	out.Prompt = nil
	if got := replyKeyboard(t, out); len(got) != 1 || got[0].ReplyMarkup != nil {
		t.Errorf("got %+v", got)
	}
}

// A request whose id is not a UUID could only make data the brain would
// refuse, and a prompt with very many would draw a keyboard Telegram refuses.
func TestAKeyboardOnlyDrawsWhatATapCouldName(t *testing.T) {
	t.Parallel()

	got := replyKeyboard(t, prompt(request1("not-a-uuid", true)))
	if got[0].ReplyMarkup != nil {
		t.Errorf("keyboard = %+v, want none", got[0].ReplyMarkup)
	}

	var many []approval.PromptRequest
	for i := range 40 {
		many = append(many, request1(strings.Replace(reqA, "e23", string(rune('a'+i%6))+"0"+string(rune('a'+i/6)), 1), true))
	}
	if rows := replyKeyboard(t, prompt(many...))[0].ReplyMarkup.InlineKeyboard; len(rows) != 10 {
		t.Errorf("%d rows, want 10", len(rows))
	}
}

// The keyboard is on the last message, which a long prompt splits into several.
func TestTheKeyboardIsOnTheLastOfSeveralMessages(t *testing.T) {
	t.Parallel()

	out := prompt(request1(reqA, true))
	out.Text = strings.Repeat("a paragraph of the agent's words.\n\n", 1500)
	got := replyKeyboard(t, out)
	if len(got) < 2 {
		t.Fatalf("%d messages, want several", len(got))
	}
	for i, g := range got {
		if (g.ReplyMarkup != nil) != (i == len(got)-1) {
			t.Errorf("message %d has a keyboard: %v", i, g.ReplyMarkup != nil)
		}
	}
}

func TestATapIsAnAnswerInTheChatItWasMadeIn(t *testing.T) {
	t.Parallel()

	for data, want := range map[string]gateway.Decision{
		"a:" + reqA: gateway.Approve, "r:" + reqA: gateway.ApproveRun, "d:" + reqA: gateway.Decline,
	} {
		in := one(t, tapBody(data))
		if in.ExternalID != "callback:4417709230112" || in.Author.Ref != "4242" || in.Author.DisplayName != "Asha Rao" ||
			in.Session != (gateway.Session{Ref: "4242", Kind: gateway.SessionDirect}) || in.Text == "" {
			t.Errorf("%s: tap = %+v", data, in)
		}
		if !in.SentAt.Equal(receivedAt) {
			t.Errorf("sent at %v", in.SentAt)
		}
		a := in.Answer
		if a == nil || a.ApprovalID != reqA || a.Decision != want {
			t.Fatalf("%s: answer = %+v", data, a)
		}
		var h struct {
			Query   string `json:"query"`
			Chat    int64  `json:"chat"`
			Message int64  `json:"message"`
		}
		if err := json.Unmarshal(a.Handle, &h); err != nil || h.Query != "4417709230112" || h.Chat != 4242 || h.Message != 31 {
			t.Errorf("handle = %s (%v)", a.Handle, err)
		}
	}
}

// A tap in a forum topic is in the topic's session, as its messages are.
func TestATapInATopicIsInTheTopicsSession(t *testing.T) {
	t.Parallel()

	body := strings.Replace(tapBody("a:"+reqA), `"chat": {"id": 4242, "type": "private"}`,
		`"message_thread_id": 3, "is_topic_message": true, "chat": {"id": -1001234567890, "type": "supergroup"}`, 1)
	if s := one(t, body).Session; s != (gateway.Session{Ref: "-1001234567890:topic:3", Kind: gateway.SessionThread}) {
		t.Errorf("session = %+v", s)
	}
}

// The data is whatever bytes were posted, so only what the keyboard writes is
// an answer; the rest is nothing, and never a panic.
func TestHostileCallbackDataIsNothing(t *testing.T) {
	t.Parallel()

	for name, data := range map[string]string{
		"empty":             "",
		"no decision":       ":" + reqA,
		"unknown decision":  "x:" + reqA,
		"upper case letter": "A:" + reqA,
		"just the id":       reqA,
		"no colon":          "a;" + reqA,
		"short id":          "a:" + reqA[:35],
		"long id":           "a:" + reqA + "0",
		"not hex":           "a:" + strings.Replace(reqA, "0192", "zzzz", 1),
		"upper case id":     "a:" + strings.ToUpper(reqA),
		"braces":            "a:{" + reqA[:34] + "}",
		"urn":               "a:" + "urn:uuid:" + reqA[:27],
		"no dashes":         "a:" + strings.ReplaceAll(reqA, "-", "") + "----",
		"a code":            "a:7k2f",
		"huge":              "a:" + strings.Repeat("9", 100000),
		"nul":               "a:\x00" + reqA[1:],
		"invalid utf8":      "a:\xff\xfe" + reqA[2:],
		"json":              `{"a":"` + reqA + `"}`,
	} {
		first, err := telegramParse(tapBody(data))
		if err != nil || len(first.Messages) != 0 {
			t.Errorf("%s: gave %+v, %v; want nothing", name, first, err)
		}
		if second, _ := telegramParse(tapBody(data)); len(second.Messages) != len(first.Messages) {
			t.Errorf("%s: not deterministic", name)
		}
	}
}

// Anything missing that a tap needs is nothing to do, and no field of a body
// can make Parse fail or panic.
func TestATapMissingWhatItNeedsIsNothing(t *testing.T) {
	t.Parallel()

	good := "a:" + reqA
	for name, body := range map[string]string{
		"no id":   strings.Replace(tapBody(good), `"id": "4417709230112"`, `"id": ""`, 1),
		"no from": strings.Replace(tapBody(good), `"from": {"id": 4242, "is_bot": false, "first_name": "Asha", "last_name": "Rao"},`, "", 1),
		"no message (an inline one)": `{"update_id": 9, "callback_query": {"id": "1", "inline_message_id": "x",
		  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"}, "data": "` + good + `"}}`,
		"a message no longer shown": strings.Replace(tapBody(good), `"date": 1762594200`, `"date": 0`, 1),
		"a channel":                 strings.Replace(tapBody(good), `"type": "private"`, `"type": "channel"`, 1),
		"a number as data":          strings.Replace(tapBody(good), `"data": "`+good+`"`, `"data": 7`, 1),
	} {
		if res, err := telegramParse(body); err == nil && len(res.Messages) != 0 {
			t.Errorf("%s gave %+v, want nothing", name, res.Messages)
		}
	}
}

func telegramParse(body string) (gateway.Result, error) {
	return providerForTest().Parse(request(body), creds())
}

func tapHandle() json.RawMessage {
	return json.RawMessage(`{"query":"4417709230112","chat":4242,"message":31}`)
}

func noticeOf(n gateway.ReplyNotice, text string) gateway.Outbound {
	out := outbound("4242", text)
	out.Kind, out.InReplyTo, out.Notice = "notice", "", &n
	return out
}

func methods(t *testing.T, out gateway.Outbound) ([]string, [][]byte) {
	t.Helper()
	reqs, _ := reply(t, out)
	var names []string
	var bodies [][]byte
	for _, r := range reqs {
		names = append(names, strings.TrimPrefix(r.URL, "https://api.telegram.org/bot"+token+"/"))
		bodies = append(bodies, r.Body)
	}
	return names, bodies
}

// A decided tap: the keyboard goes, the outcome is said in the chat, and the
// query is answered, in that order.
func TestASettledTapRemovesTheKeyboardSaysTheOutcomeAndAnswersTheQuery(t *testing.T) {
	t.Parallel()

	names, bodies := methods(t, noticeOf(gateway.ReplyNotice{Handle: tapHandle(), Settled: true}, "Approved: github create_issue."))
	if strings.Join(names, ",") != "editMessageReplyMarkup,sendRichMessage,answerCallbackQuery" {
		t.Fatalf("requests = %v", names)
	}
	if got := string(bodies[0]); got != `{"chat_id":4242,"message_id":31,"reply_markup":{"inline_keyboard":[]}}` {
		t.Errorf("edit = %s", got)
	}
	var said sent
	_ = json.Unmarshal(bodies[1], &said)
	if said.ChatID != 4242 || !strings.Contains(said.RichMessage.Markdown, "Approved") {
		t.Errorf("said = %+v", said)
	}
	if got := string(bodies[2]); got != `{"callback_query_id":"4417709230112"}` {
		t.Errorf("answer = %s", got)
	}
}

// Someone else's tap is told only to them, as an alert, and nothing is said in
// the chat; it changes no keyboard unless it settled something.
func TestAPrivateWordIsAnAlertToTheTapperOnly(t *testing.T) {
	t.Parallel()

	names, bodies := methods(t, noticeOf(gateway.ReplyNotice{Handle: tapHandle(), Private: true}, "Only Asha can answer this."))
	if strings.Join(names, ",") != "answerCallbackQuery" {
		t.Fatalf("requests = %v", names)
	}
	var a struct {
		Query string `json:"callback_query_id"`
		Text  string `json:"text"`
		Alert bool   `json:"show_alert"`
	}
	_ = json.Unmarshal(bodies[0], &a)
	if a.Query != "4417709230112" || a.Text != "Only Asha can answer this." || !a.Alert {
		t.Errorf("answer = %+v", a)
	}

	long := strings.Repeat("é", 500)
	_, bodies = methods(t, noticeOf(gateway.ReplyNotice{Handle: tapHandle(), Private: true}, long))
	_ = json.Unmarshal(bodies[0], &a)
	if n := len([]rune(a.Text)); n != 200 {
		t.Errorf("alert is %d characters, want the 200 Telegram shows", n)
	}
}

// A tap that settled nothing and is not private (a request already answered
// elsewhere, say) leaves the keyboard to its own message and says the word.
func TestAnUnsettledPublicTapSaysItsWordAndKeepsTheKeyboard(t *testing.T) {
	t.Parallel()

	names, _ := methods(t, noticeOf(gateway.ReplyNotice{Handle: tapHandle()}, "Already answered."))
	if strings.Join(names, ",") != "sendRichMessage,answerCallbackQuery" {
		t.Errorf("requests = %v", names)
	}
}

// A typed reply's notice has no handle: its words go to the chat like any reply.
func TestATypedRepliesNoticeIsAnOrdinaryReply(t *testing.T) {
	t.Parallel()

	out := noticeOf(gateway.ReplyNotice{Private: true, Settled: true}, "Approved.")
	out.InReplyTo = "4242:17"
	reqs, bodies := reply(t, out)
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].URL, "/sendRichMessage") || bodies[0].ReplyParameters == nil {
		t.Errorf("requests = %+v", reqs)
	}
	out.Notice = nil
	if reqs, _ = reply(t, out); len(reqs) != 1 {
		t.Errorf("a notice with no detail gave %d requests", len(reqs))
	}
}

// A handle is read back from the database: one that names no query or message
// cannot be answered, and is refused rather than guessed at.
func TestAHandleThatNamesNothingIsRefused(t *testing.T) {
	t.Parallel()

	for name, h := range map[string]string{
		"not json": `nope`, "no query": `{"chat":4242,"message":31}`,
		"no chat": `{"query":"1","message":31}`, "no message": `{"query":"1","chat":4242}`,
	} {
		out := noticeOf(gateway.ReplyNotice{Handle: json.RawMessage(h), Settled: true}, "Approved.")
		if reqs, err := replier().Reply(gateway.Channel{}, out, creds(), receivedAt); err == nil {
			t.Errorf("%s: sent %d requests, want a refusal", name, len(reqs))
		}
	}
}

func providerForTest() gateway.Provider { return telegram.New() }

func replier() gateway.Replier {
	rp, _ := telegram.New().(gateway.Replier)
	return rp
}
