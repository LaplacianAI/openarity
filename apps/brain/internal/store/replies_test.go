package store

import (
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const replyTo = "https://support.example.com/replies"

// answered is a run in c that answered the message text, with its reply now
// pending in the outbox. It returns the reply's id.
func (c conversation) answered(t *testing.T, s *Store, externalID, text string) int64 {
	t.Helper()
	m := c.says(t, s, externalID, text)
	r, err := c.startRun(t, s.Queries, &m, text)
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)
	endChannelRun(t, s, r.ID, "succeeded", "answer to "+text, "")

	var id int64
	if err := s.pool.QueryRow(t.Context(), `SELECT id FROM replies WHERE run_id = $1`, r.ID).Scan(&id); err != nil {
		t.Fatalf("the reply to %s: %v", externalID, err)
	}
	return id
}

// elsewhere is another direct conversation on the same channel.
func (c conversation) elsewhere(t *testing.T, s *Store, ref string) conversation {
	t.Helper()
	c.session = mustEnsureSession(t, s, c.team, c.channel.ID, ref, "direct")
	return c
}

func withReplyURL(t *testing.T, s *Store, c conversation) {
	t.Helper()
	if _, err := s.pool.Exec(t.Context(), `UPDATE channels SET reply_url = $1 WHERE id = $2`, replyTo, c.channel.ID); err != nil {
		t.Fatalf("setting the reply URL: %v", err)
	}
}

func claimReplies(t *testing.T, q *db.Queries, lease float64) []db.ClaimDueRepliesRow {
	t.Helper()
	rows, err := q.ClaimDueReplies(t.Context(), db.ClaimDueRepliesParams{LeaseSeconds: lease, BatchSize: 20})
	if err != nil {
		t.Fatalf("ClaimDueReplies: %v", err)
	}
	return rows
}

func ids(rows []db.ClaimDueRepliesRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A claimed reply carries everything a provider needs to send it, by the
// names the provider gave: the conversation's ref and the message's own id.
func TestAClaimedReplyCarriesWhereItGoes(t *testing.T) {
	s := queryStore(t)
	c := newConversation(t, s, "where")
	withReplyURL(t, s, c)
	id := c.answered(t, s, "m-1", "why did the export fail?")

	rows := claimReplies(t, s.Queries, 60)
	if len(rows) != 1 {
		t.Fatalf("claimed %d replies, want 1", len(rows))
	}
	r := rows[0]
	if r.ID != id || r.Attempts != 1 || r.Kind != "answer" || r.Text != "answer to why did the export fail?" ||
		r.Provider != "custom" || r.ReplyUrl == nil || *r.ReplyUrl != replyTo ||
		r.SessionRef == nil || *r.SessionRef != "D-where" || r.SessionKind != "direct" ||
		r.InReplyTo == nil || *r.InReplyTo != "m-1" || r.ChannelID != c.channel.ID || r.TeamID != c.team {
		t.Errorf("claimed %+v", r)
	}
}

// A conversation is read in order. Its second reply waits for its first,
// while another conversation's reply goes at once.
func TestAConversationsRepliesAreClaimedInOrder(t *testing.T) {
	s := queryStore(t)
	a := newConversation(t, s, "order")
	b := a.elsewhere(t, s, "D-other")
	a1 := a.answered(t, s, "m-1", "first")
	a2 := a.answered(t, s, "m-2", "second")
	b1 := b.answered(t, s, "m-3", "other")

	if got := ids(claimReplies(t, s.Queries, 60)); !same(got, []int64{a1, b1}) {
		t.Fatalf("first claim = %v, want %v: the second of a conversation waits", got, []int64{a1, b1})
	}
	if n, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: a1, Attempts: 1}); err != nil || n != 1 {
		t.Fatalf("ReplySent = %d, %v", n, err)
	}
	if got := ids(claimReplies(t, s.Queries, 60)); !same(got, []int64{a2}) {
		t.Errorf("after the first was sent, claimed %v, want %v", got, []int64{a2})
	}
}

