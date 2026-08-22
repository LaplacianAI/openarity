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
