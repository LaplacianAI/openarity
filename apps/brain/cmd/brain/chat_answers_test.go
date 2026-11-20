package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A conversation in a channel with a run waiting in it, against Postgres:
// what a person answering in the chat meets.
type chatWorld struct {
	*runWorld
	channel gateway.Channel
	session uuid.UUID
	run     uuid.UUID
	runs    *resumes
}

type resumes struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (r *resumes) Enqueue(context.Context, uuid.UUID) error { return nil }

func (r *resumes) Resume(_ context.Context, id uuid.UUID, _ int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func (r *resumes) got() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.ids...)
}

// newChatWorld is a direct conversation with the world's user, of the given
// role, whose run waits.
func newChatWorld(t *testing.T, role string) *chatWorld {
	t.Helper()
	w := &chatWorld{runWorld: newRunWorld(t), runs: &resumes{}}
	replies := "https://replies.example/support"
	ch, err := w.store.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: w.team, Provider: "custom", Name: "support", ReplyUrl: &replies,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	w.channel = gateway.Channel{ID: ch.ID, TeamID: w.team}
	w.member(t, w.user, role)
	if err := w.raw.QueryRow(t.Context(), `
		INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id)
		VALUES ($1, $2, 'c-1', 'direct', $3) RETURNING id`, w.team, ch.ID, w.user).Scan(&w.session); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	w.run = uuid.New()
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, hub, reply_to_channel, user_id, status, waiting_since)
		VALUES ($1, $2, $3, $1, 'hub', 'open an issue about the disk', true, true, $4, 'waiting', now())`,
		w.run, w.team, w.session, w.user); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return w
}

func (w *chatWorld) member(t *testing.T, user uuid.UUID, role string) {
	t.Helper()
	if _, err := w.raw.Exec(t.Context(),
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`, w.team, user, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

