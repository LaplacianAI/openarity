package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// conversation is one team, one custom channel, one direct session in it, and
// a person bound to speak there.
type conversation struct {
	team    uuid.UUID
	channel db.Channel
	session db.Session
	user    uuid.UUID
}

func newConversation(t *testing.T, s *Store, name string) conversation {
	t.Helper()

	ch, team := sessionFixture(t, s, name)
	return conversation{
		team:    team,
		channel: ch,
		session: mustEnsureSession(t, s, team, ch.ID, "D-"+name, "direct"),
		user:    insertUser(t, s, "dev", name),
	}
}

func (c conversation) says(t *testing.T, s *Store, externalID, text string) uuid.UUID {
	t.Helper()
	return insertMessage(t, s, message(c.channel, c.session, c.user, externalID, text))
}

func (c conversation) startRun(t *testing.T, q *db.Queries, message *uuid.UUID, input string) (db.Run, error) {
	t.Helper()
	return q.CreateChannelRun(t.Context(), db.CreateChannelRunParams{
		ID: uuid.New(), TeamID: c.team, SessionID: c.session.ID, UserID: &c.user,
		Input: input, MessageID: message,
	})
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// A run started for a message is the team's hub, answering this conversation,
// for this message.
func TestAMessageStartsAHubRunThatAnswersItsConversation(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "asks")
	m := c.says(t, s, "m-1", "why did the export fail?")

	r, err := c.startRun(t, s.Queries, &m, "why did the export fail?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	if !r.Hub || !r.ReplyToChannel || r.MessageID == nil || *r.MessageID != m ||
		r.RootRunID != r.ID || r.Status != "queued" || r.UserID == nil || *r.UserID != c.user {
		t.Errorf("run = %+v, want a queued hub root that replies, for message %s by %s", r, m, c.user)
	}
}

// A provider retries any delivery it did not see answered, so the same message
// can arrive twice. The second run for it is refused by the database itself,
// whatever the caller forgot to check.
func TestAMessageStartsOneRunEvenWhenDeliveredTwice(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "twice")
	m := c.says(t, s, "m-1", "hello")

	if _, err := c.startRun(t, s.Queries, &m, "hello"); err != nil {
		t.Fatalf("first CreateChannelRun: %v", err)
	}
	if _, err := c.startRun(t, s.Queries, &m, "hello"); !isUniqueViolation(err) {
		t.Errorf("second CreateChannelRun err = %v, want a unique violation", err)
	}
}

// A run for steers that came too late has no message of its own, and there can
// be several of those in a conversation's life.
func TestARunWithNoMessageIsNotLimitedToOne(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "late")

	for i := range 2 {
		if _, err := c.startRun(t, s.Queries, nil, "and one more thing"); err != nil {
			t.Fatalf("CreateChannelRun %d with no message: %v", i, err)
		}
	}
}

// A message from another conversation would answer the wrong people.
func TestAChannelRunRefusesAMessageFromAnotherConversation(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "here")
	elsewhere := mustEnsureSession(t, s, c.team, c.channel.ID, "D-there", "direct")
	stray := insertMessage(t, s, message(c.channel, elsewhere, c.user, "m-x", "wrong room"))

	if _, err := c.startRun(t, s.Queries, &stray, "wrong room"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("CreateChannelRun err = %v, want pgx.ErrNoRows", err)
	}
}

