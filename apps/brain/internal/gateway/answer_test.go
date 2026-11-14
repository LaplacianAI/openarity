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

// A message of files alone asks that they be looked at, and a run can: its
// first message names them and its tools read them.
func TestAMessageOfFilesAloneIsAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	d := delivery("m-1", "  \n ")
	d.Files = []Stored{{ObjectKey: "teams/x/objects/1", MediaType: "image/png", SizeBytes: 1, Filename: "a.png"}}
	if err := a.deliver(t, d); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 || a.store.created[0].Input != "The message is the files sent with it." {
		t.Fatalf("started %+v", a.store.created)
	}
}

// With neither text nor a file there is nothing to answer.
func TestAMessageWithNothingInItIsNotAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, delivery("m-1", "  \n ")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("started %d runs for a message with nothing in it", len(a.store.created))
	}
}

// A steer is text alone, so the files sent with it are named in it, for the
// run to find with attachments().
func TestASteerNamesTheFilesSentWithIt(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, delivery("m-1", "why did the export fail?")); err != nil {
		t.Fatal(err)
	}
	running := a.store.created[0].ID
	d := delivery("m-2", "here is the log")
	d.Files = []Stored{{Filename: "export.log"}, {Filename: ""}}
	if err := a.deliver(t, d); err != nil {
		t.Fatal(err)
	}
	if got := a.store.steers[running]; len(got) != 1 || got[0] != "Asha: here is the log\n[sent with export.log, a file with no name]" {
		t.Fatalf("steers = %q", got)
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

func (a *answering) deliverOne(t *testing.T, d Delivery) Delivered {
	t.Helper()
	got, err := a.inbox.DeliverOne(t.Context(), a.ch, d)
	if err != nil {
		t.Fatalf("DeliverOne: %v", err)
	}
	return got
}

// A message that starts a run asks, and the run it started takes it at once:
// the stream shows it pending and taken before the run has begun.
func TestAMessageThatStartsARunIsTakenByIt(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	got := a.deliverOne(t, delivery("m-1", "why did the export fail?"))
	msg := a.store.ids[a.ch.ID.String()+"/m-1"]
	run := a.store.created[0].ID

	if got.Message != msg || got.Run != run || got.Steered != uuid.Nil || got.Session == uuid.Nil {
		t.Fatalf("delivered %+v, want message %s and run %s", got, msg, run)
	}
	if len(a.store.asked) != 1 || a.store.asked[0] != msg {
		t.Errorf("asked %v, want the message", a.store.asked)
	}
	if len(a.store.consumed) != 1 || a.store.consumed[0].RunID != run ||
		a.store.consumed[0].Only == nil || *a.store.consumed[0].Only != msg || a.store.consumed[0].FirstOnly {
		t.Errorf("consumed %+v, want run %s taking exactly the message", a.store.consumed, run)
	}
}

// A message sent while a run works asks, and waits: the run takes it when it
// reads the steer, which is the runtime's to record.
func TestASteeredMessageWaitsForTheRunToReadIt(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.deliverOne(t, delivery("m-1", "why did the export fail?"))
	running := a.store.created[0].ID

	got := a.deliverOne(t, delivery("m-2", "it was the nightly one"))
	msg := a.store.ids[a.ch.ID.String()+"/m-2"]
	if got.Steered != running || got.Run != uuid.Nil || got.Message != msg {
		t.Fatalf("delivered %+v, want it steered into %s", got, running)
	}
	if len(a.store.asked) != 2 || a.store.asked[1] != msg {
		t.Errorf("asked %v, want both messages", a.store.asked)
	}
	if len(a.store.consumed) != 1 {
		t.Errorf("consumed %+v, want only the first message taken now", a.store.consumed)
	}
}

// A message that asks nothing, or asks and gets no answer, is never pending:
// no run is coming for it.
func TestAMessageNoRunIsComingForIsNotPending(t *testing.T) {
	t.Parallel()

	for name, setup := range map[string]func(a *answering) Delivery{
		"said in a group to someone else": func(*answering) Delivery { return inGroup(delivery("m-1", "lunch?"), false) },
		"to a team with no model": func(a *answering) Delivery {
			a.store.noModel = true
			return delivery("m-1", "hello")
		},
		"from a member who may not start runs": func(a *answering) Delivery {
			a.store.mayNot = true
			return delivery("m-1", "hello")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newAnswering()
			got := a.deliverOne(t, setup(a))
			if got.Message == uuid.Nil || got.Run != uuid.Nil || got.Steered != uuid.Nil {
				t.Fatalf("delivered %+v", got)
			}
			if len(a.store.asked) != 0 || len(a.store.consumed) != 0 {
				t.Errorf("asked %v, consumed %v; want neither", a.store.asked, a.store.consumed)
			}
		})
	}
}

func TestDeliverOneSaysWhyAMessageGetsNoAnswer(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.noModel = true
	if got := a.deliverOne(t, delivery("m-1", "hello")); !strings.Contains(got.Unanswered, "no model") {
		t.Fatalf("unanswered %q", got.Unanswered)
	}
}

// A replay is stored once; DeliverOne says which conversation it is in and
// that it stored nothing new.
func TestDeliverOneOnAReplayStoresNothingNew(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	first := a.deliverOne(t, delivery("m-1", "hello"))
	again := a.deliverOne(t, delivery("m-1", "hello"))
	if again.Message != uuid.Nil || again.Run != uuid.Nil || again.Session != first.Session {
		t.Fatalf("again %+v, first %+v", again, first)
	}
	if len(a.runs.runs) != 1 {
		t.Errorf("enqueued %d runs, want 1", len(a.runs.runs))
	}
}

func TestDeliverOneToADeletedTeamIsRefused(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.deleted = true
	if _, err := a.inbox.DeliverOne(t.Context(), a.ch, delivery("m-1", "hello")); !errors.Is(err, ErrTeamDeleted) {
		t.Fatalf("err = %v, want ErrTeamDeleted", err)
	}
	if len(a.store.calls) != 0 {
		t.Errorf("stored %d messages for a deleted team", len(a.store.calls))
	}
}

func asBuilder(d Delivery) Delivery {
	d.Session.Builder = true
	return d
}

// The builder answers a conversation begun as its own: the session is made
// the builder's when it starts, and its message starts the run that answers
// it, which the store makes the builder's because the session is.
func TestABuilderConversationIsStartedAsTheBuilders(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	if err := a.deliver(t, asBuilder(delivery("m-1", "an agent for support"))); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := a.store.sessionCalls[0]; !got.Builder {
		t.Errorf("the session was ensured as %+v, want the builder's", got)
	}
	if len(a.store.created) != 1 {
		t.Fatalf("started %d runs, want 1", len(a.store.created))
	}
}

// The builder runs on the team's model and only on it. A team whose hub is an
// agent it chose can answer anywhere else, but not in a builder conversation,
// and it is told why.
func TestABuilderConversationOnATeamWithNoModelIsNotAnswered(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.store.noModel, a.store.hubAgent = true, true
	if err := a.deliver(t, asBuilder(delivery("m-1", "an agent for support"))); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Errorf("started %d runs with no model for the builder", len(a.store.created))
	}
	if !strings.Contains(a.log.String(), "its builder cannot run") {
		t.Errorf("log = %q, want the builder named", a.log)
	}

	// The same team answers a conversation the hub holds.
	hub := delivery("m-2", "hello")
	hub.Session.Ref = "c-2"
	if err := a.deliver(t, hub); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 {
		t.Errorf("started %d runs in the hub's conversation, want 1", len(a.store.created))
	}
}