func (w *chatWorld) someone(t *testing.T, role string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('https://id.test', $1) RETURNING id`, uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	w.member(t, id, role)
	return id
}

// ask is a request of the waiting run, made for the world's user.
func (w *chatWorld) ask(t *testing.T, call, code string, expires time.Duration, requireOther bool) db.Approval {
	t.Helper()
	a, err := w.store.InsertApproval(t.Context(), db.InsertApprovalParams{
		TeamID: w.team, RunID: w.run, RootRunID: w.run, CallID: call, Tool: "github__create_issue", Class: "write",
		Arguments: []byte(`{}`), Summary: "title: Disk full", Code: code, RequestedFor: &w.user,
		ExpiresAt: time.Now().Add(expires), RequireOther: requireOther,
	})
	if err != nil {
		t.Fatalf("InsertApproval: %v", err)
	}
	return a
}

// say delivers a message, or a tap, from a person in the conversation.
func (w *chatWorld) say(t *testing.T, from uuid.UUID, id, text string, answer *gateway.Answer) {
	t.Helper()
	d := gateway.Delivery{
		UserID: from,
		Inbound: gateway.Inbound{
			ExternalID: id, Author: gateway.Author{Ref: "u-" + from.String()[:8]},
			Session: gateway.Session{Ref: "c-1", Kind: gateway.SessionDirect}, Text: text, Answer: answer,
		},
	}
	if err := gateway.NewInbox(inbox{w.store.Store}, w.runs, discardLogger()).Deliver(t.Context(), w.channel, []gateway.Delivery{d}); err != nil {
		t.Fatalf("Deliver %q: %v", text, err)
	}
}

func (w *chatWorld) request(t *testing.T, a db.Approval) db.Approval {
	t.Helper()
	got, err := w.store.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: w.team})
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	return got
}

type notice struct {
	text   string
	detail gateway.ReplyNotice
}

func (w *chatWorld) notices(t *testing.T) []notice {
	t.Helper()
	rows, err := w.raw.Query(t.Context(), `SELECT text, detail FROM replies WHERE kind = 'notice' ORDER BY id`)
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		var detail []byte
		if err := rows.Scan(&n.text, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &n.detail); err != nil {
				t.Fatalf("detail %s: %v", detail, err)
			}
		}
		out = append(out, n)
	}
	return out
}

// asks is whether the message stored under this id asks a run anything.
func (w *chatWorld) asks(t *testing.T, id string) bool {
	t.Helper()
	var asks bool
	if err := w.raw.QueryRow(t.Context(), `SELECT asks FROM messages WHERE external_id = $1`, id).Scan(&asks); err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	return asks
}

func (w *chatWorld) runStatus(t *testing.T) string {
	t.Helper()
	var s string
	if err := w.raw.QueryRow(t.Context(), `SELECT status FROM runs WHERE id = $1`, w.run).Scan(&s); err != nil {
		t.Fatalf("read run: %v", err)
	}
	return s
}

// A yes with its code from the person a request is for, who may approve their
// own, decides it in the chat: approved by them, via the channel, the run
// queued again and handed back, one notice, and the message never a steer.
func TestAYesInTheChatApprovesTheRequestAndResumesTheRun(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes 7K2F", nil)

	got := w.request(t, a)
	if got.Status != "approved" || got.DecidedBy == nil || *got.DecidedBy != w.user ||
		got.DecidedVia == nil || *got.DecidedVia != "channel" || got.DecisionScope == nil || *got.DecisionScope != "once" {
		t.Fatalf("the request is %s by %v via %v (%v), want approved once by the person via the channel",
			got.Status, got.DecidedBy, got.DecidedVia, got.DecisionScope)
	}
	if s := w.runStatus(t); s != "queued" {
		t.Errorf("the run is %s, want queued again", s)
	}
	if r := w.runs.got(); len(r) != 1 || r[0] != w.run {
		t.Errorf("handed back %v, want [%s]", r, w.run)
	}
	if n := w.notices(t); len(n) != 1 || n[0].text != "Approved: create_issue on github." {
		t.Errorf("notices %+v", n)
	}
	if w.asks(t, "m-1") {
		t.Error("the yes asks a run: it would be a steer or the next run's input")
	}
	var events int
	if err := w.raw.QueryRow(t.Context(),
		`SELECT count(*) FROM run_events WHERE run_id = $1 AND kind = 'approval_decided'`, w.run).Scan(&events); err != nil || events != 1 {
		t.Errorf("%d approval_decided events, %v; want 1", events, err)
	}
}

// A custom channel with no reply URL takes messages in and has nowhere to
// post back to: a yes from it still answers, and no notice is written that
// could only be given up.
func TestAYesFromAChannelWithNowhereToReplyAnswersSilently(t *testing.T) {
	w := newChatWorld(t, "member")
	if _, err := w.raw.Exec(t.Context(), `UPDATE channels SET reply_url = NULL WHERE id = $1`, w.channel.ID); err != nil {
		t.Fatalf("clear the reply URL: %v", err)
	}
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes 7K2F", nil)

	if got := w.request(t, a); got.Status != "approved" {
		t.Fatalf("the request is %s, want approved", got.Status)
	}
	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v, want none for a channel with nowhere to post", n)
	}
	if w.asks(t, "m-1") {
		t.Error("the yes asks a run")
	}
}

// A bare yes names no request, with two waiting as with one: the codes come
// back and nothing is decided. The code then decides the one it names.
func TestABareYesWithTwoWaitingAsksWhichAndTheCodeDecides(t *testing.T) {
	w := newChatWorld(t, "member")
	first, second := w.ask(t, "c1", "7K2F", time.Hour, false), w.ask(t, "c2", "9ABC", time.Hour, false)

	w.say(t, w.user, "m-1", "y", nil)
	if n := w.notices(t); len(n) != 1 || !strings.Contains(n[0].text, "yes 7K2F") || !strings.Contains(n[0].text, "yes 9ABC") {
		t.Fatalf("notices %+v, want the codes", n)
	}
	if w.request(t, first).Status != "pending" || w.request(t, second).Status != "pending" {
		t.Fatal("a bare yes with two waiting decided something")
	}

	w.say(t, w.user, "m-2", "no 9abc", nil)
	if got := w.request(t, second); got.Status != "denied" {
		t.Errorf("the request the code names is %s, want denied", got.Status)
	}
	if got := w.request(t, first); got.Status != "pending" {
		t.Errorf("the other is %s, want still pending", got.Status)
	}
	if s := w.runStatus(t); s != "waiting" {
		t.Errorf("the run is %s while a request still waits, want waiting", s)
	}
}

// A guest may not approve their own: their yes is told it waits for an
// approver, and it gives up nothing, as writing again would.
func TestAGuestsYesIsToldItWaitsForAnApprover(t *testing.T) {
	w := newChatWorld(t, "guest")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes 7K2F", nil)

	if got := w.request(t, a); got.Status != "pending" {
		t.Errorf("the request is %s, want pending: a guest's yes decided or gave it up", got.Status)
	}
	if n := w.notices(t); len(n) != 1 || !strings.Contains(n[0].text, "waits for an approver") {
		t.Errorf("notices %+v", n)
	}
	if w.asks(t, "m-1") {
		t.Error("the guest's yes asks a run")
	}
}

// A code that names nothing waiting decides nothing: an old yes cannot
// approve a newer request, and is told where the old one stands.
func TestAnOldCodeCannotApproveANewRequest(t *testing.T) {
	w := newChatWorld(t, "member")
	old := w.ask(t, "c1", "PQR2", time.Hour, false)
	w.say(t, w.user, "m-1", "yes PQR2", nil)
	if w.request(t, old).Status != "approved" {
		t.Fatal("the first request was not approved")
	}
	if _, err := w.raw.Exec(t.Context(), `UPDATE runs SET status = 'waiting', waiting_since = now() WHERE id = $1`, w.run); err != nil {
		t.Fatal(err)
	}
	fresh := w.ask(t, "c2", "XYZ3", time.Hour, false)

	w.say(t, w.user, "m-2", "yes PQR2", nil)

	if got := w.request(t, fresh); got.Status != "pending" {
		t.Errorf("the new request is %s after an old code", got.Status)
	}
	if n := w.notices(t); len(n) != 2 || n[1].text != "PQR2 is no longer waiting. Approved: create_issue on github." {
		t.Errorf("notices %+v", n)
	}
}

// Nothing waiting for the sender: yes is just what they said.
func TestAYesWithNothingWaitingIsAnOrdinaryMessage(t *testing.T) {
	w := newChatWorld(t, "member")

	w.say(t, w.user, "m-1", "yes", nil)

	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v for a yes with nothing waiting", n)
	}
	if !w.asks(t, "m-1") {
		t.Error("a yes with nothing waiting does not ask anything")
	}
}

// "yes 7K2F" inside a sentence is a sentence: what the person wrote, read as
// writing again.
func TestACodeInsideASentenceIsNotAnAnswer(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "sure, yes 7K2F but change the title", nil)

	if got := w.request(t, a); got.Status != "cancelled" {
		t.Errorf("the request is %s, want given up by writing again", got.Status)
	}
	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v for a sentence", n)
	}
}

// Someone else's tap on the buttons is told privately and changes nothing,
// even an approver's: the chat decides only its person's requests.
func TestSomeoneElsesTapChangesNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	approver := w.someone(t, "approver")

	w.say(t, approver, "tap-1", "Approve", &gateway.Answer{
		ApprovalID: a.ID.String(), Decision: gateway.Approve, Handle: json.RawMessage(`{"h":1}`),
	})

	if got := w.request(t, a); got.Status != "pending" {
		t.Errorf("the request is %s after someone else's tap", got.Status)
	}
	n := w.notices(t)
	var handle struct{ H int }
	if len(n) == 1 {
		_ = json.Unmarshal(n[0].detail.Handle, &handle)
	}
	if len(n) != 1 || !n[0].detail.Private || handle.H != 1 {
		t.Errorf("notices %+v, want one private word where the tap was made", n)
	}
}

// A tap names its request by id, and settles it where it was tapped.
func TestATapApprovesForTheRunAndSettlesThePrompt(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "tap-1", "Approve for this run", &gateway.Answer{
		ApprovalID: a.ID.String(), Decision: gateway.ApproveRun, Handle: json.RawMessage(`{"h":2}`),
	})

	got := w.request(t, a)
	if got.Status != "approved" || got.DecisionScope == nil || *got.DecisionScope != "run" {
		t.Fatalf("the request is %s (%v), want approved for the run", got.Status, got.DecisionScope)
	}
	n := w.notices(t)
	if len(n) != 1 || !n[0].detail.Settled || n[0].detail.Private || n[0].text != "Approved for this run: create_issue on github." {
		t.Errorf("notices %+v", n)
	}
}

// A poll's vote names its request by the hash of the option chosen.
func TestAPollVoteDeclines(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	sum := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[1]))

	w.say(t, w.user, "vote-1", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{sum[:]}})

	if got := w.request(t, a); got.Status != "denied" {
		t.Errorf("the request is %s after a vote to decline", got.Status)
	}
}

// Decline with a reason opens the form first: nothing is decided until it is
// sent, and then the reason is the request's.
func TestADeclineWithAReasonAsksForItFirst(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "tap-1", "Decline…", &gateway.Answer{ApprovalID: a.ID.String(), Decision: gateway.DeclineAsk})
	if got := w.request(t, a); got.Status != "pending" {
		t.Fatalf("the request is %s before the reason was given", got.Status)
	}
	if n := w.notices(t); len(n) != 1 || n[0].detail.Form == nil || n[0].detail.Form.ID != a.ID.String() {
		t.Fatalf("notices %+v, want a form for the request", n)
	}

	w.say(t, w.user, "modal-1", "Decline", &gateway.Answer{
		ApprovalID: a.ID.String(), Decision: gateway.Decline, Reason: "  not that repo  ",
	})
	got := w.request(t, a)
	if got.Status != "denied" || got.Reason == nil || *got.Reason != "not that repo" {
		t.Errorf("the request is %s with reason %v", got.Status, got.Reason)
	}
}

// A request past its time is marked expired, as a late answer from anywhere
// is, and the run resumes with that.
func TestALateYesFindsTheRequestExpired(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", -time.Minute, false)

	w.say(t, w.user, "m-1", "yes 7K2F", nil)

	got := w.request(t, a)
	if got.Status != "expired" || got.DecidedBy != nil || got.DecidedVia == nil || *got.DecidedVia != "sweep" {
		t.Errorf("the request is %s by %v via %v, want expired by the brain", got.Status, got.DecidedBy, got.DecidedVia)
	}
	if s := w.runStatus(t); s != "queued" {
		t.Errorf("the run is %s, want queued to carry on without it", s)
	}
	if n := w.notices(t); len(n) != 1 || !strings.Contains(n[0].text, "expired") {
		t.Errorf("notices %+v", n)
	}
}

// A platform that delivers a tap twice is answered once.
func TestATapDeliveredTwiceIsAnsweredOnce(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	tap := &gateway.Answer{ApprovalID: a.ID.String(), Decision: gateway.Approve}

	w.say(t, w.user, "tap-1", "Approve", tap)
	w.say(t, w.user, "tap-1", "Approve", tap)

	if n := w.notices(t); len(n) != 1 {
		t.Errorf("%d notices for one tap delivered twice", len(n))
	}
	if r := w.runs.got(); len(r) != 1 {
		t.Errorf("handed back %d times", len(r))
	}
}

// A tap on a prompt whose request is over is told so, and settled.
func TestATapOnAnAnsweredRequestSaysItIsOver(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	w.say(t, w.user, "m-1", "no 7K2F", nil)

	w.say(t, w.user, "tap-1", "Approve", &gateway.Answer{ApprovalID: a.ID.String(), Decision: gateway.Approve})

	if got := w.request(t, a); got.Status != "denied" {
		t.Errorf("the request is %s, want still denied", got.Status)
	}
	n := w.notices(t)
	if len(n) != 2 || !n[1].detail.Settled || n[1].text != "Declined: create_issue on github." {
		t.Errorf("notices %+v", n)
	}
	if stored, answer := w.stored(t, "tap-1"); !stored || !answer {
		t.Errorf("the tap told it was over: stored %v, kept as an answer %v; want both, out of the conversation", stored, answer)
	}
}

// A yes followed by a word that is no code of the sender's ("yes sure") is
// what they wrote, read as writing again, never "no such code".
func TestAYesAndAWordThatIsNoCodeIsWhatTheyWrote(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes sure", nil)

	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v for a yes that names no code", n)
	}
	if got := w.request(t, a); got.Status != "cancelled" {
		t.Errorf("the request is %s, want given up by writing again", got.Status)
	}
	if !w.asks(t, "m-1") {
		t.Error("the message asks nothing: it was swallowed")
	}
}

// Another person typing someone's code learns nothing of it and decides
// nothing: a code names only the sender's own requests.
func TestSomeoneElseTypingACodeDecidesNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	other := w.someone(t, "approver")

	w.say(t, other, "m-1", "yes 7K2F", nil)

	if got := w.request(t, a); got.Status != "pending" {
		t.Errorf("the request is %s after someone else typed its code", got.Status)
	}
	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v, want nothing said of another's request", n)
	}
}

// In a group the footer asks for "yes 7K2F", so that answers without a
// mention; a bare yes to nobody in particular is not an answer.
func TestInAGroupACodeAnswersWithoutAMentionAndABareYesDoesNot(t *testing.T) {
	w := newChatWorld(t, "member")
	if _, err := w.raw.Exec(t.Context(), `UPDATE sessions SET kind = 'group', user_id = NULL WHERE id = $1`, w.session); err != nil {
		t.Fatalf("make the conversation a group: %v", err)
	}
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes", nil)
	if got := w.request(t, a); got.Status != "pending" {
		t.Fatalf("a bare yes to nobody in a group left the request %s", got.Status)
	}
	if n := w.notices(t); len(n) != 0 {
		t.Fatalf("notices %+v for a bare yes in a group", n)
	}

	w.say(t, w.user, "m-2", "yes 7K2F", nil)
	if got := w.request(t, a); got.Status != "approved" {
		t.Errorf("the request is %s after yes 7K2F in the group, want approved", got.Status)
	}
	if stored, answer := w.stored(t, "m-2"); !stored || !answer {
		t.Errorf("yes 7K2F without a mention: stored %v, as an answer %v; want both", stored, answer)
	}
	if w.asks(t, "m-2") {
		t.Error("the answer asks a run")
	}
}

// A tap naming someone else's answered request is told nothing of it: not
// its tool, not how it was answered.
func TestATapOnSomeoneElsesAnsweredRequestLearnsNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	w.say(t, w.user, "m-1", "no 7K2F", nil)
	other := w.someone(t, "member")

	w.say(t, other, "tap-1", "Approve", &gateway.Answer{ApprovalID: a.ID.String(), Decision: gateway.Approve})

	if n := w.notices(t); len(n) != 1 {
		t.Errorf("notices %+v, want only the owner's own decline", n)
	}
	if stored, _ := w.stored(t, "tap-1"); stored {
		t.Error("a tap that acts on nothing of its sender's was stored in the conversation")
	}
}

// A typed code answers without a mention only from a person writing now:
// never a bot's message, nor one read back from before the bot was there.
func TestACodeFromABotOrFromBeforeTheBotAnswersNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	if _, err := w.raw.Exec(t.Context(), `UPDATE sessions SET kind = 'group', user_id = NULL WHERE id = $1`, w.session); err != nil {
		t.Fatalf("make the conversation a group: %v", err)
	}
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	for i, in := range []gateway.Inbound{
		{Author: gateway.Author{Ref: "bot", IsBot: true}},
		{Author: gateway.Author{Ref: "u-1"}, Historical: true},
	} {
		in.ExternalID, in.Text = fmt.Sprintf("m-%d", i), "yes 7K2F"
		in.Session = gateway.Session{Ref: "c-1", Kind: gateway.SessionGroup}
		d := gateway.Delivery{UserID: w.user, Inbound: in}
		if err := gateway.NewInbox(inbox{w.store.Store}, w.runs, discardLogger()).Deliver(t.Context(), w.channel, []gateway.Delivery{d}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}
	if got := w.request(t, a); got.Status != "pending" {
		t.Errorf("the request is %s after a bot's or an old yes 7K2F", got.Status)
	}
}

// A typed code is read only where the channel answers at all: a channel set
// to answer in direct chats only decides nothing said in a group.
func TestACodeInAGroupTheChannelDoesNotAnswerDecidesNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	if _, err := w.raw.Exec(t.Context(), `UPDATE sessions SET kind = 'group', user_id = NULL WHERE id = $1`, w.session); err != nil {
		t.Fatalf("make the conversation a group: %v", err)
	}
	w.channel.AnswersIn = gateway.AnswersInDirect
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes 7K2F", nil)

	if got := w.request(t, a); got.Status != "pending" {
		t.Errorf("the request is %s after yes 7K2F where the channel does not answer", got.Status)
	}
	if n := w.notices(t); len(n) != 0 {
		t.Errorf("notices %+v", n)
	}
}

// earlier is a finished run in the same conversation, before the waiting one,
// whose request with this code for this tool was answered as status says.
func (w *chatWorld) earlier(t *testing.T, code, tool, status string, ago time.Duration) {
	t.Helper()
	id := uuid.New()
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, hub, reply_to_channel, user_id,
		                  status, created_at, started_at, finished_at)
		VALUES ($1, $2, $3, $1, 'hub', 'an earlier ask', true, true, $4, 'succeeded', now() - $5::interval,
		        now() - $5::interval, now() - $5::interval)`,
		id, w.team, w.session, w.user, fmt.Sprintf("%d seconds", int(ago.Seconds()))); err != nil {
		t.Fatalf("insert an earlier run: %v", err)
	}
	a, err := w.store.InsertApproval(t.Context(), db.InsertApprovalParams{
		TeamID: w.team, RunID: id, RootRunID: id, CallID: "e1", Tool: tool, Class: "write",
		Arguments: []byte(`{}`), Code: code, RequestedFor: &w.user, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertApproval: %v", err)
	}
	via := "dashboard"
	if _, err := w.store.DecideApproval(t.Context(), db.DecideApprovalParams{
		ID: a.ID, TeamID: w.team, Status: status, DecidedBy: &w.user, DecidedVia: &via,
	}); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	if _, err := w.raw.Exec(t.Context(), `UPDATE approvals SET created_at = now() - $2::interval WHERE id = $1`,
		a.ID, fmt.Sprintf("%d seconds", int(ago.Seconds()))); err != nil {
		t.Fatalf("date the earlier request: %v", err)
	}
}