// Only a root run answers a conversation; a delegate answers its parent.
func TestOnlyARootRunRepliesToAChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "roots")
	parent, err := c.startRun(t, s.Queries, nil, "plan it")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}

	child := uuid.New()
	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, parent_run_id, root_run_id, parent_tool_call,
		                  depth, agent_name, input, reply_to_channel)
		VALUES ($1, $2, $3, $4, $4, 'call-1', 1, 'helper', 'do part', true)`,
		child, c.team, c.session.ID, parent.ID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "runs_reply_from_a_root" {
		t.Errorf("inserting a replying child err = %v, want runs_reply_from_a_root", err)
	}
}

// The run a new message is steered into is the one answering the conversation
// now: queued or running, and started from the channel. A run someone started
// from the dashboard in the same session is not it, and a finished run is not.
func TestTheActiveChannelRunIsTheQueuedOrRunningOneThatReplies(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "active")
	agent := insertAgent(t, s, c.team, "triage")

	if _, err := s.ActiveChannelRun(t.Context(), c.session.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("with no run, err = %v, want pgx.ErrNoRows", err)
	}

	ranIn(t, s, c.team, c.session.ID, agent, "running", 5) // the dashboard's, not the channel's
	if _, err := s.ActiveChannelRun(t.Context(), c.session.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("with only a dashboard run, err = %v, want pgx.ErrNoRows", err)
	}

	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	got, err := s.ActiveChannelRun(t.Context(), c.session.ID)
	if err != nil || got != r.ID {
		t.Fatalf("ActiveChannelRun = %s, %v; want %s", got, err, r.ID)
	}

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	if _, err := s.ActiveChannelRun(t.Context(), c.session.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("after it finished, err = %v, want pgx.ErrNoRows", err)
	}
}

// Leftover steers come back oldest first, and only once: taking them again
// finds nothing, so a message is never answered by two runs.
func TestLeftoverSteersAreTakenOnceOldestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "steers")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	other, err := c.startRun(t, s.Queries, nil, "another")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}

	for _, text := range []string{"first", "second"} {
		if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: text}); err != nil {
			t.Fatalf("AddSteer(%q): %v", text, err)
		}
	}
	if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: other.ID, Text: "not yours"}); err != nil {
		t.Fatalf("AddSteer: %v", err)
	}

	got, err := s.TakeLeftoverSteers(t.Context(), r.ID)
	if err != nil || len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("TakeLeftoverSteers = %q, %v; want [first second]", got, err)
	}
	if again, err := s.TakeLeftoverSteers(t.Context(), r.ID); err != nil || len(again) != 0 {
		t.Errorf("taking again = %q, %v; want nothing", again, err)
	}
	if theirs, err := s.TakeLeftoverSteers(t.Context(), other.ID); err != nil || len(theirs) != 1 {
		t.Errorf("the other run's steers = %q, %v; want its one, untouched", theirs, err)
	}
}

// The reply answers the newest message of the conversation, which is the last
// one the run read. One reply per run, whoever tries to write a second.
func TestAReplyAnswersTheNewestMessageOncePerRun(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "reply")
	first := c.says(t, s, "m-1", "why did the export fail?")
	r, err := c.startRun(t, s.Queries, &first, "why did the export fail?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	newest := c.says(t, s, "m-2", "it was the nightly one")

	reply, err := s.InsertReply(t.Context(), db.InsertReplyParams{RunID: r.ID, Kind: "answer", Text: "It ran out of disk."})
	if err != nil {
		t.Fatalf("InsertReply: %v", err)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != newest || reply.ChannelID != c.channel.ID ||
		reply.SessionID != c.session.ID || reply.Status != "pending" || reply.Attempts != 0 {
		t.Errorf("reply = %+v, want pending, in reply to %s, on %s", reply, newest, c.channel.ID)
	}

	if _, err := s.InsertReply(t.Context(), db.InsertReplyParams{RunID: r.ID, Kind: "answer", Text: "again"}); !isUniqueViolation(err) {
		t.Errorf("second InsertReply err = %v, want a unique violation", err)
	}
}

// A run that was not started from the channel writes no reply, so a question
// asked from the dashboard never posts into somebody's conversation.
func TestOnlyAChannelRunWritesAReply(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "dashboard")
	agent := insertAgent(t, s, c.team, "triage")
	dash := ranIn(t, s, c.team, c.session.ID, agent, "succeeded", 1)

	if _, err := s.InsertReply(t.Context(), db.InsertReplyParams{RunID: dash, Kind: "answer", Text: "hi"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("InsertReply for a dashboard run err = %v, want pgx.ErrNoRows", err)
	}
}

// The hub can run when the team chose a hub agent that still exists, or set a
// model for the built-in orchestrator. The same rule the runtime fails on.
func TestTheHubCanRunWithAModelOrAHubAgent(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	can := func(team uuid.UUID) bool {
		t.Helper()
		ok, err := s.HubCanRun(t.Context(), team)
		if err != nil {
			t.Fatalf("HubCanRun: %v", err)
		}
		return ok
	}

	bare := mustCreate(t, s, "bare")
	if can(bare.ID) {
		t.Error("a team with no settings can run its hub")
	}

	noModel := mustCreate(t, s, "no-model")
	mustUpsertSettings(t, s, settings(noModel.ID))
	if can(noModel.ID) {
		t.Error("a team with settings but no model and no hub agent can run its hub")
	}

	modelled := mustCreate(t, s, "modelled")
	p := settings(modelled.ID)
	model, tokens := "gpt-x", int32(1024)
	p.ModelName, p.ModelMaxTokens = &model, &tokens
	mustUpsertSettings(t, s, p)
	if !can(modelled.ID) {
		t.Error("a team with a model cannot run its hub")
	}

	chosen := mustCreate(t, s, "chosen")
	agent := insertAgent(t, s, chosen.ID, "lead")
	setHub(t, s, chosen.ID, &agent)
	if !can(chosen.ID) {
		t.Error("a team with a hub agent cannot run its hub")
	}
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, agent); err != nil {
		t.Fatalf("deleting the hub agent: %v", err)
	}
	if can(chosen.ID) {
		t.Error("a team whose hub agent was deleted, with no model, can run its hub")
	}
}

// History is what was said before the run's message, newest first, with the
// answers sent back and without apologies, which tell the model nothing.
func TestSessionTurnsAreWhatWasSaidBeforeNewestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "history")

	first := c.says(t, s, "m-1", "is the export done?")
	r, err := c.startRun(t, s.Queries, &first, "is the export done?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	if _, err := s.InsertReply(t.Context(), db.InsertReplyParams{RunID: r.ID, Kind: "answer", Text: "Not yet."}); err != nil {
		t.Fatalf("InsertReply: %v", err)
	}
	failed, err := c.startRun(t, s.Queries, nil, "and now?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	if _, err := s.InsertReply(t.Context(), db.InsertReplyParams{RunID: failed.ID, Kind: "apology", Text: "Sorry."}); err != nil {
		t.Fatalf("InsertReply: %v", err)
	}
	c.says(t, s, "m-2", "and now?")
	// The database's clock, not this process's: received_at is stamped there,
	// and a container's clock can trail the host's by more than a statement.
	var cutoff time.Time
	if err := s.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		t.Fatalf("reading the database's clock: %v", err)
	}
	c.says(t, s, "m-3", "after the cutoff")

	turns, err := s.ListSessionTurns(t.Context(), db.ListSessionTurnsParams{
		SessionID: c.session.ID, Before: cutoff, Turns: 10,
	})
	if err != nil {
		t.Fatalf("ListSessionTurns: %v", err)
	}
	var got []string
	for _, turn := range turns {
		got = append(got, turn.Kind+": "+turn.Text)
	}
	want := []string{"message: and now?", "reply: Not yet.", "message: is the export done?"}
	if len(got) != len(want) {
		t.Fatalf("turns = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("turns = %q, want %q", got, want)
			break
		}
	}
	if turns[0].Author == nil || *turns[0].Author == "" {
		t.Error("a message's author is empty; the model cannot tell who said it")
	}

	limited, err := s.ListSessionTurns(t.Context(), db.ListSessionTurnsParams{
		SessionID: c.session.ID, Before: cutoff, Turns: 1,
	})
	if err != nil || len(limited) != 1 || limited[0].Text != "and now?" {
		t.Errorf("one turn = %+v, %v; want only the newest", limited, err)
	}
}

// waitForLock returns once some other connection is waiting on a lock while
// running a statement that contains marker. Without it, a race test can let
// one transaction finish before the other starts, and pass with the guard gone.
func waitForLock(t *testing.T, s *Store, marker string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := s.pool.QueryRow(t.Context(),
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%')`, marker).Scan(&waiting); err != nil {
			t.Fatalf("watching for the lock wait: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing waited on a lock running %q", marker)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A message that arrives while a run is finishing is steered into it or starts
// a run of its own, never neither. The finish here starts no follow-up, so
// nothing but its own session lock stops the message reading the run as still
// running and steering into it after the finish has looked for leftovers: a
// steer nobody will ever read. Whatever order the two land in, the steer must
// either be refused or be among the finish's leftovers.
func TestASteerIsNeverLostToAFinish(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "finishing")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}

	finish, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = finish.Rollback(t.Context()) }()
	fq := db.New(finish)
	if _, err := fq.LockSession(t.Context(), c.session.ID); err != nil {
		t.Fatalf("finish LockSession: %v", err)
	}
	if _, err := finish.Exec(t.Context(),
		`UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	// The finish looks for leftovers before it commits, as the runtime does;
	// a steer written after this is one only the session lock can save.
	left, err := fq.TakeLeftoverSteers(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("TakeLeftoverSteers: %v", err)
	}

	steered := make(chan error, 1)
	go func() {
		steered <- s.InTx(t.Context(), func(q *db.Queries) error {
			if _, err := q.LockSession(t.Context(), c.session.ID); err != nil {
				return err
			}
			id, err := q.ActiveChannelRun(t.Context(), c.session.ID)
			if err != nil {
				return err
			}
			return q.AddSteer(t.Context(), db.AddSteerParams{RunID: id, Text: "late"})
		})
	}()

	// Either the message is now waiting on the finish, or it got through and
	// committed. Both are let happen; which one is what the assertion judges.
	var result error
	arrived, done := false, false
	deadline := time.Now().Add(5 * time.Second)
	for !done {
		select {
		case result = <-steered:
			arrived, done = true, true
		default:
			var waiting bool
			if err := s.pool.QueryRow(t.Context(),
				`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				  WHERE wait_event_type = 'Lock' AND query LIKE '%FROM sessions WHERE id%')`).Scan(&waiting); err != nil {
				t.Fatalf("watching for the lock wait: %v", err)
			}
			if waiting {
				done = true
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the message neither waited nor finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if err := finish.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !arrived {
		result = <-steered
	}

	refused := errors.Is(result, pgx.ErrNoRows)
	taken := len(left) == 1 && left[0].Text == "late"
	if !refused && !taken {
		t.Errorf("the message steered into a finishing run (err %v) and the finish took %q: the steer is lost", result, left)
	}
}

// The other order: the message holds the session and is steering when the run
// finishes. The finish waits, then takes the steer as a leftover, so it is
// answered by the follow-up run instead of lying unread in agent_steers.
func TestAFinishWaitingOnASteerTakesItAsALeftover(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "steering")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}

	deliver, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = deliver.Rollback(t.Context()) }()
	dq := db.New(deliver)
	if _, err := dq.LockSession(t.Context(), c.session.ID); err != nil {
		t.Fatalf("deliver LockSession: %v", err)
	}
	id, err := dq.ActiveChannelRun(t.Context(), c.session.ID)
	if err != nil || id != r.ID {
		t.Fatalf("ActiveChannelRun = %s, %v; want %s", id, err, r.ID)
	}
	if err := dq.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: "late"}); err != nil {
		t.Fatalf("AddSteer: %v", err)
	}

	left := make(chan []db.TakeLeftoverSteersRow, 1)
	errs := make(chan error, 1)
	go func() {
		var got []db.TakeLeftoverSteersRow
		errs <- s.InTx(t.Context(), func(q *db.Queries) error {
			if _, err := q.LockSession(t.Context(), c.session.ID); err != nil {
				return err
			}
			var err error
			got, err = q.TakeLeftoverSteers(t.Context(), r.ID)
			return err
		})
		left <- got
	}()
	waitForLock(t, s, "FROM sessions WHERE id")

	if err := deliver.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := <-left; len(got) != 1 || got[0].Text != "late" {
		t.Errorf("leftovers = %q, want [late]: a steer written before the finish must reach it", got)
	}
}

// The race the session lock is there for. A finish that starts a follow-up run
// and a message arriving at the same moment must not leave two runs answering
// one conversation. The finish holds the session; the message waits on it,
// and when it goes on it sees the follow-up and steers into it.
func TestAFollowUpAndANewMessageNeverMakeTwoActiveRuns(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "follow-up")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}

	finish, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = finish.Rollback(t.Context()) }()
	fq := db.New(finish)
	if _, err := fq.LockSession(t.Context(), c.session.ID); err != nil {
		t.Fatalf("finish LockSession: %v", err)
	}
	if _, err := finish.Exec(t.Context(),
		`UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	followUp, err := c.startRun(t, fq, nil, "the steer that came too late")
	if err != nil {
		t.Fatalf("follow-up CreateChannelRun: %v", err)
	}

	steeredInto := make(chan uuid.UUID, 1)
	errs := make(chan error, 1)
	go func() {
		errs <- s.InTx(t.Context(), func(q *db.Queries) error {
			if _, err := q.LockSession(t.Context(), c.session.ID); err != nil {
				return err
			}
			id, err := q.ActiveChannelRun(t.Context(), c.session.ID)
			if err != nil {
				return err
			}
			steeredInto <- id
			return q.AddSteer(t.Context(), db.AddSteerParams{RunID: id, Text: "new"})
		})
	}()
	waitForLock(t, s, "FROM sessions WHERE id")

	if err := finish.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("the message's transaction: %v (it found no active run, so it would start a second)", err)
	}
	if got := <-steeredInto; got != followUp.ID {
		t.Errorf("steered into %s, want the follow-up %s", got, followUp.ID)
	}
}

