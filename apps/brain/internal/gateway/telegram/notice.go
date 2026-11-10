package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// Notice tells a sender nobody has let in where they stand, quoting the
// message it answers. In a group it names them by a link to their account,
// so nobody else takes it as theirs; in a private chat their name is enough.
func (provider) Notice(_ gateway.Channel, n gateway.Notice, creds gateway.Credentials) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, errors.New("telegram: no bot token")
	}
	if n.Text == "" {
		return gateway.OutboundRequest{}, fmt.Errorf("telegram: no words for a %q notice", n.Kind)
	}
	chatID, thread, err := target(n.Session.Ref)
	if err != nil {
		return gateway.OutboundRequest{}, err
	}

	body := sendRichMessage{
		ChatID: chatID, MessageThreadID: thread,
		RichMessage: richMessage{Markdown: named(n.Sender, n.Session.Kind) + ", " + n.Text},
	}
	if id, ok := messageIn(n.InReplyTo, chatID); ok {
		body.ReplyParameters = &replyParameters{MessageID: id, AllowSendingWithoutReply: true}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("telegram: encoding the notice: %w", err)
	}
	return gateway.OutboundRequest{URL: apiBase + token + "/sendRichMessage", Header: jsonHeader(), Body: raw}, nil
}

// named is how a notice addresses its sender. The name is the sender's own
// text, so it is escaped: a name written as a link stays words.
func named(a gateway.Author, kind gateway.SessionKind) string {
	name := escapeMarkdown(a.DisplayName)
	if name == "" {
		name = "Hello"
	}
	id, err := strconv.ParseInt(a.Ref, 10, 64)
	if kind == gateway.SessionDirect || err != nil {
		return name
	}
	return "[" + name + "](tg://user?id=" + strconv.FormatInt(id, 10) + ")"
}

// escapeMarkdown backslash-escapes every ASCII punctuation character, which
// Markdown reads as itself.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
