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
	// resumed is the steered run, queued again because the message gave up
	// what it was waiting for its sender to approve.
	resumed *db.RequeueAnsweredRow
	// notice is the message the brain queued a notice about, to send now.
	notice uuid.UUID
}

// answer is what a stored message does: answers a request its sender was
// asked, starts a run, or steers the one already working. via is where it was
// written, dashboard or channel, as a request it gives up records it.
//
// An answer comes first: a tap on the brain's own prompt asks it whatever the
// channel's word or where it answers, as does a typed code, and a typed
// answer is read before steering,
// so a yes is never a steer, never the next run's input, and never gives up
// the very request it answers. A message that answered is not marked as
// asking, so no run ever takes it.
func answer(ctx context.Context, q Queries, ch Channel, s db.Session, m Delivery, t tap, messageID uuid.UUID, via string) (outcome, error) {
	c := chat{q: q, ch: ch, s: s, m: m, message: messageID}
	if m.Answer != nil {
		return answerTap(ctx, c, t)
	}
	if !asksUs(ch, s, m) {
		// "yes 7K2F" answers where its person writes it, a group thread
		// without a mention too, but only where the channel answers at all.
		if !answersHere(ch.answering(), SessionKind(s.Kind), m.Inbound) {
			return outcome{}, nil
		}
		out, _, err := typedAnswer(ctx, c, false)
		return out, err
	}
	if strings.TrimSpace(m.Text) == "" && len(m.Files) == 0 && len(m.Missed) == 0 {
		return outcome{unanswered: "the message has no text"}, nil
	}
	if out, handled, err := typedAnswer(ctx, c, true); handled || err != nil {
		return out, err
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
		resumed, err := Supersede(ctx, q, active, m.UserID, via)
		if err != nil {
			return outcome{}, err
		}
		return outcome{steered: active, resumed: resumed}, nil
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

func asksUs(ch Channel, s db.Session, m Delivery) bool {
	return addressed(ch.answering(), SessionKind(s.Kind), m.Inbound)
}

// addressed is whether a message asks the bot. Never from a bot or from
// before the bot was there; the owner writing to the account itself always,
// or, when the channel has a trigger word, when it holds the word; never
// outside where the channel answers. Inside it, a channel with a
// trigger word is asked by a message holding the word, in a direct chat or a
// group alike; one without is asked by every direct message and a group
// message that mentions us. The one rule for starting a run, steering one,
// queueing a stranger and telling them so.
func addressed(a Answering, kind SessionKind, in Inbound) bool {
	if !answersHere(a, kind, in) {
		return false
	}
	if in.ToSelf {
		// A word, once set, applies, so notes to self stay notes.
		return a.Trigger == "" || HasWord(in.Text, a.Trigger)
	}
	if a.Trigger != "" {
		return HasWord(in.Text, a.Trigger)
	}
	return kind == SessionDirect || slices.ContainsFunc(in.Mentions, func(x Mention) bool { return x.IsUs })
}

// answersHere is whether the channel reads a message here at all: never a
// bot's or one from before the bot was there, and never outside where the
// channel answers. The owner writing to the account itself is read wherever
// it answers: nobody else is in that conversation.
func answersHere(a Answering, kind SessionKind, in Inbound) bool {
	if in.Author.IsBot || in.Historical {
		return false
	}
	if in.ToSelf {
		return true
	}
	if kind == SessionDirect {
		return a.Where != AnswersInGroups
	}
	return a.Where != AnswersInDirect
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
//
// A file sent and not kept is said after it, so the run can tell the person
// what it never saw rather than answer as if it had.
func inputOf(m Delivery) string {
	said := m.Text
	if strings.TrimSpace(m.Text) == "" && len(m.Files) > 0 {
		said = "The message is the files sent with it."
	}
	if len(m.Missed) == 0 {
		return said
	}
	missed := "[Not received: " + strings.Join(m.Missed, "; ") + ".]"
	if strings.TrimSpace(said) == "" {
		return missed
	}
	return said + "\n" + missed
}

// Superseding is what giving up a request needs: the requests, their events,
// and the run queued again.
type Superseding interface {
	SupersedeApprovals(ctx context.Context, arg db.SupersedeApprovalsParams) ([]db.Approval, error)
	EndTreeRoomWaits(ctx context.Context, arg db.EndTreeRoomWaitsParams) (int64, error)
	AppendDecidedEvents(ctx context.Context, ids []uuid.UUID) error
	RequeueAnswered(ctx context.Context, id uuid.UUID) (db.RequeueAnsweredRow, error)
}

// Supersede is a person writing to a run that waits on them: writing again
// before answering is moving on from what they were asked, so every request
// in the run's tree made for them ends, answered by them, and the run is
// queued again, once nothing else waits, to read what they wrote. Without
// this, their words would be read only after the call they were asked about
// was made. Other people's requests stand. What the tree waits for in the
// sandbox ends too, whoever wrote: the run reads what they wrote now rather
// than once there is room. The caller holds the session, and hands the run
// returned to the workers once it commits.
func Supersede(ctx context.Context, q Superseding, runID, userID uuid.UUID, via string) (*db.RequeueAnsweredRow, error) {
	ended, err := q.SupersedeApprovals(ctx, db.SupersedeApprovalsParams{RootRunID: runID, UserID: userID, Via: via})
	if err != nil {
		return nil, fmt.Errorf("give up what run %s asked %s: %w", runID, userID, err)
	}
	waits, err := q.EndTreeRoomWaits(ctx, db.EndTreeRoomWaitsParams{RootRunID: runID, Ended: "superseded"})
	if err != nil {
		return nil, fmt.Errorf("give up what run %s waits for in the sandbox: %w", runID, err)
	}
	if len(ended) == 0 && waits == 0 {
		return nil, nil
	}
	if len(ended) > 0 {
		ids := make([]uuid.UUID, len(ended))
		for i, a := range ended {
			ids[i] = a.ID
		}
		if err := q.AppendDecidedEvents(ctx, ids); err != nil {
			return nil, fmt.Errorf("record what run %s no longer asks: %w", runID, err)
		}
	}
	row, err := q.RequeueAnswered(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Still running, parking or waiting on someone else: its park, or
		// the last answer, queues it.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue run %s again: %w", runID, err)
	}
	return &row, nil
}

// typedAnswer reads a typed message as an answer, as answerTyped does, and
// marks one it was as an answer: whatever it did (decided a request, was told
// its request is over, was asked which code it meant), it is the person
// acting on a request, not something said, and leaves the conversation as a
// tap does. Kept, not dropped, since its notice answers it. Marked after it is
// stored, unlike a tap: a typed message is known to be an answer only by
// reading it against the requests, which needs its row for the notice. The
// same transaction holds the session, so no reader sees it unmarked.
//
// A bare yes or no, told the code, is marked too: while a request waits its
// run is parked on that very request, so a bare yes is about it, and shown to
// the model it would read as an approval when nothing was decided.
func typedAnswer(ctx context.Context, c chat, addressed bool) (outcome, bool, error) {
	out, handled, err := answerTyped(ctx, c, addressed)
	if err != nil || !handled {
		return out, handled, err
	}
	if err := c.q.MarkMessageAnswer(ctx, c.message); err != nil {
		return outcome{}, true, fmt.Errorf("mark message %s as an answer: %w", c.message, err)
	}
	return out, true, nil
}
