package contracts

import "context"

// Sink is where the gateway hands a normalised Message on — the orchestrator,
// once it exists. Deliver must return promptly, as an enqueue would; delivery
// is at-least-once, so downstream dedupes on (Channel, ChannelID,
// ConversationID, ProviderMessageID).
type Sink interface {
	Deliver(ctx context.Context, msg Message) error
}