// A sender's binding was checked once, at approval. Starting a run asks again:
// the member's role now has to grant it, in this team, and they have to still
// be a member at all.
func TestMemberMayAsksTheRoleTheyHoldNow(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "rights")
	other := mustCreate(t, s, "elsewhere")
	user := insertUser(t, s, "dev", "rights")
	may := func(team uuid.UUID) bool {
		t.Helper()
		ok, err := s.MemberMay(t.Context(), db.MemberMayParams{TeamID: team, UserID: user, Action: "run:write"})
		if err != nil {
			t.Fatalf("MemberMay: %v", err)
		}
		return ok
	}

	if may(team.ID) {
		t.Error("someone who is not a member may start runs")
	}

	addMember(t, s, team.ID, user, "member")
	if !may(team.ID) {
		t.Error("a member whose role grants run:write may not start runs")
	}
	if may(other.ID) {
		t.Error("a member of one team may start runs in another")
	}

	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO roles (name) VALUES ('watcher');
		INSERT INTO role_permissions (role, action) VALUES ('watcher', 'session:read_all');`); err != nil {
		t.Fatalf("adding a role without run:write: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE team_members SET role = 'watcher' WHERE team_id = $1 AND user_id = $2`, team.ID, user); err != nil {
		t.Fatalf("changing the role: %v", err)
	}
	if may(team.ID) {
		t.Error("a member moved to a role without run:write may still start runs")
	}

	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, team.ID, user); err != nil {
		t.Fatalf("removing the member: %v", err)
	}
	if may(team.ID) {
		t.Error("a removed member may still start runs")
	}
}

