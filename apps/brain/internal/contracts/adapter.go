package contracts

import (
	"errors"
	"net/http"
)

// ErrIgnore reports an update the adapter understood and deliberately
// declined — an edited message, a bot author, a payload with nothing to say.
// Adapters wrap it with the reason: fmt.Errorf("bot author: %w", ErrIgnore).
var ErrIgnore = errors.New("update ignored")

// ErrSecretUnusable reports a stored secret the channel's provider could
// never have accepted — a misconfiguration, not an attack. Adapters return
// it from Verify so the handler can log it under its own reason instead of
// a plain verification failure.
var ErrSecretUnusable = errors.New("stored secret is unusable for this channel")

// WebhookAdapter is one webhook channel. Verify authenticates a request
// against the exact raw bytes received — nothing may parse or re-serialise
// the body before it. The secret is fetched by the caller from
// secrets.Store and handed in per request; the single-string arity
// covers every channel built so far and is expected to widen when a channel
// needs more than one credential.
type WebhookAdapter interface {
	Channel() string
	Verify(r *http.Request, body []byte, secret string) error
	Parse(body []byte) (Message, error)
}
