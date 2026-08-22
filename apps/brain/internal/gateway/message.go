package gateway

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

type ConversationKind string

const (
	ConversationDirect ConversationKind = "direct"
	ConversationGroup  ConversationKind = "group"
	ConversationThread ConversationKind = "thread"
)
const SenderRefMax = 256

type Conversation struct {
	Ref  string
	Kind ConversationKind
}

type Author struct {
	Ref         string
	DisplayName string
	IsBot       bool
}

type Mention struct {
	SenderRef   string
	DisplayName string
	IsUs        bool
}

type Attachment struct {
	Ref       string
	Filename  string
	MediaType string
	Size      int64
}

type Enrichment struct {
	Replace map[string]string
}

type Inbound struct {
	ExternalID string

	Author       Author
	Conversation Conversation

	Text string

	Mentions []Mention

	Attachments []Attachment

	Enrichment Enrichment

	ReplyTo string

	SentAt time.Time
}

type Result struct {
	Messages []Inbound
	Ack      []byte
}

func (in Inbound) Validate() error {
	switch {
	case in.ExternalID == "":
		return errors.New("no ExternalID, which is the idempotency key")
	case in.Author.Ref == "":
		return errors.New("no Author.Ref, so the sender cannot be resolved")
	case in.Conversation.Ref == "":
		return errors.New("no Conversation.Ref, so it belongs to no session")
	case utf8.RuneCountInString(in.Author.Ref) > SenderRefMax:
		return fmt.Errorf("Author.Ref is %d characters, over the %d a sender ref may be",
			utf8.RuneCountInString(in.Author.Ref), SenderRefMax)
	}

	switch in.Conversation.Kind {
	case ConversationDirect, ConversationGroup, ConversationThread:
	default:
		return fmt.Errorf("conversation kind %q is not direct, group or thread", in.Conversation.Kind)
	}

	for i, m := range in.Mentions {
		switch {
		case m.SenderRef == "":
			return fmt.Errorf("mention %d has no SenderRef", i)
		case utf8.RuneCountInString(m.SenderRef) > SenderRefMax:
			return fmt.Errorf("mention %d has a SenderRef of %d characters, over the %d allowed",
				i, utf8.RuneCountInString(m.SenderRef), SenderRefMax)
		}
	}
	return nil
}
