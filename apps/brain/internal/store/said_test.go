package store

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func reachOf(t *testing.T, s *Store, run uuid.UUID) db.GetReplyReachRow {
	t.Helper()
	r, err := s.GetReplyReach(t.Context(), run)
	if err != nil {
		t.Fatalf("GetReplyReach: %v", err)
	}
	return r
}

// delegateOf is a running delegate of parent, in its conversation, held by
// the worker that holds its parent.
func delegateOf(t *testing.T, s *Store, parent db.Run, agent string) uuid.UUID {
	t.Helper()
	child := uuid.New()
	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, parent_run_id, root_run_id, depth, parent_tool_call, agent_name, input,
		                  reply_to_channel, status, worker, heartbeat_at, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'c1', $7, 'look', false, 'running', 'w-1', now(), now())`,
		child, parent.TeamID, parent.SessionID, parent.ID, parent.RootRunID, parent.Depth+1, agent); err != nil {
		t.Fatalf("insert delegate: %v", err)
	}
	return child
}

func say(t *testing.T, s *Store, run uuid.UUID, seq int32, text string) int64 {
	t.Helper()
	n, err := s.InsertSaidReply(t.Context(), db.InsertSaidReplyParams{RunID: run, EventSeq: seq, Text: text})
	if err != nil {
		t.Fatalf("InsertSaidReply: %v", err)
	}
	return n
}

func saysOnRun(t *testing.T, s *Store, run uuid.UUID, text string) int32 {
	t.Helper()
	seq, err := s.AppendRunEvent(t.Context(), db.AppendRunEventParams{
		RunID: run, Worker: "w-1", Kind: "said",
		Payload: []byte(`{"tool_call_id":"c1","agent":"x","text":` + quote(text) + `}`),
	})
	if err != nil {
		t.Fatalf("AppendRunEvent: %v", err)
	}
	return seq
}

func quote(s string) string { return `"` + s + `"` }

// A run on a channel it can post to answers its person, and so does each of
// its delegates, at any depth; one on a conversation read on its stream does
// too, whose event is the delivery. A channel that can no longer be posted
// to answers nobody mid-run.
func TestWhoARunTellsThingsMidRun(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "reach")

	root := c.working(t, s, "m1")
	if r := reachOf(t, s, root.ID); !r.Answers || r.Streamed {
		t.Errorf("a channel run: %+v, want it to answer on the channel", r)
	}
	child := delegateOf(t, s, root, "billing")
	grandchild := delegateOf(t, s, db.Run{ID: child, TeamID: root.TeamID, SessionID: root.SessionID, RootRunID: root.ID, Depth: 1}, "ledger")
	for name, id := range map[string]uuid.UUID{"a delegate": child, "a delegate's delegate": grandchild} {
		if r := reachOf(t, s, id); !r.Answers || r.Streamed {
			t.Errorf("%s: %+v, want it to answer as its root does", name, r)
		}
	}

	// A run in the conversation that does not answer the channel, as one the
	// API started there, tells nobody on it, and is sent nothing.
	quiet := c.working(t, s, "m9")
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET reply_to_channel = false WHERE id = $1`, quiet.ID); err != nil {
		t.Fatal(err)
	}
	if r := reachOf(t, s, quiet.ID); r.Answers {
		t.Errorf("a run that answers no channel: %+v, want nobody answered", r)
	}
	if n := say(t, s, quiet.ID, 1, "x"); n != 0 {
		t.Errorf("a run that answers no channel wrote %d replies", n)
	}

	if _, err := s.pool.Exec(t.Context(), `UPDATE channels SET reply_url = NULL WHERE id = $1`, c.channel.ID); err != nil {
		t.Fatal(err)
	}
	if r := reachOf(t, s, root.ID); r.Answers {
		t.Errorf("a custom channel with no reply URL: %+v, want nobody answered", r)
	}

	f := newRunFixture(t, s, "direct")
	alice := insertUser(t, s, testIssuer, "alice")
	f.session = insertDirectSession(t, s, f.team, alice)
	direct := createRun(t, s, f)
	if r := reachOf(t, s, direct.ID); !r.Answers || !r.Streamed {
		t.Errorf("a run read on its stream: %+v, want it to answer there", r)
	}
}

