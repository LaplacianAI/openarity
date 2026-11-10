package telegram_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/providertest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
)

const (
	secret = "oawh_secret-token"
	token  = "7012345678:AAHk-example_token"
)

var receivedAt = time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)

func creds() gateway.Credentials {
	return gateway.Credentials{
		gateway.KeySigning:      secret,
		telegram.KeyBotToken:    token,
		telegram.KeyBotID:       "7012345678",
		telegram.KeyBotUsername: "OpenarityBot",
	}
}

func request(body string) gateway.WebhookRequest {
	h := http.Header{}
	h.Set(telegram.VerifyHeader, secret)
	h.Set("Content-Type", "application/json")
	return gateway.WebhookRequest{Method: http.MethodPost, Header: h, Body: []byte(body), ReceivedAt: receivedAt}
}

// A private chat, as Telegram posts it.
const private = `{
  "update_id": 912345001,
  "message": {
    "message_id": 17,
    "from": {"id": 4242, "is_bot": false, "first_name": "Asha", "last_name": "Rao", "username": "asha", "language_code": "en"},
    "chat": {"id": 4242, "first_name": "Asha", "last_name": "Rao", "username": "asha", "type": "private"},
    "date": 1762594200,
    "text": "Why did the nightly export fail?"
  }
}`

// Telegram echoes a shared secret, signing neither the body nor a timestamp,
// so the suite asks for neither a tampered body nor a stale delivery to be
// refused; v1 reads no attachments.
func TestTelegramPassesTheConformanceSuite(t *testing.T) {
	providertest.Run(t, telegram.New(), providertest.Fixtures{
		Creds:                creds(),
		Request:              request(private),
		WantExternalID:       "4242:17",
		SignsNoBody:          true,
		CarriesNoAttachments: true,
	})
}

