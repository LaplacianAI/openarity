// Package gateway is the brain's public inbound edge: channel adapters and
// the webhook handler that drives them. Nothing downstream learns which
// channel a message came from — see CLAUDE.md in this directory.
package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
)

// secretTokenHeader is where Telegram echoes the secret_token registered at
// setWebhook time. It is the only authentication a Telegram webhook carries:
// the body is not signed, so the token authenticates the sender, not the
// bytes.
const secretTokenHeader = "X-Telegram-Bot-Api-Secret-Token" //nolint:gosec // the header's name, not a credential

// secretTokenShape is setWebhook's grammar for secret_token. A stored secret
// outside it can never match a header Telegram accepted, so it is surfaced
// as misconfiguration (contracts.ErrSecretUnusable) rather than 401ing
// silently forever. Note a bot token contains ':' and is deliberately
// outside this shape.
var secretTokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// maxDate rejects timestamps this adapter refuses to believe: 4102444800 is
// 2100-01-01T00:00:00Z. Telegram's date is attacker-influenced, and a large
// enough value makes time.Unix produce a year beyond 9999, which slog's JSON
// handler emits as a malformed record — the bound protects the log pipeline,
// not the business logic.
const maxDate = 4102444800

// Telegram is the Telegram channel adapter. Stateless: every dependency
// arrives per call.
type Telegram struct{}

func (Telegram) Channel() string { return "telegram" }

// Verify compares the secret-token header against the stored secret in
// constant time. The empty cases are rejected explicitly first:
// ConstantTimeCompare("", "") is 1, so an empty stored secret plus a missing
// header would otherwise authenticate everyone. The comparison runs over
// fixed-width digests because ConstantTimeCompare short-circuits on length —
// comparing the raw strings would leak the stored token's length by timing.
func (Telegram) Verify(r *http.Request, _ []byte, secret string) error {
	if !secretTokenShape.MatchString(secret) {
		return fmt.Errorf("not a valid telegram secret token: %w", contracts.ErrSecretUnusable)
	}
	values := r.Header.Values(secretTokenHeader)
	if len(values) != 1 || values[0] == "" {
		return errors.New("missing secret token header")
	}
	got, want := sha256.Sum256([]byte(values[0])), sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return errors.New("secret token mismatch")
	}
	return nil
}

// The subset of a Telegram Update the adapter reads. Unknown fields are
// ignored by encoding/json, which is what we want: Telegram adds fields
// constantly. IDs are int64 — supergroup chat ids are large negatives and
// user ids exceed 2^32.
type tgUpdate struct {
	Message *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      tgChat  `json:"chat"`
	Date      int64   `json:"date"`
	Text      string  `json:"text"`
	Caption   string  `json:"caption"`
}

type tgUser struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// Parse normalises a verified Update body. It fills only the payload-derived
// Message fields; Channel, ChannelID and TenantID belong to the handler.
//
// Anything that is not a fresh human text-or-caption message wraps ErrIgnore
// with the reason: non-message updates (edits, channel posts, membership
// changes), authorless and bot-authored messages (bot authors are dropped
// here to keep two bots in one group from looping), messages with a
// zero-filled chat id, message id or date — encoding/json zero-fills missing
// fields silently, and a "0" id would corrupt the dedup key downstream —
// and dates past maxDate.
func (Telegram) Parse(body []byte) (contracts.Message, error) {
	var update tgUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		return contracts.Message{}, fmt.Errorf("decode update: %w", err)
	}

	msg := update.Message
	switch {
	case msg == nil:
		return contracts.Message{}, fmt.Errorf("not a message update: %w", contracts.ErrIgnore)
	case msg.From == nil:
		return contracts.Message{}, fmt.Errorf("no author: %w", contracts.ErrIgnore)
	case msg.From.IsBot:
		return contracts.Message{}, fmt.Errorf("bot author: %w", contracts.ErrIgnore)
	case msg.Chat.ID == 0:
		return contracts.Message{}, fmt.Errorf("no chat id: %w", contracts.ErrIgnore)
	case msg.MessageID == 0:
		return contracts.Message{}, fmt.Errorf("no message id: %w", contracts.ErrIgnore)
	case msg.Date <= 0:
		return contracts.Message{}, fmt.Errorf("no date: %w", contracts.ErrIgnore)
	case msg.Date > maxDate:
		return contracts.Message{}, fmt.Errorf("implausible date: %w", contracts.ErrIgnore)
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return contracts.Message{}, fmt.Errorf("no text or caption: %w", contracts.ErrIgnore)
	}

	return contracts.Message{
		ConversationID:    strconv.FormatInt(msg.Chat.ID, 10),
		ProviderMessageID: strconv.FormatInt(msg.MessageID, 10),
		ProviderUserID:    strconv.FormatInt(msg.From.ID, 10),
		Text:              text,
		SentAt:            time.Unix(msg.Date, 0).UTC(),
	}, nil
}
