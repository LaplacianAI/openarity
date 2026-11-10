package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// stranger is a sender waiting on a channel, as the gateway leaves them.
type stranger struct {
	team, channel uuid.UUID
	ref           string
}

func waitingStranger(t *testing.T, s *Store, ref string) stranger {
	t.Helper()
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "telegram", "bot")
	record(t, s, ch.ID, ref, "Erik")
	return stranger{team: team.ID, channel: ch.ID, ref: ref}
}

func (w stranger) tell(t *testing.T, s *Store) (int64, error) {
	t.Helper()
	return s.NoteWaitingSender(t.Context(), db.NoteWaitingSenderParams{
		TeamID: w.team, ChannelID: w.channel, SenderRef: w.ref,
		SessionRef: "-100", SessionKind: "group", MessageRef: "-100:7",
	})
}

func (w stranger) approve(t *testing.T, s *Store) db.ApproveSenderRow {
	t.Helper()
	user := insertUser(t, s, "dev", "erik-"+uuid.NewString())
	row, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: w.channel, SenderRef: w.ref, UserID: user,
	})
	if err != nil {
		t.Fatalf("ApproveSender: %v", err)
	}
	return row
}

func claimNotices(t *testing.T, s *Store) []db.ClaimDueNoticesRow {
	t.Helper()
	rows, err := s.ClaimDueNotices(t.Context(), db.ClaimDueNoticesParams{LeaseSeconds: 60, BatchSize: 20})
	if err != nil {
		t.Fatalf("ClaimDueNotices: %v", err)
	}
	return rows
}

func countNotices(t *testing.T, s *Store, channel uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM notices WHERE channel_id = $1", channel).Scan(&n); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	return n
}

func TestAWaitingSenderIsToldOnceAndWhereTheyWrote(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")

	if _, err := w.tell(t, s); err != nil {
		t.Fatalf("first notice: %v", err)
	}
	if _, err := w.tell(t, s); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second notice: err = %v, want no rows: one notice per wait", err)
	}

	rows := claimNotices(t, s)
	if len(rows) != 1 {
		t.Fatalf("claimed %d notices, want 1", len(rows))
	}
	got := rows[0]
	if got.Kind != "waiting" || got.SessionRef != "-100" || got.SessionKind != "group" ||
		got.InReplyTo != "-100:7" || got.SenderName != "Erik" || got.Provider != "telegram" {
		t.Errorf("notice = %+v, want a waiting notice to Erik, quoting -100:7 in -100", got)
	}
}

func TestTwoMessagesAtOnceMakeOneNotice(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")

	const racers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range racers {
		wg.Go(func() {
			<-start
			_, _ = s.NoteWaitingSender(t.Context(), db.NoteWaitingSenderParams{
				TeamID: w.team, ChannelID: w.channel, SenderRef: w.ref,
				SessionRef: "-100", SessionKind: "group", MessageRef: "-100:7",
			})
		})
	}
	close(start)
	wg.Wait()

	if n := countNotices(t, s, w.channel); n != 1 {
		t.Errorf("%d notices after %d messages at once, want 1", n, racers)
	}
}

// A sender dropped at the queue's cap has no pending row, so nothing to tell.
func TestASenderWhoWasNotQueuedIsNotTold(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	nobody := stranger{team: w.team, channel: w.channel, ref: "not-queued"}

	if _, err := nobody.tell(t, s); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want no rows", err)
	}
	if n := countNotices(t, s, w.channel); n != 0 {
		t.Errorf("%d notices, want none", n)
	}
}

func TestASenderToldTheyWereWaitingIsToldTheyAreIn(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	if _, err := w.tell(t, s); err != nil {
		t.Fatal(err)
	}

	approved := w.approve(t, s)
	if approved.Linked != 1 || approved.Admitted == 0 {
		t.Fatalf("approve = %+v, want linked and a notice", approved)
	}

	// The waiting notice is first in the conversation, so it is claimed
	// first and holds the second back until it is sent.
	first := claimNotices(t, s)
	if len(first) != 1 || first[0].Kind != "waiting" {
		t.Fatalf("first claim = %+v, want the waiting notice alone", first)
	}
	if _, err := s.NoticeSent(t.Context(), db.NoticeSentParams{ID: first[0].ID, Attempts: first[0].Attempts}); err != nil {
		t.Fatal(err)
	}
	second := claimNotices(t, s)
	if len(second) != 1 || second[0].ID != approved.Admitted || second[0].Kind != "admitted" ||
		second[0].SessionRef != "-100" || second[0].InReplyTo != "" {
		t.Errorf("second claim = %+v, want the admitted notice, where they were told, quoting nothing", second)
	}
}