func parse(t *testing.T, body string) gateway.Result {
	t.Helper()
	res, err := telegram.New().Parse(request(body), creds())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func one(t *testing.T, body string) gateway.Inbound {
	t.Helper()
	res := parse(t, body)
	if len(res.Messages) != 1 {
		t.Fatalf("Parse gave %d messages, want 1", len(res.Messages))
	}
	return res.Messages[0]
}

func TestAPrivateMessageIsADirectConversation(t *testing.T) {
	t.Parallel()

	in := one(t, private)
	if in.ExternalID != "4242:17" || in.Text != "Why did the nightly export fail?" {
		t.Errorf("message = %+v", in)
	}
	if in.Author != (gateway.Author{Ref: "4242", DisplayName: "Asha Rao"}) {
		t.Errorf("author = %+v", in.Author)
	}
	if in.Session != (gateway.Session{Ref: "4242", Kind: gateway.SessionDirect}) {
		t.Errorf("session = %+v", in.Session)
	}
	if !in.SentAt.Equal(time.Unix(1762594200, 0)) {
		t.Errorf("sent at %v", in.SentAt)
	}
	if err := in.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// message_id counts per chat, so two chats' message 17 are two messages.
func TestTheSameMessageIDInTwoChatsIsTwoMessages(t *testing.T) {
	t.Parallel()

	other := strings.ReplaceAll(private, "4242", "5151")
	if a, b := one(t, private).ExternalID, one(t, other).ExternalID; a == b {
		t.Errorf("both chats' message 17 read as %q", a)
	}
}

func group(text, entities string) string {
	return `{"update_id": 1, "message": {"message_id": 90, "date": 1762594200,
	  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"},
	  "chat": {"id": -1001234567890, "title": "Ops", "type": "supergroup"},
	  "text": ` + quote(text) + `, "entities": ` + entities + `}}`
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func addressed(in gateway.Inbound) bool {
	for _, m := range in.Mentions {
		if m.IsUs {
			return true
		}
	}
	return false
}

// In a group the team answers only what is addressed to it: our bot by name
// in any case, never another bot's name.
func TestAGroupMessageIsAddressedOnlyByOurBotsName(t *testing.T) {
	t.Parallel()

	ours := one(t, group("@openaritybot is the export done?", `[{"type":"mention","offset":0,"length":13}]`))
	if ours.Session != (gateway.Session{Ref: "-1001234567890", Kind: gateway.SessionGroup}) {
		t.Errorf("session = %+v", ours.Session)
	}
	if !addressed(ours) {
		t.Errorf("our bot's name, in another case, is not addressed to us: %+v", ours.Mentions)
	}

	theirs := one(t, group("@otherbot is the export done?", `[{"type":"mention","offset":0,"length":9}]`))
	if addressed(theirs) || len(theirs.Mentions) != 1 || theirs.Mentions[0].SenderRef != "@otherbot" {
		t.Errorf("another bot's name = %+v, want it named and not us", theirs.Mentions)
	}

	plain := one(t, group("is the export done?", `[]`))
	if addressed(plain) {
		t.Error("a message naming nobody is addressed to us")
	}
}

// Offsets count UTF-16 units: an emoji before the name moves it by two.
func TestAMentionAfterAnEmojiIsStillFound(t *testing.T) {
	t.Parallel()

	in := one(t, group("🔥 @OpenarityBot help", `[{"type":"mention","offset":3,"length":13}]`))
	if !addressed(in) {
		t.Errorf("mentions = %+v, want our bot found after the emoji", in.Mentions)
	}
}

// An entity Telegram should never send, past the text's end, is ignored, not
// a panic and not a mention.
func TestAnEntityOutsideTheTextIsIgnored(t *testing.T) {
	t.Parallel()

	in := one(t, group("hi", `[{"type":"mention","offset":1,"length":40},{"type":"mention","offset":-1,"length":2}]`))
	if len(in.Mentions) != 0 {
		t.Errorf("mentions = %+v, want none", in.Mentions)
	}
}

func TestACommandToOurBotIsAddressedAndToAnotherIsNot(t *testing.T) {
	t.Parallel()

	if in := one(t, group("/status@OpenarityBot", `[{"type":"bot_command","offset":0,"length":20}]`)); !addressed(in) {
		t.Errorf("a command to our bot is not addressed: %+v", in.Mentions)
	}
	if in := one(t, group("/status@OtherBot", `[{"type":"bot_command","offset":0,"length":16}]`)); addressed(in) {
		t.Error("a command to another bot is addressed to us")
	}
}

func TestATextMentionOfOurBotIsAddressed(t *testing.T) {
	t.Parallel()

	in := one(t, group("Openarity, help", `[{"type":"text_mention","offset":0,"length":9,"user":{"id":7012345678,"is_bot":true,"first_name":"Openarity"}}]`))
	if !addressed(in) {
		t.Errorf("mentions = %+v", in.Mentions)
	}
	other := one(t, group("Asha, help", `[{"type":"text_mention","offset":0,"length":4,"user":{"id":99,"is_bot":false,"first_name":"Asha"}}]`))
	if addressed(other) || other.Mentions[0].SenderRef != "99" {
		t.Errorf("a person named = %+v", other.Mentions)
	}
}

// Replying to the bot's answer is talking to it; replying to a person is not.
func TestAReplyToOurBotIsAddressed(t *testing.T) {
	t.Parallel()

	reply := func(fromID string) string {
		return `{"update_id": 1, "message": {"message_id": 91, "date": 1762594200,
		  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"},
		  "chat": {"id": -1001234567890, "type": "supergroup"},
		  "text": "and yesterday's?",
		  "reply_to_message": {"message_id": 90, "date": 1762594100, "chat": {"id": -1001234567890, "type": "supergroup"},
		    "from": {"id": ` + fromID + `, "is_bot": true, "first_name": "Openarity"}}}}`
	}
	in := one(t, reply("7012345678"))
	if !addressed(in) || in.ReplyTo != "-1001234567890:90" {
		t.Errorf("a reply to our bot = %+v", in)
	}
	if addressed(one(t, reply("555"))) {
		t.Error("a reply to someone else is addressed to us")
	}
}

// Without the bot's identity nothing in a group is addressed to us: a command
// to no bot at all, "/status@", must not match an empty username.
func TestWithoutTheBotsIdentityNothingIsAddressed(t *testing.T) {
	t.Parallel()

	c := creds()
	delete(c, telegram.KeyBotUsername)
	delete(c, telegram.KeyBotID)
	res, err := telegram.New().Parse(request(group("/status@ hi", `[{"type":"bot_command","offset":0,"length":8}]`)), c)
	if err != nil {
		t.Fatal(err)
	}
	if addressed(res.Messages[0]) {
		t.Error("a message was addressed to a bot with no identity")
	}
}

func TestAForumTopicIsItsOwnConversation(t *testing.T) {
	t.Parallel()

	body := `{"update_id": 1, "message": {"message_id": 5, "message_thread_id": 3, "is_topic_message": true,
	  "date": 1762594200, "from": {"id": 4242, "is_bot": false, "first_name": "Asha"},
	  "chat": {"id": -1001234567890, "type": "supergroup", "is_forum": true}, "text": "hi"}}`
	if s := one(t, body).Session; s != (gateway.Session{Ref: "-1001234567890:topic:3", Kind: gateway.SessionThread}) {
		t.Errorf("session = %+v", s)
	}
}

func TestACaptionIsTheTextOfAPhoto(t *testing.T) {
	t.Parallel()

	body := `{"update_id": 1, "message": {"message_id": 6, "date": 1762594200,
	  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"}, "chat": {"id": 4242, "type": "private"},
	  "photo": [{"file_id": "AgAC", "file_unique_id": "AQAD", "width": 90, "height": 90}],
	  "caption": "@OpenarityBot what is this?", "caption_entities": [{"type":"mention","offset":0,"length":13}]}}`
	in := one(t, body)
	if in.Text != "@OpenarityBot what is this?" || !addressed(in) {
		t.Errorf("message = %+v", in)
	}
}

func TestABotIsSaidToBeOne(t *testing.T) {
	t.Parallel()

	if !one(t, strings.Replace(private, `"is_bot": false`, `"is_bot": true`, 1)).Author.IsBot {
		t.Error("a bot's message is not marked as a bot's")
	}
}

// Everything that is not a new message from someone is nothing to do.
func TestWhatIsNotANewMessageIsNothingToDo(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"an edit": `{"update_id": 1, "edited_message": {"message_id": 17, "date": 1, "edit_date": 2,
		  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"}, "chat": {"id": 4242, "type": "private"}, "text": "x"}}`,
		"a channel post": `{"update_id": 1, "channel_post": {"message_id": 3, "date": 1,
		  "chat": {"id": -1009, "type": "channel"}, "text": "news"}}`,
		"a message with nobody behind it": `{"update_id": 1, "message": {"message_id": 3, "date": 1,
		  "sender_chat": {"id": -1009, "type": "supergroup"}, "chat": {"id": -1009, "type": "supergroup"}, "text": "x"}}`,
		"a message in a channel": `{"update_id": 1, "message": {"message_id": 3, "date": 1,
		  "from": {"id": 4242, "is_bot": false, "first_name": "Asha"}, "chat": {"id": -1009, "type": "channel"}, "text": "x"}}`,
	} {
		if res := parse(t, body); len(res.Messages) != 0 {
			t.Errorf("%s gave %d messages, want none", name, len(res.Messages))
		}
	}
}

func outbound(ref, text string) gateway.Outbound {
	return gateway.Outbound{
		ID: "0192f5c4-7d1e-7c3a-9b2e-4f6a8d0c1e23", Session: gateway.Session{Ref: ref, Kind: gateway.SessionDirect},
		InReplyTo: "4242:17", Kind: "answer", Text: text,
	}
}

type sent struct {
	ChatID          int64 `json:"chat_id"`
	MessageThreadID int64 `json:"message_thread_id"`
	RichMessage     struct {
		Markdown string `json:"markdown"`
	} `json:"rich_message"`
	ReplyParameters *struct {
		MessageID                int64 `json:"message_id"`
		AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
	} `json:"reply_parameters"`
}

func reply(t *testing.T, out gateway.Outbound) ([]gateway.OutboundRequest, []sent) {
	t.Helper()
	rp, ok := telegram.New().(gateway.Replier)
	if !ok {
		t.Fatal("telegram does not reply")
	}
	reqs, err := rp.Reply(gateway.Channel{}, out, creds(), receivedAt)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	bodies := make([]sent, len(reqs))
	for i, r := range reqs {
		if err := json.Unmarshal(r.Body, &bodies[i]); err != nil {
			t.Fatalf("part %d is not JSON: %v", i, err)
		}
	}
	return reqs, bodies
}

// The answer goes back to the chat as a rich message, quoting what it answers,
// to Telegram and nowhere else.
func TestAReplyIsARichMessageToTheChat(t *testing.T) {
	t.Parallel()

	reqs, bodies := reply(t, outbound("4242", "## Export\n\n- disk full\n- **fix**: rotate logs"))
	if len(reqs) != 1 {
		t.Fatalf("%d parts, want 1", len(reqs))
	}
	if reqs[0].URL != "https://api.telegram.org/bot"+token+"/sendRichMessage" ||
		reqs[0].Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %v", reqs[0].URL, reqs[0].Header)
	}
	b := bodies[0]
	if b.ChatID != 4242 || b.MessageThreadID != 0 || b.RichMessage.Markdown != "## Export\n\n- disk full\n- **fix**: rotate logs" {
		t.Errorf("body = %+v", b)
	}
	if b.ReplyParameters == nil || b.ReplyParameters.MessageID != 17 || !b.ReplyParameters.AllowSendingWithoutReply {
		t.Errorf("reply parameters = %+v, want message 17, sent even if it is gone", b.ReplyParameters)
	}
}

// A message from another chat is not quoted: Telegram would refuse it.
func TestAReplyQuotesOnlyAMessageInItsOwnChat(t *testing.T) {
	t.Parallel()

	out := outbound("4242", "ok")
	for _, in := range []string{"", "5151:17", "4242:x", "17"} {
		out.InReplyTo = in
		if _, bodies := reply(t, out); bodies[0].ReplyParameters != nil {
			t.Errorf("in reply to %q quoted %+v", in, bodies[0].ReplyParameters)
		}
	}
}

func TestAReplyInATopicGoesToTheTopicButTheGeneralTopicGoesToTheChat(t *testing.T) {
	t.Parallel()

	if _, b := reply(t, outbound("-1001234567890:topic:3", "ok")); b[0].ChatID != -1001234567890 || b[0].MessageThreadID != 3 {
		t.Errorf("topic 3 = %+v", b[0])
	}
	if _, b := reply(t, outbound("-1001234567890:topic:1", "ok")); b[0].MessageThreadID != 0 {
		t.Errorf("the General topic sent thread %d, which Telegram refuses", b[0].MessageThreadID)
	}
}

// An image in the answer would be fetched by Telegram as it is shown; it is
// sent as a link the reader chooses to open.
func TestAReplysImagesAreSentAsLinks(t *testing.T) {
	t.Parallel()

	_, b := reply(t, outbound("4242", "see ![chart](https://evil.example/c.png?d=secret) <b>now</b>"))
	if got := b[0].RichMessage.Markdown; got != "see [chart](https://evil.example/c.png?d=secret) &lt;b>now&lt;/b>" {
		t.Errorf("markdown = %q", got)
	}
}

// A long answer is several messages, only the first quoting, each to the same
// chat, and the same answer always the same parts.
func TestALongReplyIsSeveralMessages(t *testing.T) {
	t.Parallel()

	para := strings.Repeat("The nightly export ran out of disk. ", 400) // 14400 characters
	text := para + "\n\n" + para + "\n\n" + para
	reqs, bodies := reply(t, outbound("4242", text))
	if len(reqs) != 2 {
		t.Fatalf("%d parts, want 2", len(reqs))
	}
	if bodies[0].ReplyParameters == nil || bodies[1].ReplyParameters != nil {
		t.Error("a part other than the first quotes the message, or the first does not")
	}
	if bodies[0].RichMessage.Markdown+"\n\n"+bodies[1].RichMessage.Markdown != text {
		t.Error("the parts are not the answer")
	}
	again, _ := reply(t, outbound("4242", text))
	for i := range reqs {
		if string(reqs[i].Body) != string(again[i].Body) {
			t.Errorf("part %d differs between two builds of the same reply", i)
		}
	}
}

// Nothing a retry fixes: no token, no chat, nothing to say.
func TestAReplyWithNoTokenNoChatOrNoTextIsRefused(t *testing.T) {
	t.Parallel()

	rp, ok := telegram.New().(gateway.Replier)
	if !ok {
		t.Fatal("telegram does not reply")
	}
	noToken := creds()
	delete(noToken, telegram.KeyBotToken)
	cases := map[string]struct {
		out   gateway.Outbound
		creds gateway.Credentials
	}{
		"no token":     {outbound("4242", "ok"), noToken},
		"no chat":      {outbound("general", "ok"), creds()},
		"no topic":     {outbound("4242:topic:x", "ok"), creds()},
		"nothing said": {outbound("4242", "  \n "), creds()},
	}
	for name, c := range cases {
		if reqs, err := rp.Reply(gateway.Channel{}, c.out, c.creds, receivedAt); err == nil {
			t.Errorf("%s: built %d requests", name, len(reqs))
		}
	}
}

func setup(t *testing.T) gateway.Setup {
	t.Helper()
	s, ok := telegram.New().(gateway.Setup)
	if !ok {
		t.Fatal("telegram has no Setup")
	}
	return s
}

func TestSetupAsksTelegramWhoTheBotIs(t *testing.T) {
	t.Parallel()

	s := setup(t)
	if got := s.Supplied(); len(got) != 1 || got[0] != telegram.KeyBotToken {
		t.Errorf("Supplied = %v", got)
	}
	req, err := s.SetupRequest(gateway.Credentials{telegram.KeyBotToken: token})
	if err != nil || req.URL != "https://api.telegram.org/bot"+token+"/getMe" {
		t.Errorf("SetupRequest = %+v, %v", req, err)
	}
}

// A token is sent to Telegram only when it is shaped like one: a path
// separator or a query in it would send it somewhere else.
func TestSetupRefusesWhatIsNotATokenBeforeSendingIt(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "abc", "123:abc/../../x", "123:abc?x=1", "123:abc#x", " 123:abc", "123:"} {
		if req, err := setup(t).SetupRequest(gateway.Credentials{telegram.KeyBotToken: bad}); err == nil {
			t.Errorf("%q became a request to %s", bad, req.URL)
		}
	}
}