// What a run or its delegate said goes to the root's channel as a step, in
// the root's conversation and in turn with its other replies, each named by
// its event so a write retried is one. A conversation with no channel to
// post to is sent nothing.
func TestWhatIsSaidIsSentToTheRootsChannel(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "said")

	root := c.working(t, s, "m1")
	child := delegateOf(t, s, root, "billing")
	if n := say(t, s, root.ID, 4, "Checking the logs now."); n != 1 {
		t.Fatalf("the root's wrote %d", n)
	}
	if n := say(t, s, root.ID, 4, "Checking the logs now."); n != 0 {
		t.Errorf("said twice, %d more written", n)
	}
	if n := say(t, s, child, 2, "billing: the invoice is paid"); n != 1 {
		t.Fatalf("the delegate's wrote %d", n)
	}

	var got []string
	for range 2 {
		rows := claimReplies(t, s.Queries, 60)
		if len(rows) != 1 {
			t.Fatalf("claimed %d at once, want one per conversation", len(rows))
		}
		row := rows[0]
		if row.Kind != "event" || row.EventSeq == nil || row.InReplyTo != nil || row.ChannelID != c.channel.ID {
			t.Errorf("a said reply is %s, seq %v, quoting %v, on %s; want a step on the root's channel",
				row.Kind, row.EventSeq, row.InReplyTo, row.ChannelID)
		}
		got = append(got, row.RunID.String()+":"+row.Text)
		if _, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: row.ID, Attempts: row.Attempts}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{root.ID.String() + ":Checking the logs now.", child.String() + ":billing: the invoice is paid"}
	if !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}

	f := newRunFixture(t, s, "direct")
	alice := insertUser(t, s, testIssuer, "alice")
	f.session = insertDirectSession(t, s, f.team, alice)
	direct := createRun(t, s, f)
	if n := say(t, s, direct.ID, 1, "x"); n != 0 {
		t.Errorf("a run read on its stream wrote %d replies, want none", n)
	}
}

// A tree's said events are counted together and no other tree's; a run's
// are read back in the order they were said.
func TestWhatATreeSaidIsCountedTogether(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	c := newConversation(t, s, "count")

	root := c.working(t, s, "m1")
	child := delegateOf(t, s, root, "billing")
	other := newConversation(t, s, "other").working(t, s, "m2")
	saysOnRun(t, s, root.ID, "one")
	first := saysOnRun(t, s, child, "first")
	say(t, s, child, first, "billing: first")
	saysOnRun(t, s, child, "never posted")
	second := saysOnRun(t, s, child, "second")
	say(t, s, child, second, "billing: second")
	saysOnRun(t, s, other.ID, "elsewhere")
	if _, err := appendEvent(t, s, child, "w-1", "text"); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountSaidInTree(t.Context(), root.ID)
	if err != nil || n != 4 {
		t.Errorf("CountSaidInTree = %d, %v; want the tree's 4, the one never posted too", n, err)
	}
	// On a channel, what reached the person: its own words, without the name
	// its post carries, and not the one whose post failed.
	told, err := s.ListRunSaid(t.Context(), child)
	if err != nil || !slices.Equal(told, []string{"first", "second"}) {
		t.Errorf("ListRunSaid = %q, %v; want what was posted", told, err)
	}

	// On the dashboard the event is the delivery: every one is listed.
	f := newRunFixture(t, s, "said-direct")
	alice := insertUser(t, s, testIssuer, "alice")
	f.session = insertDirectSession(t, s, f.team, alice)
	direct := createRun(t, s, f)
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status = 'running', worker = 'w-1', heartbeat_at = now() WHERE id = $1`, direct.ID); err != nil {
		t.Fatal(err)
	}
	saysOnRun(t, s, direct.ID, "a")
	saysOnRun(t, s, direct.ID, "b")
	if told, err := s.ListRunSaid(t.Context(), direct.ID); err != nil || !slices.Equal(told, []string{"a", "b"}) {
		t.Errorf("ListRunSaid with no channel = %q, %v", told, err)
	}

	// A conversation the dashboard started is on the team's dashboard
	// channel, which is read on its stream too.
	d := newStreamFixture(t, s)
	dash := createRun(t, s, d.runFixture)
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status = 'running', worker = 'w-1', heartbeat_at = now() WHERE id = $1`, dash.ID); err != nil {
		t.Fatal(err)
	}
	saysOnRun(t, s, dash.ID, "a")
	saysOnRun(t, s, dash.ID, "b")
	if told, err := s.ListRunSaid(t.Context(), dash.ID); err != nil || !slices.Equal(told, []string{"a", "b"}) {
		t.Errorf("ListRunSaid on the dashboard channel = %q, %v", told, err)
	}
}