// A code comes round again across a conversation's runs. The one waiting is
// the one it answers, even when an answered one is newer; with none waiting,
// it names the newest answered.
func TestARepeatedCodeNamesTheWaitingRequestThenTheNewest(t *testing.T) {
	w := newChatWorld(t, "member")
	w.earlier(t, "7K2F", "mail__send", "denied", 2*time.Hour)
	w.earlier(t, "7K2F", "calendar__create_event", "approved", time.Hour)
	waiting := w.ask(t, "c1", "7K2F", time.Hour, false)
	if _, err := w.raw.Exec(t.Context(), `UPDATE approvals SET created_at = now() - interval '3 hours' WHERE id = $1`, waiting.ID); err != nil {
		t.Fatalf("date the waiting request: %v", err)
	}

	w.say(t, w.user, "m-1", "yes 7K2F", nil)
	if got := w.request(t, waiting); got.Status != "approved" {
		t.Fatalf("the waiting request is %s, want approved by its code", got.Status)
	}

	w.say(t, w.user, "m-2", "no 7K2F", nil)
	n := w.notices(t)
	if len(n) != 2 || n[1].text != "7K2F is no longer waiting. Approved: create_event on calendar." {
		t.Errorf("notices %+v, want the newest answered named", n)
	}
}

// stored is whether a message was kept under this id, and whether it was kept
// as an answer, out of the conversation.
func (w *chatWorld) stored(t *testing.T, id string) (stored, answer bool) {
	t.Helper()
	err := w.raw.QueryRow(t.Context(), `SELECT answer FROM messages WHERE external_id = $1`, id).Scan(&answer)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false
	}
	if err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	return true, answer
}

