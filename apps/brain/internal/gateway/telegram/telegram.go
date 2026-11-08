// Package telegram reads a Telegram bot's webhook and answers in the chat a
// message came from, as a rich message.
package telegram

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

const (
	// KeyBotToken is the token @BotFather gives; a person supplies it.
	KeyBotToken = "bot_token"
	// KeyBotID and KeyBotUsername come from getMe when the channel is
	// connected: Parse needs them to tell a message to our bot from one to
	// another bot in the same group.
	KeyBotID       = "bot_id"
	KeyBotUsername = "bot_username"

	// VerifyHeader carries the secret_token given to setWebhook.
	VerifyHeader = "X-Telegram-Bot-Api-Secret-Token"
)

// apiBase is fixed: a bot token is only ever sent to Telegram.
const apiBase = "https://api.telegram.org/bot"

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

type provider struct{}

func New() gateway.Provider { return provider{} }

func (provider) Name() string { return "telegram" }

func (provider) Routes() []gateway.Route { return []gateway.Route{{Method: http.MethodPost}} }

func (provider) Keys() []string {
	return []string{gateway.KeySigning, KeyBotToken, KeyBotID, KeyBotUsername}
}

// Verify compares the secret Telegram echoes back. Telegram signs nothing and
// sends no timestamp, so a repeated update is caught by its ExternalID instead.
// A channel missing any of its credentials takes nothing: without the bot's
// identity, no message in a group could be told apart as addressed to it.
func (p provider) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	for _, k := range p.Keys() {
		if creds.Get(k) == "" {
			return fmt.Errorf("telegram: no %s", k)
		}
	}
	secret := creds.Get(gateway.KeySigning)
	if !hmac.Equal([]byte(req.Header.Get(VerifyHeader)), []byte(secret)) {
		return errors.New("telegram: bad secret token")
	}
	return nil
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID       int64     `json:"message_id"`
	MessageThreadID int64     `json:"message_thread_id"`
	IsTopicMessage  bool      `json:"is_topic_message"`
	Date            int64     `json:"date"`
	From            *user     `json:"from"`
	Chat            chat      `json:"chat"`
	Text            string    `json:"text"`
	Caption         string    `json:"caption"`
	Entities        []entity  `json:"entities"`
	CaptionEntities []entity  `json:"caption_entities"`
	ReplyTo         *replyMsg `json:"reply_to_message"`
}

type replyMsg struct {
	MessageID int64 `json:"message_id"`
	From      *user `json:"from"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user"`
}

// Parse reads a new message. Every other update (an edit, a channel post, a
// button press) is nothing to do, and so is a message with no person behind
// it, such as one sent as the group itself.
func (provider) Parse(req gateway.WebhookRequest, creds gateway.Credentials) (gateway.Result, error) {
	var u update
	if err := json.Unmarshal(req.Body, &u); err != nil {
		return gateway.Result{}, fmt.Errorf("telegram: %w", err)
	}
	m := u.Message
	if m == nil || m.From == nil {
		return gateway.Result{}, nil
	}
	session, ok := sessionOf(m)
	if !ok {
		return gateway.Result{}, nil
	}

	chatID := strconv.FormatInt(m.Chat.ID, 10)
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}

	in := gateway.Inbound{
		ExternalID: chatID + ":" + strconv.FormatInt(m.MessageID, 10),
		Author: gateway.Author{
			Ref:         strconv.FormatInt(m.From.ID, 10),
			DisplayName: displayName(m.From),
			IsBot:       m.From.IsBot,
		},
		Session:  session,
		Text:     text,
		Mentions: mentions(text, entities, m.ReplyTo, creds),
		SentAt:   time.Unix(m.Date, 0).UTC(),
	}
	if m.ReplyTo != nil {
		in.ReplyTo = chatID + ":" + strconv.FormatInt(m.ReplyTo.MessageID, 10)
	}
	return gateway.Result{Messages: []gateway.Inbound{in}}, nil
}

// sessionOf names the conversation. A forum topic is its own conversation,
// inside its group; a channel post is not a conversation with anyone.
func sessionOf(m *message) (gateway.Session, bool) {
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	if m.IsTopicMessage && m.MessageThreadID != 0 {
		return gateway.Session{
			Ref: chatID + ":topic:" + strconv.FormatInt(m.MessageThreadID, 10), Kind: gateway.SessionThread,
		}, true
	}
	switch m.Chat.Type {
	case "private":
		return gateway.Session{Ref: chatID, Kind: gateway.SessionDirect}, true
	case "group", "supergroup":
		return gateway.Session{Ref: chatID, Kind: gateway.SessionGroup}, true
	}
	return gateway.Session{}, false
}

