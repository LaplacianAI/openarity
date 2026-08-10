package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
)

func verifyRequest(t *testing.T, headerValues ...string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook/telegram/ch-1", nil)
	for _, v := range headerValues {
		r.Header.Add(secretTokenHeader, v)
	}
	return r
}

func TestVerify(t *testing.T) {
	t.Parallel()

	tooLong := strings.Repeat("a", 257)

	tests := map[string]struct {
		headers []string
		secret  string
		wantErr bool
	}{
		"matching token passes": {[]string{"tok_A-1"}, "tok_A-1", false},
		"wrong token":           {[]string{"wrong"}, "tok_A-1", true},
		"missing header":        {nil, "tok_A-1", true},
		"empty header value":    {[]string{""}, "tok_A-1", true},
		"duplicate headers":     {[]string{"tok_A-1", "tok_A-1"}, "tok_A-1", true},

		// ConstantTimeCompare("", "") returns 1 — these explicit rejections
		// are all that stands between a blank stored secret and a full
		// authentication bypass.
		"empty secret and no header":    {nil, "", true},
		"empty secret and empty header": {[]string{""}, "", true},

		// setWebhook only accepts [A-Za-z0-9_-]{1,256}, so a stored secret
		// outside that shape (a bot token has a colon) can never match and
		// must fail loudly, even when the attacker echoes it.
		"bot token shaped secret": {[]string{"123456:AAHtoken"}, "123456:AAHtoken", true},
		"secret over 256 chars":   {[]string{tooLong}, tooLong, true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := Telegram{}.Verify(verifyRequest(t, tc.headers...), nil, tc.secret)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("Verify err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// The handler logs a misconfigured secret as its own outcome, so the error
// identity is a contract, not a message.
func TestVerifyFlagsUnusableStoredSecret(t *testing.T) {
	t.Parallel()

	for name, secret := range map[string]string{
		"empty":            "",
		"bot token shaped": "123456:AAHtoken",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := Telegram{}.Verify(verifyRequest(t, secret), nil, secret)
			if !errors.Is(err, errSecretUnusable) {
				t.Errorf("Verify err = %v, want errSecretUnusable", err)
			}
		})
	}
}

// A real supergroup update: negative 64-bit chat id, user id past 2^32.
const updateJSON = `{
	"update_id": 794029,
	"message": {
		"message_id": 42,
		"from": {"id": 5561234567, "is_bot": false, "first_name": "Ada", "username": "ada"},
		"chat": {"id": -1001234567890, "type": "supergroup", "title": "ops"},
		"date": 1700000000,
		"text": "deploy the thing"
	}
}`

// The want has Channel, ChannelID and TenantID zero on purpose: those are
// the handler's fields, and Parse filling them would put two owners on one
// field.
func TestParseNormalisesARealUpdate(t *testing.T) {
	t.Parallel()

	got, err := Telegram{}.Parse([]byte(updateJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !got.SentAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("SentAt = %v, want unix 1700000000", got.SentAt)
	}
	if got.SentAt.Location() != time.UTC {
		t.Errorf("SentAt location = %v, want UTC", got.SentAt.Location())
	}
	got.SentAt = time.Time{}

	want := contracts.Message{
		ConversationID:    "-1001234567890",
		ProviderMessageID: "42",
		ProviderUserID:    "5561234567",
		Text:              "deploy the thing",
	}
	if got != want {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

// Telegram puts a photo's caption in "caption", not "text". Treating no-text
// as ignorable without this fallback silently drops every captioned photo
// forever.
func TestParseFallsBackToTheCaption(t *testing.T) {
	t.Parallel()

	body := `{"message": {"message_id": 7, "from": {"id": 9}, "chat": {"id": 3},
		"date": 1700000000, "caption": "look at this graph"}}`

	got, err := Telegram{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Text != "look at this graph" {
		t.Errorf("Text = %q, want the caption", got.Text)
	}
}

// Everything that is not a fresh human message wraps ErrIgnore. The zero-id
// cases matter because encoding/json zero-fills missing fields without
// erroring, and a ConversationID of "0" would corrupt the dedup key.
func TestParseIgnores(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"edited message":  `{"update_id": 1, "edited_message": {"message_id": 1}}`,
		"membership only": `{"update_id": 1, "my_chat_member": {}}`,
		"null body":       `null`,
		"empty object":    `{}`,
		"no author":       `{"message": {"message_id": 1, "chat": {"id": 2}, "date": 3, "text": "x"}}`,
		"bot author":      `{"message": {"message_id": 1, "from": {"id": 2, "is_bot": true}, "chat": {"id": 3}, "date": 4, "text": "x"}}`,
		"zero chat id":    `{"message": {"message_id": 1, "from": {"id": 2}, "date": 4, "text": "x"}}`,
		"zero message id": `{"message": {"from": {"id": 2}, "chat": {"id": 3}, "date": 4, "text": "x"}}`,
		"zero date":       `{"message": {"message_id": 1, "from": {"id": 2}, "chat": {"id": 3}, "text": "x"}}`,
		"no text or caption": `{"message": {"message_id": 1, "from": {"id": 2}, "chat": {"id": 3},
			"date": 4, "photo": [{"file_id": "abc"}]}}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Telegram{}.Parse([]byte(body))
			if !errors.Is(err, contracts.ErrIgnore) {
				t.Errorf("Parse err = %v, want ErrIgnore", err)
			}
		})
	}
}

// Malformed input is an error, never ErrIgnore — the handler logs the two
// outcomes differently, and a decode failure must not read as a decision.
func TestParseRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty body":       ``,
		"not json":         `secret-token=hunter2`,
		"truncated":        `{"message": {"message_id": 4`,
		"wrong type":       `[1, 2, 3]`,
		"trailing garbage": `{} {}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Telegram{}.Parse([]byte(body))
			if err == nil {
				t.Fatal("Parse accepted malformed input")
			}
			if errors.Is(err, contracts.ErrIgnore) {
				t.Errorf("malformed input classified as ignorable: %v", err)
			}
		})
	}
}