func TestCompleteReadsTheBotsIdentity(t *testing.T) {
	t.Parallel()

	got, warnings, err := setup(t).Complete(http.StatusOK,
		[]byte(`{"ok":true,"result":{"id":7012345678,"is_bot":true,"first_name":"Openarity","username":"OpenarityBot","can_join_groups":true,"can_read_all_group_messages":true}}`))
	if err != nil || got[telegram.KeyBotID] != "7012345678" || got[telegram.KeyBotUsername] != "OpenarityBot" || len(got) != 2 {
		t.Errorf("Complete = %v, %v", got, err)
	}
	if len(warnings) != 0 {
		t.Errorf("a bot that reads every group message was warned about: %v", warnings)
	}
}

// A bot as BotFather makes it has privacy mode on: in a group it is sent only
// commands and replies to it, so the person is told, and the channel is made.
func TestABotWithPrivacyModeOnIsConnectedWithAWarning(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"said":    `{"ok":true,"result":{"id":7,"is_bot":true,"username":"OpenarityBot","can_read_all_group_messages":false}}`,
		"omitted": `{"ok":true,"result":{"id":7,"is_bot":true,"username":"OpenarityBot"}}`,
	} {
		got, warnings, err := setup(t).Complete(http.StatusOK, []byte(body))
		if err != nil || got[telegram.KeyBotUsername] != "OpenarityBot" {
			t.Errorf("%s: Complete = %v, %v; want the bot", name, got, err)
		}
		if len(warnings) != 1 || warnings[0] != telegram.PrivacyWarning {
			t.Errorf("%s: warnings = %v, want the privacy warning", name, warnings)
		}
	}
}