// A vote changed after it decided its request is refused before it is
// stored: WhatsApp cannot take a poll back, and the decision is final. It
// changes nothing, says nothing, hands nothing back, and never reaches the
// conversation, the next run's input or the session's reads.
func TestAPollVoteChangedAfterItDecidedIsNotStored(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	yes := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[0]))
	no := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[1]))

	w.say(t, w.user, "vote-1", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{yes[:]}})
	w.say(t, w.user, "vote-2", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{no[:]}})
	w.say(t, w.user, "vote-3", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{yes[:]}})

	if got := w.request(t, a); got.Status != "approved" {
		t.Errorf("the request is %s after its vote was changed, want still approved", got.Status)
	}
	if stored, answer := w.stored(t, "vote-1"); !stored || !answer {
		t.Errorf("the deciding vote: stored %v, as an answer %v; want both", stored, answer)
	}
	for _, id := range []string{"vote-2", "vote-3"} {
		if stored, _ := w.stored(t, id); stored {
			t.Errorf("%s, a vote changed after the decision, was stored", id)
		}
	}
	if n := w.notices(t); len(n) != 1 {
		t.Errorf("notices %+v, want only the decision's", n)
	}
	if r := w.runs.got(); len(r) != 1 {
		t.Errorf("handed back %d times, want once", len(r))
	}
}

