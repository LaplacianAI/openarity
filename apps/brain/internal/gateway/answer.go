package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type outcome struct {
	run        uuid.UUID // a run started for this message, to hand to the workers
	unanswered string    // why a message that asked for an answer gets none
}

func answer(ctx context.Context, q Queries, ch Channel, s db.Session, m Delivery, messageID uuid.UUID) (outcome, error) {
	if !asksUs(s, m) {
		return outcome{}, nil
	}
	if strings.TrimSpace(m.Text) == "" {
		return outcome{unanswered: "the message has no text"}, nil
	}

	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: ch.TeamID, UserID: m.UserID, Action: "run:write"})
	if err != nil {
		return outcome{}, fmt.Errorf("may %s start runs: %w", m.UserID, err)
	}
	if !may {
		return outcome{unanswered: "the sender's member may not start runs"}, nil
	}

	active, err := q.ActiveChannelRun(ctx, s.ID)
	switch {
	case err == nil:
		if err := q.AddSteer(ctx, db.AddSteerParams{RunID: active, Text: steer(m)}); err != nil {
			return outcome{}, fmt.Errorf("steer run %s: %w", active, err)
		}
		return outcome{}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return outcome{}, fmt.Errorf("active run in session %s: %w", s.ID, err)
	}

	can, err := q.HubCanRun(ctx, ch.TeamID)
	if err != nil {
		return outcome{}, fmt.Errorf("can the hub of %s run: %w", ch.TeamID, err)
	}

	if !can {
		return outcome{unanswered: "the team has no model set, so its hub cannot run"}, nil
	}

	id := uuid.New()
	if _, err := q.CreateChannelRun(ctx, db.CreateChannelRunParams{
		ID: id, TeamID: ch.TeamID, SessionID: s.ID, UserID: &m.UserID,
		Input: m.Text, MessageID: &messageID,
	}); err != nil {
		return outcome{}, fmt.Errorf("start a run for %q: %w", m.ExternalID, err)
	}
	return outcome{run: id}, nil
}

func asksUs(s db.Session, m Delivery) bool {
	if m.Author.IsBot {
		return false
	}
	if s.Kind == string(SessionDirect) {
		return true
	}
	return slices.ContainsFunc(m.Mentions, func(x Mention) bool { return x.IsUs })
}

func steer(m Delivery) string {
	who := m.Author.DisplayName
	if who == "" {
		who = m.Author.Ref
	}
	return who + ": " + m.Text
}