// Every refusal is an error, and none repeats the body it was given, which a
// person connecting the channel reads.
func TestCompleteRefusesAnythingButABot(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status int
		body   string
	}{
		"an unknown token": {http.StatusUnauthorized, `{"ok":false,"error_code":401,"description":"Unauthorized"}`},
		"a missing bot":    {http.StatusNotFound, `{"ok":false}`},
		"a Telegram fault": {http.StatusBadGateway, `bad gateway`},
		"not ok":           {http.StatusOK, `{"ok":false}`},
		"garbage":          {http.StatusOK, `<html>`},
		"a person":         {http.StatusOK, `{"ok":true,"result":{"id":5,"is_bot":false,"first_name":"Asha","username":"asha"}}`},
		"no username":      {http.StatusOK, `{"ok":true,"result":{"id":5,"is_bot":true,"first_name":"X"}}`},
		"no id":            {http.StatusOK, `{"ok":true,"result":{"is_bot":true,"username":"x_bot"}}`},
	}
	for name, c := range cases {
		got, _, err := setup(t).Complete(c.status, []byte(c.body))
		if err == nil {
			t.Errorf("%s: completed with %v", name, got)
			continue
		}
		if strings.Contains(err.Error(), c.body) {
			t.Errorf("%s: the error repeats the answer: %v", name, err)
		}
	}
}