// A reply waiting for its retry holds back its own conversation, and only
// until it is given up: a failed reply is not pending, so the next goes.
func TestAReplyWaitingToRetryHoldsBackItsConversationUntilGivenUp(t *testing.T) {
	s := queryStore(t)
	a := newConversation(t, s, "retry")
	a1 := a.answered(t, s, "m-1", "first")
	a2 := a.answered(t, s, "m-2", "second")

	claimReplies(t, s.Queries, 60)
	if n, err := s.RetryReply(t.Context(), db.RetryReplyParams{
		ID: a1, Attempts: 1, Error: "the reply URL answered 503", RetrySeconds: 60,
	}); err != nil || n != 1 {
		t.Fatalf("RetryReply = %d, %v", n, err)
	}
	if got := claimReplies(t, s.Queries, 60); len(got) != 0 {
		t.Fatalf("claimed %v while the first waits to retry, want nothing", ids(got))
	}

	if _, err := s.pool.Exec(t.Context(), `UPDATE replies SET next_attempt_at = now() WHERE id = $1`, a1); err != nil {
		t.Fatal(err)
	}
	claimReplies(t, s.Queries, 60)
	if n, err := s.GiveUpReply(t.Context(), db.GiveUpReplyParams{ID: a1, Attempts: 2, Error: "the reply URL answered 503"}); err != nil || n != 1 {
		t.Fatalf("GiveUpReply = %d, %v", n, err)
	}
	if got := ids(claimReplies(t, s.Queries, 60)); !same(got, []int64{a2}) {
		t.Errorf("after the first failed, claimed %v, want %v", got, []int64{a2})
	}

	var status, lastError string
	if err := s.pool.QueryRow(t.Context(), `SELECT status, last_error FROM replies WHERE id = $1`, a1).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != "the reply URL answered 503" {
		t.Errorf("the given-up reply is %s %q", status, lastError)
	}
}

// The lease: a claimed reply is nobody else's until it runs out, and then it
// is claimed again under a new attempt. The first sender's late mark matches
// nothing, so it cannot record a send the second may be making differently.
func TestALeasedReplyIsClaimedAgainOnlyOnceItRunsOutAndAStaleMarkIsIgnored(t *testing.T) {
	s := queryStore(t)
	c := newConversation(t, s, "lease")
	id := c.answered(t, s, "m-1", "hello")

	claimReplies(t, s.Queries, 60)
	if got := claimReplies(t, s.Queries, 60); len(got) != 0 {
		t.Fatalf("claimed %v again inside its lease", ids(got))
	}

	if _, err := s.pool.Exec(t.Context(), `UPDATE replies SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	again := claimReplies(t, s.Queries, 60)
	if len(again) != 1 || again[0].Attempts != 2 {
		t.Fatalf("after the lease, claimed %+v, want it on attempt 2", again)
	}

	if n, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: id, Attempts: 1}); err != nil || n != 0 {
		t.Errorf("a stale sender's mark matched %d rows (%v), want 0", n, err)
	}
	if n, err := s.RetryReply(t.Context(), db.RetryReplyParams{ID: id, Attempts: 1, Error: "late", RetrySeconds: 60}); err != nil || n != 0 {
		t.Errorf("a stale sender's retry matched %d rows (%v), want 0", n, err)
	}
	if n, err := s.GiveUpReply(t.Context(), db.GiveUpReplyParams{ID: id, Attempts: 1, Error: "late"}); err != nil || n != 0 {
		t.Errorf("a stale sender's give-up matched %d rows (%v), want 0", n, err)
	}
	if n, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: id, Attempts: 2}); err != nil || n != 1 {
		t.Errorf("the current sender's mark matched %d rows (%v), want 1", n, err)
	}
	if n, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: id, Attempts: 2}); err != nil || n != 0 {
		t.Errorf("a sent reply was marked again: %d rows (%v)", n, err)
	}
}

// Two senders claiming at the same moment: the second skips what the first
// holds, and skips the reply queued behind it too.
func TestTwoSendersNeverClaimTheSameReply(t *testing.T) {
	s := queryStore(t)
	a := newConversation(t, s, "race")
	b := a.elsewhere(t, s, "D-other")
	a1 := a.answered(t, s, "m-1", "first")
	a.answered(t, s, "m-2", "second")
	b1 := b.answered(t, s, "m-3", "other")

	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	q := db.New(tx)
	first, err := q.ClaimDueReplies(t.Context(), db.ClaimDueRepliesParams{LeaseSeconds: 60, BatchSize: 1})
	if err != nil || len(first) != 1 || first[0].ID != a1 {
		t.Fatalf("the first sender claimed %v (%v), want %d", ids(first), err, a1)
	}

	if got := ids(claimReplies(t, s.Queries, 60)); !same(got, []int64{b1}) {
		t.Errorf("the second sender claimed %v, want only the other conversation's %d", got, b1)
	}
}

// A reply's lease and backoff are counted from the database's clock.
func TestARetryIsDueAfterItsWait(t *testing.T) {
	s := queryStore(t)
	c := newConversation(t, s, "wait")
	id := c.answered(t, s, "m-1", "hello")
	claimReplies(t, s.Queries, 60)

	if _, err := s.RetryReply(t.Context(), db.RetryReplyParams{ID: id, Attempts: 1, Error: "x", RetrySeconds: 300}); err != nil {
		t.Fatal(err)
	}
	var wait float64
	if err := s.pool.QueryRow(t.Context(),
		`SELECT extract(epoch FROM next_attempt_at - now()) FROM replies WHERE id = $1`, id).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait < 290 || wait > 300 {
		t.Errorf("due in %.0fs, want about 300", wait)
	}
}
