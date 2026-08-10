package contracts

import (
	"context"
	"errors"
	"net/http"
)

// ErrIgnore reports an update the adapter understood and deliberately
// declined — an edited message, a bot author, a payload with nothing to say.
// Adapters wrap it with the reason: fmt.Errorf("bot author: %w", ErrIgnore).
var ErrIgnore = errors.New("update ignored")

// WebhookAdapter is one webhook channel. Verify authenticates a request
// against the exact raw bytes received — nothing may parse or re-serialise
// the body before it. The secret is fetched by the caller from
// secrets.SecretStore and handed in per request; the single-string arity
// covers every channel built so far and is expected to widen when a channel
// needs more than one credential.
type WebhookAdapter interface {
	Channel() string
	Verify(r *http.Request, body []byte, secret string) error
	Parse(body []byte) (Message, error)
}

// Sink is where the gateway hands a normalised Message on — the orchestrator,
// once it exists. Deliver must return promptly, as an enqueue would; delivery
// is at-least-once, so downstream dedupes on (Channel, ChannelID,
// ConversationID, ProviderMessageID).
type Sink interface {
	Deliver(ctx context.Context, msg Message) error
}