// running puts a run in the hands of worker w-1, as ClaimRun would, so
// FinishRunWithEvent has something to end.
func running(t *testing.T, s *Store, id uuid.UUID) {
	t.Helper()
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE runs SET status = 'running', worker = 'w-1', heartbeat_at = now(), started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("running %s: %v", id, err)
	}
}

func endChannelRun(t *testing.T, s *Store, id uuid.UUID, status, output, failure string) uuid.UUID {
	t.Helper()
	p := db.FinishRunParams{ID: id, Worker: "w-1", Status: status}
	if status == "succeeded" {
		p.Output = &output
	}
	if failure != "" {
		p.Error = &failure
	}
	n, followUp, err := s.FinishRunWithEvent(t.Context(), p, db.AppendRunEventParams{
		RunID: id, Kind: "finished", Payload: []byte(`{"status":"` + status + `"}`), Worker: "w-1",
	})
	if err != nil || n != 1 {
		t.Fatalf("FinishRunWithEvent(%s) = %d, %v; want 1 row", status, n, err)
	}
	return followUp
}

func repliesTo(t *testing.T, s *Store, run uuid.UUID) []db.Reply {
	t.Helper()
	rows, err := s.pool.Query(t.Context(), `SELECT kind, text FROM replies WHERE run_id = $1`, run)
	if err != nil {
		t.Fatalf("reading replies: %v", err)
	}
	defer rows.Close()
	var out []db.Reply
	for rows.Next() {
		var r db.Reply
		if err := rows.Scan(&r.Kind, &r.Text); err != nil {
			t.Fatalf("scanning a reply: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// A channel run that answers sends its answer back, written in the
// transaction that finishes it: a finished run with no reply cannot exist.
func TestAFinishedChannelRunSendsItsAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "answers")
	m := c.says(t, s, "m-1", "why did the export fail?")
	r, err := c.startRun(t, s.Queries, &m, "why did the export fail?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)

	if followUp := endChannelRun(t, s, r.ID, "succeeded", "It ran out of disk.", ""); followUp != uuid.Nil {
		t.Errorf("a run with nothing left over started %s", followUp)
	}
	if got := repliesTo(t, s, r.ID); len(got) != 1 || got[0].Kind != "answer" || got[0].Text != "It ran out of disk." {
		t.Errorf("replies = %+v, want the answer", got)
	}
}

// A failed run tells the person it could not answer, and never why: the error
// can name the brain's model endpoint, a server or a setting.
func TestAFailedChannelRunApologisesWithoutTheError(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "fails")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)

	endChannelRun(t, s, r.ID, "failed", "", "the model endpoint could not be reached: dial tcp 10.0.3.7:443")
	got := repliesTo(t, s, r.ID)
	if len(got) != 1 || got[0].Kind != "apology" || got[0].Text != Apology {
		t.Errorf("replies = %+v, want the apology", got)
	}
}

// An answer that is empty is no answer; the person gets the apology rather
// than a blank message.
func TestAnEmptyAnswerIsAnApology(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "blank")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)

	endChannelRun(t, s, r.ID, "succeeded", " \n", "")
	if got := repliesTo(t, s, r.ID); len(got) != 1 || got[0].Kind != "apology" {
		t.Errorf("replies = %+v, want the apology", got)
	}
}

