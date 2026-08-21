package gateway

import "time"

type ConversationKind string

const (
	ConversationDirect ConversationKind = "direct"
	ConversationGroup  ConversationKind = "group"
	ConversationThread ConversationKind = "thread"
)

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
	Reply    []byte
}
