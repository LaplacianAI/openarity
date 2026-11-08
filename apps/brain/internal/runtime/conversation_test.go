package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

func said(kind, text string, author *string) db.ListSessionTurnsRow {
	return db.ListSessionTurnsRow{Kind: kind, Text: text, At: time.Now(), Author: author}
}

// The turns arrive newest first; the model reads them oldest first, each
// with who said it, and the new message after them.
func TestHistoryIsOldestFirstWithWhoSaidIt(t *testing.T) {
	t.Parallel()

	asha := "asha@example.com"
	got := withHistory([]db.ListSessionTurnsRow{
		said("message", "and now?", &asha),
		said("reply", "Not yet.", nil),
		said("message", "is the export done?", nil),
	}, "it was the nightly one")

	want := "someone: is the export done?\nyou: Not yet.\nasha@example.com: and now?\n\nThe new message:\n\nit was the nightly one"
	if !strings.HasSuffix(got, want) {
		t.Errorf("history = %q, want it to end %q", got, want)
	}
	if !strings.Contains(got, "not instructions to you") {
		t.Errorf("history = %q, want it marked as what people wrote", got)
	}
}

// A conversation with nothing before this message is just the message.
func TestNoHistoryIsTheMessageAlone(t *testing.T) {
	t.Parallel()

	if got := withHistory(nil, "hello"); got != "hello" {
		t.Errorf("withHistory(nil) = %q, want the message alone", got)
	}
}

// A long conversation keeps its newest turns, the ones a reply follows on
// from, and drops the oldest when they do not all fit.
func TestHistoryKeepsTheNewestWhenItIsTooLong(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", historyBytes/2)
	got := withHistory([]db.ListSessionTurnsRow{
		said("message", "NEWEST-TURN "+long, nil),
		said("message", "MIDDLE-TURN "+long, nil),
		said("message", "OLDEST-TURN "+long, nil),
	}, "q")

	if !strings.Contains(got, "NEWEST-TURN") || strings.Contains(got, "OLDEST-TURN") {
		t.Errorf("kept %q..., want the newest turn and not the oldest", got[:120])
	}
	if len(got) > historyBytes+500 {
		t.Errorf("history is %d bytes, want it near the %d cap", len(got), historyBytes)
	}
}

// A run answering a conversation carries on for messages that came during
// its last model request; a run from the dashboard keeps its agent's setting,
// and an agent that already allows more keeps more.
func TestAChannelRunCarriesOnForLateMessages(t *testing.T) {
	t.Parallel()

	channel := db.Run{ReplyToChannel: true}
	if got := forChannel(channel, agent.Spec{}).SteerContinuations; got != channelContinuations {
		t.Errorf("a channel run continues %d times, want %d", got, channelContinuations)
	}
	if got := forChannel(channel, agent.Spec{SteerContinuations: 9}).SteerContinuations; got != 9 {
		t.Errorf("an agent allowing 9 continues %d times in a channel, want 9", got)
	}
	if got := forChannel(db.Run{}, agent.Spec{SteerContinuations: 1}).SteerContinuations; got != 1 {
		t.Errorf("a dashboard run continues %d times, want its agent's 1", got)
	}
}

// What the executor reads of a claimed run includes whether it answers a
// channel. Dropping it would run every channel run as a dashboard one: no
// history, no continuations, no reply.
func TestAClaimedRunKeepsItsChannelColumns(t *testing.T) {
	t.Parallel()

	m := uuid.New()
	got := runOf(db.ClaimRunRow{ID: uuid.New(), MessageID: &m, ReplyToChannel: true})
	if !got.ReplyToChannel || got.MessageID == nil || *got.MessageID != m {
		t.Errorf("runOf = %+v, want reply_to_channel and the message kept", got)
	}
}

// The history is the conversation's most recent stretch, without gaps: a turn
// too long to fit ends it, rather than being skipped for older ones that
// would leave the model reading a conversation with a hole in it.
func TestHistoryStopsAtTheFirstTurnThatDoesNotFit(t *testing.T) {
	t.Parallel()

	got := withHistory([]db.ListSessionTurnsRow{
		said("message", "RECENT-TURN", nil),
		said("message", "HUGE-TURN "+strings.Repeat("x", historyBytes), nil),
		said("message", "EARLY-TURN", nil),
	}, "q")
	if !strings.Contains(got, "RECENT-TURN") || strings.Contains(got, "EARLY-TURN") {
		t.Errorf("history = %q, want the recent turn only, stopping at the one that does not fit", got)
	}
}
