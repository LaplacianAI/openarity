package main

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
)

// logSink stands in for the orchestrator until it exists: it records that a
// message arrived and drops it. Fields only — the text itself is never
// logged, just its length.
type logSink struct {
	logger *slog.Logger
}

func (s logSink) Deliver(_ context.Context, msg contracts.Message) error {
	s.logger.Info("message received",
		"channel", msg.Channel,
		"channel_id", msg.ChannelID,
		"team_id", msg.TeamID,
		"conversation_id", msg.ConversationID,
		"provider_message_id", msg.ProviderMessageID,
		"provider_user_id", msg.ProviderUserID,
		"sent_at", msg.SentAt,
		"text_len", utf8.RuneCountInString(msg.Text),
	)
	return nil
}
