package gateway

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	PendingCap     = 50
	displayNameMax = 64
)

type Senders interface {
	FindChannelSender(ctx context.Context, arg db.FindChannelSenderParams) (uuid.UUID, error)
	RecordPendingSender(ctx context.Context, arg db.RecordPendingSenderParams) (int64, error)
	NoteWaitingSender(ctx context.Context, arg db.NoteWaitingSenderParams) (int64, error)
}

func ResolveSender(
	ctx context.Context, s Senders, channelID uuid.UUID, in Inbound,
) (uuid.UUID, bool, error) {
	userID, err := s.FindChannelSender(ctx, db.FindChannelSenderParams{
		ChannelID: channelID,
		SenderRef: in.Author.Ref,
	})
	if err == nil {
		return userID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	if in.Author.IsBot {
		return uuid.Nil, false, nil
	}

	_, err = s.RecordPendingSender(ctx, db.RecordPendingSenderParams{
		ChannelID:  channelID,
		SenderRef:  in.Author.Ref,
		SenderName: CleanName(in.Author.DisplayName),
		Cap:        PendingCap,
	})
	if err != nil {
		return uuid.Nil, false, err
	}

	return uuid.Nil, false, nil
}

func CleanName(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError:
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return ' '
		default:
			return r
		}
	}, raw)

	cleaned = strings.Join(strings.Fields(cleaned), " ")

	if utf8.RuneCountInString(cleaned) > displayNameMax {
		cleaned = string([]rune(cleaned)[:displayNameMax])
	}
	return cleaned
}

// TellWaiting writes the notice telling a sender nobody has let in that they
// are waiting, the first time in this wait that they ask the bot. It is
// called after ResolveSender queued them: a sender dropped at the cap has no
// pending row, and so no notice. Reports the notice written, if one was.
func TellWaiting(ctx context.Context, s Senders, ch db.Channel, in Inbound) (int64, bool, error) {
	if !ch.NotifyStrangers || !addressed(in.Session.Kind, in) {
		return 0, false, nil
	}
	id, err := s.NoteWaitingSender(ctx, db.NoteWaitingSenderParams{
		TeamID:      ch.TeamID,
		ChannelID:   ch.ID,
		SenderRef:   in.Author.Ref,
		SessionRef:  in.Session.Ref,
		SessionKind: string(in.Session.Kind),
		MessageRef:  in.ExternalID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return id, true, nil
}