// An answer, tapped or typed, is the person acting on a request, not something
// said: the session's reads, its history and the next run's input leave it
// out. So does a typed answer to a request already decided, which is still
// told where the request stands. What is not an answer ("yes sure", a code
// that is not theirs) is what they wrote, and stays.
func TestAnAnswerTappedOrTypedIsKeptOutOfTheConversation(t *testing.T) {
	w := newChatWorld(t, "member")
	first := w.ask(t, "c1", "7K2F", time.Hour, false)
	w.ask(t, "c2", "9JD3", time.Hour, false)

	w.say(t, w.user, "tap-1", "Approve", &gateway.Answer{ApprovalID: first.ID.String(), Decision: gateway.Approve})
	w.say(t, w.user, "typed-1", "yes 9JD3", nil)
	w.say(t, w.user, "typed-2", "no 9JD3", nil)
	w.say(t, w.user, "said-1", "yes sure", nil)

	for id, want := range map[string]bool{"tap-1": true, "typed-1": true, "typed-2": true, "said-1": false} {
		if stored, answer := w.stored(t, id); !stored || answer != want {
			t.Errorf("%s: stored %v, as an answer %v; want stored, as an answer %v", id, stored, answer, want)
		}
	}
	if n := w.notices(t); len(n) != 3 || n[2].text != "9JD3 is no longer waiting. Approved: create_issue on github." {
		t.Errorf("notices %+v, want the change of mind told the request is over", n)
	}

	ctx := t.Context()
	texts := func(got []string) string { return strings.Join(got, " | ") }

	history, err := w.store.ListSessionHistory(ctx, db.ListSessionHistoryParams{SessionID: w.session, Before: 1 << 62, PageSize: 50})
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	var said []string
	for _, h := range history {
		said = append(said, h.Text)
	}
	if got := texts(said); got != "yes sure" {
		t.Errorf("history = %q, want only what was said", got)
	}

	since, err := w.store.ListMessagesSinceLastRun(ctx, db.ListMessagesSinceLastRunParams{
		SessionID: w.session, Before: time.Now().Add(time.Minute), RunID: uuid.New(), Turns: 50,
	})
	if err != nil {
		t.Fatalf("ListMessagesSinceLastRun: %v", err)
	}
	said = said[:0]
	for _, m := range since {
		said = append(said, m.Text)
	}
	if got := texts(said); got != "yes sure" {
		t.Errorf("the next run's input = %q, want only what was said", got)
	}

	events, err := w.store.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: w.session, PageSize: 50})
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	said = said[:0]
	for _, e := range events {
		if e.Kind == "message" {
			var m struct{ Text string }
			_ = json.Unmarshal(e.Payload, &m)
			said = append(said, m.Text)
		}
	}
	if got := texts(said); got != "yes sure" {
		t.Errorf("the session's stream = %q, want only what was said", got)
	}

	page, err := w.store.ListMessagesBySession(ctx, db.ListMessagesBySessionParams{SessionID: w.session, PageSize: 50})
	if err != nil {
		t.Fatalf("ListMessagesBySession: %v", err)
	}
	count, err := w.store.CountMessagesBySession(ctx, w.session)
	if err != nil {
		t.Fatalf("CountMessagesBySession: %v", err)
	}
	if len(page) != 1 || page[0].Text != "yes sure" || count != 1 {
		t.Errorf("the session's messages = %d (%d counted), want only what was said", len(page), count)
	}
}

