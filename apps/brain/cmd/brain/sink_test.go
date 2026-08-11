package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
)

// The sink is the only evidence a message made it through the gateway, so
// the identifying fields must all be on the line — and the content must not
// be. Message text is user data; the log shipper takes it off the box.
func TestLogSinkLogsFieldsNotContent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := logSink{logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	err := sink.Deliver(t.Context(), contracts.Message{
		Channel:           "telegram",
		ChannelID:         "ch-1",
		TenantID:          "t-1",
		ConversationID:    "-100123",
		ProviderMessageID: "42",
		ProviderUserID:    "556",
		Text:              "the launch codes are 0000",
		SentAt:            time.Unix(1700000000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		`"channel":"telegram"`, `"channel_id":"ch-1"`, `"tenant_id":"t-1"`,
		`"conversation_id":"-100123"`, `"provider_message_id":"42"`, `"provider_user_id":"556"`,
		`"text_len":25`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("record is missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, "launch codes") {
		t.Errorf("the sink logged message content: %s", out)
	}
}
