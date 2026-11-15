package whatsapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

const (
	// partRunes is where a long reply is split. WhatsApp takes far more in
	// one message; a person reading a phone does not.
	partRunes = 4000

	// SendURL marks a part as the socket's to send. It is never dialled: the
	// HTTP poster would refuse a URL with no host.
	SendURL = "whatsapp:send"
)

// Part is one message to send, as the socket's Poster reads it.
type Part struct {
	// ID is the same on every attempt to send this part.
	ID string `json:"id"`
	// To is the conversation, as Map named it.
	To   string `json:"to"`
	Text string `json:"text"`
	// Quote is the message it answers, on the first part only.
	Quote string `json:"quote,omitempty"`
	// Mentions are the people the text names with @, by their address.
	Mentions []string `json:"mentions,omitempty"`
}

// Reply is a run's answer, or a step of it, as parts: WhatsApp's own marks
// for the Markdown, split at 4,000 characters between lines where it can,
// and a code block cut in two closed and reopened. The same inputs split the
// same way, so a retry resumes at the part not sent.
func (provider) Reply(_ gateway.Channel, out gateway.Outbound, _ gateway.Credentials, _ time.Time) ([]gateway.OutboundRequest, error) {
	to, err := conversation(out.Session.Ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Text) == "" {
		return nil, errors.New("whatsapp: an empty reply")
	}
	texts := split(Format(out.Text), partRunes)
	reqs := make([]gateway.OutboundRequest, 0, len(texts))
	for i, text := range texts {
		p := Part{ID: out.ID + ":" + strconv.Itoa(i), To: to, Text: text}
		if i == 0 {
			p.Quote = out.InReplyTo
		}
		req, err := request(p)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

// conversation is a session's ref, checked to be an address WhatsApp sends to.
func conversation(ref string) (string, error) {
	j, err := types.ParseJID(ref)
	if err != nil || j.User == "" {
		return "", fmt.Errorf("whatsapp: %q is not a conversation", ref)
	}
	switch j.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return j.ToNonAD().String(), nil
	default:
		return "", fmt.Errorf("whatsapp: %q is not a person or a group", ref)
	}
}

func request(p Part) (gateway.OutboundRequest, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("whatsapp: encoding a part: %w", err)
	}
	return gateway.OutboundRequest{URL: SendURL, Body: b}, nil
}

// ReadPart is a part back from the request Reply or Notice built.
func ReadPart(req gateway.OutboundRequest) (Part, error) {
	if req.URL != SendURL {
		return Part{}, fmt.Errorf("whatsapp: %q is not a part to send", req.URL)
	}
	var p Part
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return Part{}, fmt.Errorf("whatsapp: reading a part: %w", err)
	}
	if p.ID == "" || p.To == "" || p.Text == "" {
		return Part{}, errors.New("whatsapp: a part with no id, address or text")
	}
	return p, nil
}

// split cuts text into parts of at most n characters: at the last line break
// that fits, else the last space, else wherever the limit falls. A part that
// ends inside a code block closes it, and the next reopens it.
func split(text string, n int) []string {
	var parts []string
	open := false
	for text != "" {
		head := ""
		if open {
			head = "```\n"
		}
		room := n - utf8.RuneCountInString(head) - len("\n```")
		if utf8.RuneCountInString(text) <= room+len("\n```") {
			if rest := strings.TrimRight(text, "\n "); rest != "" || head != "" {
				parts = append(parts, head+rest)
			}
			break
		}
		cut := cutAt(text, room)
		piece, rest := text[:cut], strings.TrimLeft(text[cut:], "\n ")
		if fences(piece)%2 == 1 != open {
			parts = append(parts, head+strings.TrimRight(piece, "\n")+"\n```")
			open = true
		} else {
			parts = append(parts, head+strings.TrimRight(piece, "\n "))
			open = false
		}
		text = rest
	}
	return parts
}

// cutAt is the byte offset to cut text at so the piece holds at most n
// characters, on a line break or a space when one is in its second half.
func cutAt(text string, n int) int {
	end, count := 0, 0
	for i := range text {
		if count == n {
			end = i
			break
		}
		count++
	}
	if end == 0 {
		return len(text)
	}
	if i := strings.LastIndexByte(text[:end], '\n'); i > end/2 {
		return i + 1
	}
	if i := strings.LastIndexByte(text[:end], ' '); i > end/2 {
		return i + 1
	}
	return end
}

// fences counts the code fences that open or close a block in s.
func fences(s string) int {
	n := 0
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			n++
		}
	}
	return n
}