func TestTheRegistryTakesTelegram(t *testing.T) {
	t.Parallel()

	reg, err := gateway.NewRegistry(telegram.New())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, ok := reg.Setup("telegram"); !ok {
		t.Error("telegram is not set up through the registry")
	}
	if _, ok := reg.Replier("telegram"); !ok {
		t.Error("telegram does not reply through the registry")
	}
}

func connector(t *testing.T) gateway.Connector {
	t.Helper()
	c, ok := telegram.New().(gateway.Connector)
	if !ok {
		t.Fatal("telegram cannot be pointed at a channel")
	}
	return c
}

// setWebhook carries the channel's address and secret, and asks for new
// messages only: edits and the rest would be posted to be dropped.
func TestConnectPointsTheBotAtTheChannel(t *testing.T) {
	t.Parallel()

	req, err := connector(t).ConnectRequest("https://hooks.example.com/hooks/telegram/c-1", creds())
	if err != nil {
		t.Fatalf("ConnectRequest: %v", err)
	}
	if req.URL != "https://api.telegram.org/bot"+token+"/setWebhook" || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %v", req.URL, req.Header)
	}
	var body struct {
		URL            string   `json:"url"`
		SecretToken    string   `json:"secret_token"`
		AllowedUpdates []string `json:"allowed_updates"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.URL != "https://hooks.example.com/hooks/telegram/c-1" || body.SecretToken != secret ||
		len(body.AllowedUpdates) != 1 || body.AllowedUpdates[0] != "message" {
		t.Errorf("body = %+v", body)
	}
}

// Telegram posts only to https; without a token or a secret there is nothing
// to point with.
func TestConnectRefusesWhatTelegramCouldNotUse(t *testing.T) {
	t.Parallel()

	noToken, noSecret := creds(), creds()
	delete(noToken, telegram.KeyBotToken)
	delete(noSecret, gateway.KeySigning)
	cases := map[string]struct {
		url   string
		creds gateway.Credentials
	}{
		"plain http": {"http://brain.internal/hooks/telegram/c-1", creds()},
		"no address": {"", creds()},
		"no token":   {"https://h.example.com/x", noToken},
		"no secret":  {"https://h.example.com/x", noSecret},
	}
	for name, c := range cases {
		if req, err := connector(t).ConnectRequest(c.url, c.creds); err == nil {
			t.Errorf("%s: built %s", name, req.URL)
		}
	}
}

func TestDisconnectLetsTheBotGo(t *testing.T) {
	t.Parallel()

	req, err := connector(t).DisconnectRequest(creds())
	if err != nil || req.URL != "https://api.telegram.org/bot"+token+"/deleteWebhook" {
		t.Errorf("DisconnectRequest = %+v, %v", req, err)
	}
	if _, err := connector(t).DisconnectRequest(gateway.Credentials{}); err == nil {
		t.Error("a disconnect was built with no token")
	}
}

// Only "ok" is success. Telegram's own reason is passed on, cut to a line,
// and an answer that is not Telegram's says only its status.
func TestAnsweredSaysWhyTelegramRefused(t *testing.T) {
	t.Parallel()

	c := connector(t)
	if err := c.Answered(http.StatusOK, []byte(`{"ok":true,"result":true,"description":"Webhook was set"}`)); err != nil {
		t.Errorf("success read as %v", err)
	}
	for name, a := range map[string]struct {
		status int
		body   string
		says   string
	}{
		"a refusal":  {http.StatusBadRequest, `{"ok":false,"description":"Bad Request: bad webhook:\n Failed to resolve host"}`, "telegram said: Bad Request: bad webhook: Failed to resolve host"},
		"not ok":     {http.StatusOK, `{"ok":false}`, "telegram answered 200"},
		"not json":   {http.StatusBadGateway, `<html>`, "telegram answered 502"},
		"a long one": {http.StatusBadRequest, `{"ok":false,"description":"` + strings.Repeat("x", 500) + `"}`, "telegram said: " + strings.Repeat("x", 200)},
	} {
		if err := c.Answered(a.status, []byte(a.body)); err == nil || err.Error() != a.says {
			t.Errorf("%s: %v, want %q", name, err, a.says)
		}
	}
}