// Someone stopped the run on purpose; nothing is sent, and nothing is started
// for what came after.
func TestACancelledChannelRunSendsNothing(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "cancelled")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)
	if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: "Asha: and the other one"}); err != nil {
		t.Fatalf("AddSteer: %v", err)
	}

	if followUp := endChannelRun(t, s, r.ID, "cancelled", "", ""); followUp != uuid.Nil {
		t.Errorf("a cancelled run started %s", followUp)
	}
	if got := repliesTo(t, s, r.ID); len(got) != 0 {
		t.Errorf("replies = %+v, want none", got)
	}
}

// Messages that came after the run's last model request are answered by one
// run started in the same transaction, whose input is those messages, oldest
// first. They are taken, so no later finish answers them again.
func TestSteersLeftOverStartOneRunToAnswerThem(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "leftovers")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)
	for _, text := range []string{"Asha: one more thing", "Asha: and another"} {
		if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: text}); err != nil {
			t.Fatalf("AddSteer: %v", err)
		}
	}

	followUp := endChannelRun(t, s, r.ID, "succeeded", "Done.", "")
	if followUp == uuid.Nil {
		t.Fatal("no run was started for the leftover steers")
	}
	next, err := s.GetRun(t.Context(), followUp)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", followUp, err)
	}
	if !next.ReplyToChannel || !next.Hub || next.Status != "queued" || next.SessionID != c.session.ID ||
		next.Input != "Asha: one more thing\nAsha: and another" || next.MessageID != nil {
		t.Errorf("follow-up = %+v, want a queued hub run replying in the same session, with both messages", next)
	}
	if left, _ := s.TakeLeftoverSteers(t.Context(), r.ID); len(left) != 0 {
		t.Errorf("leftovers still there: %q", left)
	}
	if got := repliesTo(t, s, r.ID); len(got) != 1 || got[0].Text != "Done." {
		t.Errorf("replies = %+v, want the finished run's own answer as well", got)
	}
}

