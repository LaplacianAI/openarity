package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// A button's callback_data is one letter for the decision, a colon and the
// request's id: "a:" and 36 characters, 38 of the 64 bytes Telegram allows.
// It is only a name. Whether the person who tapped may answer that request is
// the gateway's to decide, so nothing in it is trusted.
const callbackLen = 2 + 36

// maxKeyboardRequests bounds the rows of a keyboard, so a prompt with a great
// many requests cannot draw one Telegram refuses; the rest are answered by
// typing, as the footer says.
const maxKeyboardRequests = 10

type decisionButton struct {
	letter   byte
	decision gateway.Decision
	label    string
	// said is what the tap reads as in the conversation.
	said string
}

var buttons = []decisionButton{
	{'a', gateway.Approve, "Approve", "Approve"},
	{'r', gateway.ApproveRun, "For this run", "Approve for this run"},
	{'d', gateway.Decline, "Decline", "Decline"},
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

// keyboardOf draws a row for each request the chat asks its person to answer.
// With several, each button names its request by its code, which the footer
// shows beside the tool. Nil when no request is to be asked, so Reply sends
// the text alone.
func keyboardOf(p *approval.Prompt) *inlineKeyboard {
	var asked []approval.PromptRequest
	for _, r := range p.Requests {
		if _, err := uuid.Parse(r.ID); r.Ask && err == nil {
			asked = append(asked, r)
		}
	}
	if len(asked) > maxKeyboardRequests {
		asked = asked[:maxKeyboardRequests]
	}
	if len(asked) == 0 {
		return nil
	}
	kb := &inlineKeyboard{}
	for _, r := range asked {
		id, _ := uuid.Parse(r.ID)
		row := make([]inlineButton, 0, len(buttons))
		for _, b := range buttons {
			text := b.label
			if len(asked) > 1 && r.Code != "" {
				text += " " + r.Code
			}
			row = append(row, inlineButton{Text: text, CallbackData: string(b.letter) + ":" + id.String()})
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, row)
	}
	return kb
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *user    `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

// handle is what a notice needs to answer a tap where it was made: the query
// to answer, and the message whose keyboard to take away.
type handle struct {
	Query   string `json:"query"`
	Chat    int64  `json:"chat"`
	Message int64  `json:"message"`
}

// callbackData reads a button's data. It is bytes anyone may send the webhook
// by a client that is not Telegram's, so only exactly what keyboardOf writes
// is taken: nothing else is an answer.
func callbackData(data string) (gateway.Decision, string, bool) {
	if len(data) != callbackLen || data[1] != ':' {
		return "", "", false
	}
	// A parse that fails is the zero id, which is not the text it failed on.
	id, _ := uuid.Parse(data[2:])
	if id.String() != data[2:] {
		return "", "", false
	}
	for _, b := range buttons {
		if b.letter == data[0] {
			return b.decision, id.String(), true
		}
	}
	return "", "", false
}

// tap reads a press of one of our buttons as the sender answering a request in
// the conversation the button was drawn in, so it lands in the session the
// chat's messages do. A press that names nothing we drew, on a message
// Telegram no longer shows us (its date is 0, and it says nothing of a topic)
// or from nobody, is nothing to do.
func tap(q *callbackQuery, at time.Time) gateway.Result {
	if q.ID == "" || q.From == nil || q.Message == nil || q.Message.Date == 0 {
		return gateway.Result{}
	}
	decision, id, ok := callbackData(q.Data)
	if !ok {
		return gateway.Result{}
	}
	session, ok := sessionOf(q.Message)
	if !ok {
		return gateway.Result{}
	}
	raw, err := json.Marshal(handle{Query: q.ID, Chat: q.Message.Chat.ID, Message: q.Message.MessageID})
	if err != nil {
		return gateway.Result{}
	}
	var said string
	for _, b := range buttons {
		if b.decision == decision {
			said = b.said
		}
	}
	return gateway.Result{Messages: []gateway.Inbound{{
		// A query id is unique to the tap; the prefix keeps it from ever
		// equalling a message's "chat:message".
		ExternalID: "callback:" + q.ID,
		Author: gateway.Author{
			Ref:         strconv.FormatInt(q.From.ID, 10),
			DisplayName: displayName(q.From),
			IsBot:       q.From.IsBot,
		},
		Session: session,
		Text:    said,
		SentAt:  at,
		Answer:  &gateway.Answer{ApprovalID: id, Decision: decision, Handle: raw},
	}}}
}

type answerCallbackQuery struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

type editMessageReplyMarkup struct {
	ChatID      int64          `json:"chat_id"`
	MessageID   int64          `json:"message_id"`
	ReplyMarkup inlineKeyboard `json:"reply_markup"`
}

// alertMax is the longest text Telegram shows with answerCallbackQuery.
const alertMax = 200

// tapNotice answers a tap where it was made, in order: the keyboard is taken
// away when the tap settled its request, so an old prompt cannot be tapped
// again; then Text goes into the chat for everyone, as a message of its own
// (the prompt's text is the agent's, and is left as it was said); and the
// query is answered last. A Private word is the query's answer instead, an
// alert only the tapper sees, and goes to nobody else.
//
// The query is answered last because Telegram refuses one that is answered
// twice or late, and a refused request fails the reply: answered first, a
// refusal would keep the keyboard and the outcome from ever being sent.
func tapNotice(token string, chatID, thread int64, out gateway.Outbound) ([]gateway.OutboundRequest, error) {
	var h handle
	if err := json.Unmarshal(out.Notice.Handle, &h); err != nil || h.Query == "" || h.Chat == 0 || h.Message == 0 {
		return nil, errors.New("telegram: the tap's handle names no query and message")
	}
	var reqs []gateway.OutboundRequest
	add := func(method string, body any) error {
		req, err := call(token, method, body)
		if err != nil {
			return err
		}
		reqs = append(reqs, req)
		return nil
	}
	if out.Notice.Settled {
		if err := add("editMessageReplyMarkup", editMessageReplyMarkup{
			ChatID: h.Chat, MessageID: h.Message, ReplyMarkup: inlineKeyboard{InlineKeyboard: [][]inlineButton{}},
		}); err != nil {
			return nil, err
		}
	}
	answer := answerCallbackQuery{CallbackQueryID: h.Query}
	if out.Notice.Private {
		answer.Text, answer.ShowAlert = clip(out.Text, alertMax), true
	} else {
		said, err := texts(token, chatID, thread, out, nil)
		if err != nil {
			return nil, fmt.Errorf("telegram: the tap's outcome: %w", err)
		}
		reqs = append(reqs, said...)
	}
	if err := add("answerCallbackQuery", answer); err != nil {
		return nil, err
	}
	return reqs, nil
}

// clip cuts s to n characters.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
