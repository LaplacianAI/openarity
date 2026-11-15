package gateway

import (
	"context"

	"github.com/google/uuid"
)

type Channel struct {
	ID     uuid.UUID
	TeamID uuid.UUID
	// ReplyURL is where a provider that posts replies to a URL sends them.
	// Empty is a channel that does not reply.
	ReplyURL string
	// AnswersIn is where the channel answers; empty is everywhere.
	AnswersIn AnswersIn
}

// AnswersIn is where a channel answers: everywhere (direct chats, and groups
// where it is mentioned), only groups, or only direct chats. A message
// outside it asks nothing, so a number that is also a person's own can
// answer in their groups and leave their direct chats alone.
type AnswersIn string

const (
	AnswersEverywhere AnswersIn = "everywhere"
	AnswersInGroups   AnswersIn = "groups"
	AnswersInDirect   AnswersIn = "direct"
)

// Valid is whether a is a place a channel can answer.
func (a AnswersIn) Valid() bool {
	switch a {
	case AnswersEverywhere, AnswersInGroups, AnswersInDirect:
		return true
	default:
		return false
	}
}

type Delivery struct {
	Inbound
	UserID uuid.UUID
	Files  []Stored
	// Missed says, a sentence each, which files were sent and not kept, and
	// why, so the run can tell the person rather than the file vanishing.
	Missed []string
}

type Sink interface {
	Deliver(ctx context.Context, ch Channel, msgs []Delivery) error
}