// A run someone started from the dashboard finishes as it always did: no
// reply, no lock it does not need.
func TestARunFromTheDashboardFinishesAsBefore(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "dashboard-finish")
	agent := insertAgent(t, s, c.team, "triage")
	id := ranIn(t, s, c.team, c.session.ID, agent, "running", 1)
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET worker = 'w-1' WHERE id = $1`, id); err != nil {
		t.Fatalf("assigning the worker: %v", err)
	}
	// Nothing steers a dashboard run, but if a steer were there, the finish
	// still must not take it as a channel's leftover and start a run for it.
	if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: id, Text: "stray"}); err != nil {
		t.Fatalf("AddSteer: %v", err)
	}

	if followUp := endChannelRun(t, s, id, "succeeded", "Done.", ""); followUp != uuid.Nil {
		t.Errorf("a dashboard run started %s", followUp)
	}
	if got := repliesTo(t, s, id); len(got) != 0 {
		t.Errorf("replies = %+v, want none", got)
	}
}

// The finish itself takes the session's lock before it ends the run. Held
// here by a delivery that has not committed, the finish waits for it, and then
// takes the steer that delivery wrote.
func TestTheFinishWaitsForADeliveryHoldingTheSession(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "finish-waits")
	r, err := c.startRun(t, s.Queries, nil, "hello")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)

	deliver, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = deliver.Rollback(t.Context()) }()
	dq := db.New(deliver)
	if _, err := dq.LockSession(t.Context(), c.session.ID); err != nil {
		t.Fatalf("LockSession: %v", err)
	}
	if err := dq.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: "Asha: late"}); err != nil {
		t.Fatalf("AddSteer: %v", err)
	}

	followUp := make(chan uuid.UUID, 1)
	go func() { followUp <- endChannelRun(t, s, r.ID, "succeeded", "Done.", "") }()
	waitForLock(t, s, "FROM sessions WHERE id")

	if err := deliver.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := <-followUp; got == uuid.Nil {
		t.Error("the finish did not take the steer written while it waited: it did not wait")
	}
}

// A message that arrived after the run's last request was steered into it and
// never read: the run's reply quotes what the run did read, and the late one is
// the follow-up's. Each message and its steer are written in one transaction,
// as a delivery writes them, so they share now().
func TestAReplyQuotesTheNewestMessageItsRunRead(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "unread")
	first := c.says(t, s, "m-1", "can you search the internet?")
	r, err := c.startRun(t, s.Queries, &first, "can you search the internet?")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)

	steered := func(externalID, text string) uuid.UUID {
		var id uuid.UUID
		if err := s.InTx(t.Context(), func(q *db.Queries) error {
			var err error
			if id, err = q.InsertMessage(t.Context(), message(c.channel, c.session, c.user, externalID, text)); err != nil {
				return err
			}
			return q.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: "Asha: " + text})
		}); err != nil {
			t.Fatalf("delivering %s: %v", externalID, err)
		}
		return id
	}
	// Read mid-run: the SDK takes a steer before its next request.
	read := steered("m-2", "only this week")
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agent_steers WHERE session = $1`, r.ID.String()); err != nil {
		t.Fatal(err)
	}
	// Too late: after the last request, so nothing reads it.
	late := steered("m-3", "I mean web")

	followUp := endChannelRun(t, s, r.ID, "succeeded", "Yes, I can search.", "")
	if followUp == uuid.Nil {
		t.Fatal("the late message started no follow-up")
	}
	var quoted *uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `SELECT in_reply_to FROM replies WHERE run_id = $1`, r.ID).Scan(&quoted); err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	if quoted == nil || *quoted != read {
		t.Errorf("the reply quotes %v, want %s, the newest message the run read (not %s)", quoted, read, late)
	}
}
