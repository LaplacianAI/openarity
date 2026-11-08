package gateway

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// answering is an Inbox over a fake store whose enqueuer and log the test can
// read afterwards.
type answering struct {
	store *inboxStore
	runs  *enqueued
	log   *bytes.Buffer
	inbox *Inbox
	ch    Channel
}

func newAnswering() *answering {
	store := newInboxStore()
	runs := &enqueued{store: store}
	log := &bytes.Buffer{}
	return &answering{
		store: store, runs: runs, log: log,
		inbox: NewInbox(store, runs, slog.New(slog.NewTextHandler(log, nil))),
		ch:    Channel{ID: uuid.New(), TeamID: uuid.New()},
	}
}

func (a *answering) deliver(t *testing.T, msgs ...Delivery) error {
	t.Helper()
	return a.inbox.Deliver(t.Context(), a.ch, msgs)
}

func inGroup(d Delivery, mentionsUs bool) Delivery {
	d.Session = Session{Ref: "room-1", Kind: SessionGroup}
	if mentionsUs {
		d.Mentions = []Mention{{SenderRef: "B-us", DisplayName: "openarity", IsUs: true}}
	}
	return d
}

// A direct message is the team's to answer: one hub run, for this message and
// this sender, handed to the workers once it is committed and not before.
func TestADirectMessageStartsOneRunAfterItIsStored(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := delivery("m-1", "why did the export fail?")
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(a.store.created) != 1 {
		t.Fatalf("started %d runs, want 1", len(a.store.created))
	}
	run := a.store.created[0]
	if run.Input != d.Text || run.UserID == nil || *run.UserID != d.UserID ||
		run.MessageID == nil || *run.MessageID != a.store.ids[a.ch.ID.String()+"/m-1"] || run.TeamID != a.ch.TeamID {
		t.Errorf("run = %+v, want the message's text, sender, id and team", run)
	}
	if len(a.runs.runs) != 1 || a.runs.runs[0] != run.ID {
		t.Errorf("enqueued %v, want [%s]", a.runs.runs, run.ID)
	}
	if a.runs.early {
		t.Error("the run was handed to the workers before its transaction committed")
	}
}

// A provider retries what it did not see answered. The second delivery is the
// same message, and starts nothing.
func TestARedeliveryStartsNoSecondRun(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := delivery("m-1", "hello")
	for range 2 {
		if err := a.deliver(t, d); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}
	if len(a.store.created) != 1 || len(a.runs.runs) != 1 {
		t.Errorf("started %d runs and enqueued %d, want 1 and 1", len(a.store.created), len(a.runs.runs))
	}
	if got := a.store.steers[a.store.created[0].ID]; len(got) != 0 {
		t.Errorf("the redelivery was steered into the run as %q; it is the message the run already has", got)
	}
}

// A bot's message never starts a run, even a bot someone bound: two bots that
// answer each other would run until the budget ran out.
func TestABotsMessageIsNeverAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := delivery("m-1", "beep")
	d.Author.IsBot = true
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 || len(a.runs.runs) != 0 {
		t.Errorf("a bot's message started %d runs", len(a.store.created))
	}
	if len(a.store.stored) != 1 {
		t.Errorf("stored %d messages, want the bot's one: it is part of the conversation", len(a.store.stored))
	}
}

// In a group, people talk to each other. Only a message that mentions us asks
// the team anything.
func TestAGroupMessageIsAnsweredOnlyWhenItMentionsUs(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, inGroup(delivery("m-1", "lunch?"), false)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Fatalf("a group message that mentions nobody started %d runs", len(a.store.created))
	}
	if strings.Contains(a.log.String(), "not answered") {
		t.Error("a message that asked nothing was logged as unanswered")
	}

	if err := a.deliver(t, inGroup(delivery("m-2", "@openarity why did the export fail?"), true)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 {
		t.Errorf("a group message that mentions us started %d runs, want 1", len(a.store.created))
	}
}

// A mention of somebody else in a group is still people talking.
func TestMentioningSomeoneElseIsNotAskingUs(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := inGroup(delivery("m-1", "@bala can you look?"), false)
	d.Mentions = []Mention{{SenderRef: "U-bala", DisplayName: "bala"}}
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("mentioning someone else started %d runs", len(a.store.created))
	}
}

