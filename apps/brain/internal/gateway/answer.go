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
	steered    uuid.UUID // the run already working, which this message was sent to
	unanswered string    // why a message that asked for an answer gets none
}

func answer(ctx context.Context, q Queries, ch Channel, s db.Session, m Delivery, messageID uuid.UUID) (outcome, error) {
	if !asksUs(s, m) {
		return outcome{}, nil
	}
	if strings.TrimSpace(m.Text) == "" && len(m.Files) == 0 {
		return outcome{unanswered: "the message has no text"}, nil
	}

	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: ch.TeamID, UserID: m.UserID, Action: "run:write"})
	if err != nil {
		return outcome{}, fmt.Errorf("may %s start runs: %w", m.UserID, err)
	}
	if !may {
		return outcome{unanswered: "the sender's member may not start runs"}, nil
	}

	active, err := q.ActiveSessionRun(ctx, s.ID)
	switch {
	case err == nil:
		if err := q.MarkMessageAsks(ctx, messageID); err != nil {
			return outcome{}, fmt.Errorf("mark message %s as asking: %w", messageID, err)
		}
		if err := q.AddSteer(ctx, db.AddSteerParams{RunID: active, Text: steer(m)}); err != nil {
			return outcome{}, fmt.Errorf("steer run %s: %w", active, err)
		}
		return outcome{steered: active}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return outcome{}, fmt.Errorf("active run in session %s: %w", s.ID, err)
	}

	if why, err := cannotRun(ctx, q, ch.TeamID, s.Builder); err != nil || why != "" {
		return outcome{unanswered: why}, err
	}

	id := uuid.New()
	if _, err := q.CreateChannelRun(ctx, db.CreateChannelRunParams{
		ID: id, TeamID: ch.TeamID, SessionID: s.ID, UserID: &m.UserID,
		Input: inputOf(m), MessageID: &messageID,
	}); err != nil {
		return outcome{}, fmt.Errorf("start a run for %q: %w", m.ExternalID, err)
	}
	if err := q.MarkMessageAsks(ctx, messageID); err != nil {
		return outcome{}, fmt.Errorf("mark message %s as asking: %w", messageID, err)
	}
	if _, err := q.ConsumeMessages(ctx, db.ConsumeMessagesParams{RunID: id, Only: &messageID}); err != nil {
		return outcome{}, fmt.Errorf("hand run %s the message it answers: %w", id, err)
	}
	return outcome{run: id}, nil
}

// cannotRun is why the agent that answers this conversation cannot run, or
// "" when it can: the builder needs the team's model, the hub that or an
// agent chosen to be it.
func cannotRun(ctx context.Context, q Queries, teamID uuid.UUID, builder bool) (string, error) {
	if builder {
		can, err := q.BuilderCanRun(ctx, teamID)
		if err != nil {
			return "", fmt.Errorf("can the builder of %s run: %w", teamID, err)
		}
		if !can {
			return "the team has no model set, so its builder cannot run", nil
		}
		return "", nil
	}
	can, err := q.HubCanRun(ctx, teamID)
	if err != nil {
		return "", fmt.Errorf("can the hub of %s run: %w", teamID, err)
	}
	if !can {
		return "the team has no model set, so its hub cannot run", nil
	}
	return "", nil
}

func asksUs(s db.Session, m Delivery) bool { return addressed(SessionKind(s.Kind), m.Inbound) }

// addressed is whether a message asks the bot: always in a direct
// conversation, elsewhere only when it mentions us; never from a bot.
func addressed(kind SessionKind, in Inbound) bool {
	if in.Author.IsBot {
		return false
	}
	if kind == SessionDirect {
		return true
	}
	return slices.ContainsFunc(in.Mentions, func(x Mention) bool { return x.IsUs })
}

func steer(m Delivery) string {
	who := m.Author.DisplayName
	if who == "" {
		who = m.Author.Ref
	}
	said := who + ": " + inputOf(m)
	if len(m.Files) == 0 {
		return said
	}
	// A run reads a steer's text alone; the files it names are in the
	// conversation for attachments() to list.
	names := make([]string, len(m.Files))
	for i, f := range m.Files {
		names[i] = f.Filename
		if names[i] == "" {
			names[i] = "a file with no name"
		}
	}
	return said + "\n[sent with " + strings.Join(names, ", ") + "]"
}

// inputOf is what a message asks a run. A run's first message names the files
// sent with it, so a message of files alone asks that they be looked at.
func inputOf(m Delivery) string {
	if strings.TrimSpace(m.Text) == "" && len(m.Files) > 0 {
		return "The message is the files sent with it."
	}
	return m.Text
}