func displayName(u *user) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		return "@" + u.Username
	}
	return name
}

// mentions is who a message names. It is addressed to us when it names our
// bot by username or by id, sends a command to it, or replies to it.
func mentions(text string, entities []entity, replyTo *replyMsg, creds gateway.Credentials) []gateway.Mention {
	botID, botName := creds.Get(KeyBotID), creds.Get(KeyBotUsername)
	units := utf16.Encode([]rune(text))
	var out []gateway.Mention
	for _, e := range entities {
		switch e.Type {
		case "mention":
			name := strings.TrimPrefix(slice(units, e.Offset, e.Length), "@")
			if name == "" {
				continue
			}
			out = append(out, gateway.Mention{
				SenderRef: "@" + name, DisplayName: "@" + name,
				IsUs: strings.EqualFold(name, botName),
			})
		case "text_mention":
			if e.User == nil {
				continue
			}
			id := strconv.FormatInt(e.User.ID, 10)
			out = append(out, gateway.Mention{
				SenderRef: id, DisplayName: displayName(e.User), IsUs: id == botID,
			})
		case "bot_command":
			// "/status@" names no bot; without the guard it would name ours
			// whenever the username is missing.
			_, to, ok := strings.Cut(slice(units, e.Offset, e.Length), "@")
			if ok && botName != "" && strings.EqualFold(to, botName) {
				out = append(out, gateway.Mention{SenderRef: botID, DisplayName: "@" + botName, IsUs: true})
			}
		}
	}
	if replyTo != nil && replyTo.From != nil && strconv.FormatInt(replyTo.From.ID, 10) == botID {
		out = append(out, gateway.Mention{SenderRef: botID, DisplayName: "@" + botName, IsUs: true})
	}
	return out
}

// slice is an entity's text. Telegram counts offsets in UTF-16 code units, so
// an emoji before a mention moves it by two, not one.
func slice(units []uint16, offset, length int) string {
	if offset < 0 || length <= 0 || offset > len(units) || length > len(units)-offset {
		return ""
	}
	return string(utf16.Decode(units[offset : offset+length]))
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

type richMessage struct {
	Markdown string `json:"markdown"`
}

type sendRichMessage struct {
	ChatID          int64            `json:"chat_id"`
	MessageThreadID int64            `json:"message_thread_id,omitempty"`
	RichMessage     richMessage      `json:"rich_message"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
}

// Reply sends the answer as rich messages, so headings, lists, tables and
// code read as they were written. One too long for a message is several, and
// only the first quotes the message it answers.
func (provider) Reply(
	_ gateway.Channel, out gateway.Outbound, creds gateway.Credentials, _ time.Time,
) ([]gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return nil, errors.New("telegram: no bot token")
	}
	chatID, thread, err := target(out.Session.Ref)
	if err != nil {
		return nil, err
	}
	parts := Split(Sanitize(out.Text))
	if len(parts) == 0 {
		return nil, errors.New("telegram: nothing to send")
	}

	h := http.Header{}
	h.Set("Content-Type", "application/json")
	reqs := make([]gateway.OutboundRequest, 0, len(parts))
	for i, part := range parts {
		body := sendRichMessage{ChatID: chatID, MessageThreadID: thread, RichMessage: richMessage{Markdown: part}}
		if i == 0 {
			if id, ok := messageIn(out.InReplyTo, chatID); ok {
				body.ReplyParameters = &replyParameters{MessageID: id, AllowSendingWithoutReply: true}
			}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("telegram: encoding the reply: %w", err)
		}
		reqs = append(reqs, gateway.OutboundRequest{URL: apiBase + token + "/sendRichMessage", Header: h, Body: raw})
	}
	return reqs, nil
}

// target reads back the chat and topic sessionOf named. The General topic is
// thread 1, which Telegram refuses as a thread id: it is sent to the chat.
func target(ref string) (int64, int64, error) {
	chatPart, topicPart, isTopic := strings.Cut(ref, ":topic:")
	chatID, err := strconv.ParseInt(chatPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("telegram: session %q names no chat", ref)
	}
	if !isTopic {
		return chatID, 0, nil
	}
	thread, err := strconv.ParseInt(topicPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("telegram: session %q names no topic", ref)
	}
	if thread == 1 {
		thread = 0
	}
	return chatID, thread, nil
}

// messageIn is the message id of an ExternalID, when it is in this chat.
func messageIn(externalID string, chatID int64) (int64, bool) {
	c, m, ok := strings.Cut(externalID, ":")
	if !ok || c != strconv.FormatInt(chatID, 10) {
		return 0, false
	}
	id, err := strconv.ParseInt(m, 10, 64)
	return id, err == nil
}

type setWebhook struct {
	URL            string   `json:"url"`
	SecretToken    string   `json:"secret_token"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// ConnectRequest points the bot at the channel: Telegram posts each new
// message to hookURL with the channel's secret, and nothing else. Telegram
// posts only to https, so a brain without a public https address cannot be
// pointed at; the person is shown the step instead.
func (provider) ConnectRequest(hookURL string, creds gateway.Credentials) (gateway.OutboundRequest, error) {
	token, secret := creds.Get(KeyBotToken), creds.Get(gateway.KeySigning)
	switch {
	case !tokenPattern.MatchString(token):
		return gateway.OutboundRequest{}, errors.New("telegram: no bot token")
	case secret == "":
		return gateway.OutboundRequest{}, errors.New("telegram: no secret token")
	case !strings.HasPrefix(hookURL, "https://"):
		return gateway.OutboundRequest{}, errors.New("telegram posts only to an https address")
	}
	raw, err := json.Marshal(setWebhook{URL: hookURL, SecretToken: secret, AllowedUpdates: []string{"message"}})
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("telegram: encoding setWebhook: %w", err)
	}
	return gateway.OutboundRequest{URL: apiBase + token + "/setWebhook", Header: jsonHeader(), Body: raw}, nil
}

// DisconnectRequest stops Telegram posting to a channel that is going away.
// Pending updates are kept: the bot may be connected again.
func (provider) DisconnectRequest(creds gateway.Credentials) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if !tokenPattern.MatchString(token) {
		return gateway.OutboundRequest{}, errors.New("telegram: no bot token")
	}
	return gateway.OutboundRequest{URL: apiBase + token + "/deleteWebhook", Header: jsonHeader(), Body: []byte("{}")}, nil
}