func TestASenderNeverToldTheyWereWaitingIsNotToldTheyAreIn(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")

	if approved := w.approve(t, s); approved.Linked != 1 || approved.Admitted != 0 {
		t.Errorf("approve = %+v, want linked and no notice", approved)
	}
	if n := countNotices(t, s, w.channel); n != 0 {
		t.Errorf("%d notices, want none", n)
	}
}

func TestAChannelSwitchedOffSendsNoAdmittedNotice(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	if _, err := w.tell(t, s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{
		ID: w.channel, TeamID: w.team, NotifyStrangers: pgtype.Bool{Bool: false, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	if approved := w.approve(t, s); approved.Admitted != 0 {
		t.Errorf("approve wrote notice %d on a channel that sends none", approved.Admitted)
	}
}

func TestADeletedTeamsNoticesWait(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	if _, err := w.tell(t, s); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SoftDeleteTeam(t.Context(), w.team); err != nil || n != 1 {
		t.Fatalf("SoftDeleteTeam = %d, %v", n, err)
	}

	if rows := claimNotices(t, s); len(rows) != 0 {
		t.Errorf("claimed %d notices for a deleted team, want none", len(rows))
	}
}

// A mark from a claim whose lease ran out matches nothing, as for replies.
func TestAStaleClaimCannotMarkANotice(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	if _, err := w.tell(t, s); err != nil {
		t.Fatal(err)
	}
	row := claimNotices(t, s)[0]

	n, err := s.NoticeSent(t.Context(), db.NoticeSentParams{ID: row.ID, Attempts: row.Attempts - 1})
	if err != nil || n != 0 {
		t.Errorf("stale mark = %d, %v, want 0 rows", n, err)
	}
}

func TestUpdatingAChannelChangesOnlyWhatWasSent(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	url := "https://example.com/replies"
	ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: team.ID, Provider: "custom", Name: "support", ReplyUrl: &url,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.NotifyStrangers {
		t.Fatal("a new channel does not notify strangers; the default is on")
	}

	off, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{
		ID: ch.ID, TeamID: team.ID, NotifyStrangers: pgtype.Bool{Bool: false, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.NotifyStrangers || off.ReplyUrl == nil || *off.ReplyUrl != url {
		t.Errorf("after switching notices off: notify %v, reply_url %v; the reply URL should stay", off.NotifyStrangers, off.ReplyUrl)
	}

	cleared, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: ch.ID, TeamID: team.ID, SetReplyUrl: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ReplyUrl != nil || cleared.NotifyStrangers {
		t.Errorf("after clearing the reply URL: notify %v, reply_url %v; notify should stay off", cleared.NotifyStrangers, cleared.ReplyUrl)
	}
}

// An approval the binding check refuses changes nothing, and that includes
// the notice: nobody is told they are in when they are not.
func TestARefusedApprovalWritesNoNotice(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	w := waitingStranger(t, s, "42")
	if _, err := w.tell(t, s); err != nil {
		t.Fatal(err)
	}
	holder := insertUser(t, s, "dev", "holder")
	if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: w.channel, SenderRef: w.ref, UserID: holder,
	}); err != nil {
		t.Fatal(err)
	}

	wrong := uuid.New()
	row, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: w.channel, SenderRef: w.ref, UserID: insertUser(t, s, "dev", "other"), ExpectedHolder: &wrong,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Linked != 0 || row.Admitted != 0 {
		t.Errorf("approve = %+v, want a refusal that wrote nothing", row)
	}
	if n := countNotices(t, s, w.channel); n != 1 {
		t.Errorf("%d notices, want only the waiting one", n)
	}
}