// Only a code decides. A bare yes with one request waiting may answer the
// bot's own question rather than the request, so it decides nothing: it is
// told the code, kept out of the conversation, and gives up nothing, so the
// request still waits for its code.
func TestABareYesWithOneWaitingIsToldTheCodeAndDecidesNothing(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)

	w.say(t, w.user, "m-1", "yes", nil)

	if got := w.request(t, a); got.Status != "pending" {
		t.Fatalf("a bare yes left the request %s, want still pending", got.Status)
	}
	if s := w.runStatus(t); s != "waiting" {
		t.Errorf("the run is %s after a bare yes, want still waiting", s)
	}
	if n := w.notices(t); len(n) != 1 || n[0].text != "A request is waiting. Reply with the code: yes 7K2F (create_issue on github)." {
		t.Errorf("notices %+v, want the code to reply with", n)
	}
	if stored, answer := w.stored(t, "m-1"); !stored || !answer || w.asks(t, "m-1") {
		t.Errorf("the bare yes: stored %v, as an answer %v, asks %v; want an answer that asks nothing",
			stored, answer, w.asks(t, "m-1"))
	}

	w.say(t, w.user, "m-2", "yes 7K2F", nil)
	if got := w.request(t, a); got.Status != "approved" {
		t.Errorf("the request is %s after its code, want approved", got.Status)
	}
}

// A vote taken back, or a refused vote delivered again, names nothing: it is
// refused before it is stored, every time, and says nothing.
func TestAVoteTakenBackOrDeliveredAgainIsNotStored(t *testing.T) {
	w := newChatWorld(t, "member")
	a := w.ask(t, "c1", "7K2F", time.Hour, false)
	yes := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[0]))
	no := sha256.Sum256([]byte(gateway.PollOptions("7K2F")[1]))

	w.say(t, w.user, "vote-1", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{yes[:]}})
	w.say(t, w.user, "vote-2", "Voted in the poll", &gateway.Answer{Hashes: nil})
	w.say(t, w.user, "vote-3", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{no[:]}})
	w.say(t, w.user, "vote-3", "Voted in the poll", &gateway.Answer{Hashes: [][]byte{no[:]}})

	if got := w.request(t, a); got.Status != "approved" {
		t.Errorf("the request is %s, want still approved", got.Status)
	}
	for _, id := range []string{"vote-2", "vote-3"} {
		if stored, _ := w.stored(t, id); stored {
			t.Errorf("%s was stored", id)
		}
	}
	if n := w.notices(t); len(n) != 1 {
		t.Errorf("notices %+v, want only the decision's", n)
	}
}