// The kind is the session's, fixed by its first message: a later message
// claiming the group is a direct conversation does not make every line in it
// a question for the team.
func TestTheSessionsKindDecidesNotTheMessages(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, inGroup(delivery("m-1", "morning"), false)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	claims := delivery("m-2", "answer me")
	claims.Session = Session{Ref: "room-1", Kind: SessionDirect}
	if err := a.deliver(t, claims); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("a message claiming the group was direct started %d runs", len(a.store.created))
	}
}

// Approval bound the sender to a member once; starting a run asks again. A
// sender whose member may no longer start runs is stored and not answered,
// and the log says why.
func TestASenderWhoMayNotStartRunsIsNotAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.mayNot = true
	if err := a.deliver(t, delivery("m-1", "hello")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("started %d runs for a sender who may not", len(a.store.created))
	}
	if !strings.Contains(a.log.String(), "may not start runs") {
		t.Errorf("log = %q, want why the message was not answered", a.log)
	}
}

// A run that would fail for want of a model is not started at all; the log
// says what to fix instead.
func TestAMessageToATeamWithNoModelIsNotAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.noModel = true
	if err := a.deliver(t, delivery("m-1", "hello")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("started %d runs for a team with no model", len(a.store.created))
	}
	if !strings.Contains(a.log.String(), "no model set") {
		t.Errorf("log = %q, want the missing model named", a.log)
	}
}

// A message that is only a file has no question in it, and a run needs input.
func TestAMessageWithNoTextIsNotAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := delivery("m-1", "  \n ")
	d.Files = []Stored{{ObjectKey: "teams/x/objects/1", MediaType: "image/png", SizeBytes: 1, Filename: "a.png"}}
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("started %d runs for a message with no text", len(a.store.created))
	}
}

// A message for a conversation the team is already answering goes to that run,
// with who said it, rather than starting a second run alongside it.
func TestAMessageMidRunIsSteeredIntoIt(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, delivery("m-1", "why did the export fail?")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	running := a.store.created[0].ID

	if err := a.deliver(t, delivery("m-2", "it was the nightly one")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 || len(a.runs.runs) != 1 {
		t.Errorf("started %d runs and enqueued %d, want the one already running", len(a.store.created), len(a.runs.runs))
	}
	if got := a.store.steers[running]; len(got) != 1 || got[0] != "Asha: it was the nightly one" {
		t.Errorf("steers = %q, want the message with its sender's name", got)
	}
}

// A steer names its sender by ref when the provider gave no display name.
func TestASteerFallsBackToTheSendersRef(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, delivery("m-1", "first")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	d := delivery("m-2", "second")
	d.Author.DisplayName = ""
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := a.store.steers[a.store.created[0].ID]; len(got) != 1 || got[0] != "u-1: second" {
		t.Errorf("steers = %q, want the ref when there is no name", got)
	}
}

// Starting the run is part of storing the message: if it fails, neither is
// written, the provider gets a 500 and sends the message again. A message
// stored with no run would never be answered.
func TestAFailedRunStartLeavesNoMessage(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.runErr = errors.New("connection reset")
	if err := a.deliver(t, delivery("m-1", "hello")); err == nil {
		t.Fatal("Deliver succeeded although the run could not be started")
	}
	if len(a.store.stored) != 0 || len(a.runs.runs) != 0 {
		t.Errorf("stored %d messages and enqueued %d runs after the run failed to start", len(a.store.stored), len(a.runs.runs))
	}
}

// The workers being unreachable does not lose the run: it is queued in
// Postgres and the sweep starts it. The delivery succeeds and the log says so.
func TestARunTheWorkersMissedIsStillStored(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.runs.err = errors.New("dbos unavailable")
	if err := a.deliver(t, delivery("m-1", "hello")); err != nil {
		t.Fatalf("Deliver = %v, want success: the run is stored and the sweep will start it", err)
	}
	if len(a.store.created) != 1 {
		t.Errorf("started %d runs, want 1", len(a.store.created))
	}
	if !strings.Contains(a.log.String(), "the sweep will start it") {
		t.Errorf("log = %q, want the missed hand-over named", a.log)
	}
}