// Answered reads Telegram's answer to setWebhook or deleteWebhook. Its own
// description says what to fix ("bad webhook: Failed to resolve host"), and
// never holds the token, so it is passed on, cut to a line.
func (provider) Answered(status int, body []byte) error {
	var a struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return fmt.Errorf("telegram answered %d", status)
	}
	if status == http.StatusOK && a.OK {
		return nil
	}
	reason := strings.Join(strings.Fields(a.Description), " ")
	if reason == "" {
		return fmt.Errorf("telegram answered %d", status)
	}
	if r := []rune(reason); len(r) > 200 {
		reason = string(r[:200])
	}
	return fmt.Errorf("telegram said: %s", reason)
}

func jsonHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return h
}

// Supplied is what a person gives when connecting a Telegram channel.
func (provider) Supplied() []string { return []string{KeyBotToken} }

// SetupRequest asks Telegram which bot the token belongs to. A token that is
// not shaped like one is refused before it is sent anywhere.
func (provider) SetupRequest(supplied gateway.Credentials) (gateway.OutboundRequest, error) {
	token := supplied.Get(KeyBotToken)
	if !tokenPattern.MatchString(token) {
		return gateway.OutboundRequest{}, errors.New("bot_token is not a Telegram bot token: it looks like 123456:ABC-xyz")
	}
	return gateway.OutboundRequest{URL: apiBase + token + "/getMe", Header: http.Header{}}, nil
}

// Complete reads getMe. What it returns about a refusal never echoes the
// token: it is shown to whoever connected the channel.
func (provider) Complete(status int, body []byte) (gateway.Credentials, error) {
	if status == http.StatusUnauthorized || status == http.StatusNotFound {
		return nil, errors.New("the bot token is not one Telegram knows")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("asked about the bot, Telegram answered %d", status)
	}
	var me struct {
		OK     bool `json:"ok"`
		Result user `json:"result"`
	}
	if err := json.Unmarshal(body, &me); err != nil || !me.OK {
		return nil, errors.New("the answer Telegram gave about the bot could not be read")
	}
	if !me.Result.IsBot || me.Result.ID == 0 || me.Result.Username == "" {
		return nil, errors.New("the token belongs to an account that is not a bot")
	}
	return gateway.Credentials{
		KeyBotID:       strconv.FormatInt(me.Result.ID, 10),
		KeyBotUsername: me.Result.Username,
	}, nil
}
