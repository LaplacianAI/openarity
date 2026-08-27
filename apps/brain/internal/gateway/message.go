package gateway

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

type SessionKind string

const (
	SessionDirect SessionKind = "direct"
	SessionGroup  SessionKind = "group"
	SessionThread SessionKind = "thread"
)

const (
	SenderRefMax  = 256
	SessionRefMax = 256
)

type Session struct {
	Ref  string
	Kind SessionKind
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
	Ref              string
	ClaimedFilename  string
	ClaimedMediaType string
	ClaimedSize      int64
}

type Enrichment struct {
	Replace map[string]string
}

type Inbound struct {
	ExternalID string

	Author  Author
	Session Session

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
	case in.Session.Ref == "":
		return errors.New("no Session.Ref, so it belongs to no conversation")
	case utf8.RuneCountInString(in.Author.Ref) > SenderRefMax:
		return fmt.Errorf("Author.Ref is %d characters, over the %d a sender ref may be",
			utf8.RuneCountInString(in.Author.Ref), SenderRefMax)
	case utf8.RuneCountInString(in.Session.Ref) > SessionRefMax:
		return fmt.Errorf("Session.Ref is %d characters, over the %d a session ref may be",
			utf8.RuneCountInString(in.Session.Ref), SessionRefMax)
	}

	switch in.Session.Kind {
	case SessionDirect, SessionGroup, SessionThread:
	default:
		return fmt.Errorf("session kind %q is not direct, group or thread", in.Session.Kind)
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
