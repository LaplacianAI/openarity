package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// relay is a provider whose replies are the text, posted with the secret in
// a header, so a test can see what the sender handed it and what it sent.
type relay struct {
	fetchProvider
	err error
	// parts splits the text into this many requests; 0 is one, -1 is none.
	parts int

	mu   sync.Mutex
	outs []Outbound
	chs  []Channel
}

func (*relay) Name() string { return "relay" }

func (r *relay) Reply(ch Channel, out Outbound, creds Credentials, _ time.Time) ([]OutboundRequest, error) {
	r.mu.Lock()
	r.outs, r.chs = append(r.outs, out), append(r.chs, ch)
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	h := http.Header{}
	h.Set("X-Secret", creds.Get(KeySigning))
	n := max(r.parts, 1)
	if r.parts < 0 {
		n = 0
	}
	reqs := make([]OutboundRequest, n)
	for i := range reqs {
		body := out.Text
		if n > 1 {
			body = fmt.Sprintf("%s #%d", out.Text, i+1)
		}
		reqs[i] = OutboundRequest{URL: ch.ReplyURL, Header: h, Body: []byte(body)}
	}
	return reqs, nil
}

// outbox hands out its due rows a batch at a time and records every mark.
type outbox struct {
	due      []db.ClaimDueRepliesRow
	claimErr error
	markErr  error
	// partLost is a lease another sender took: a part's mark matches nothing.
	partLost bool

	claims  []db.ClaimDueRepliesParams
	parts   []db.ReplyPartSentParams
	sent    []db.ReplySentParams
	retried []db.RetryReplyParams
	gaveUp  []db.GiveUpReplyParams

	notices       []db.ClaimDueNoticesRow
	noticeSent    []db.NoticeSentParams
	noticeRetried []db.RetryNoticeParams
	noticeGaveUp  []db.GiveUpNoticeParams
}

func (o *outbox) ClaimDueNotices(_ context.Context, arg db.ClaimDueNoticesParams) ([]db.ClaimDueNoticesRow, error) {
	if o.claimErr != nil {
		return nil, o.claimErr
	}
	n := min(int(arg.BatchSize), len(o.notices))
	rows := o.notices[:n]
	o.notices = o.notices[n:]
	return rows, nil
}

func (o *outbox) NoticeSent(_ context.Context, arg db.NoticeSentParams) (int64, error) {
	o.noticeSent = append(o.noticeSent, arg)
	return 1, o.markErr
}

func (o *outbox) RetryNotice(_ context.Context, arg db.RetryNoticeParams) (int64, error) {
	o.noticeRetried = append(o.noticeRetried, arg)
	return 1, o.markErr
}

func (o *outbox) GiveUpNotice(_ context.Context, arg db.GiveUpNoticeParams) (int64, error) {
	o.noticeGaveUp = append(o.noticeGaveUp, arg)
	return 1, o.markErr
}

func (o *outbox) ClaimDueReplies(_ context.Context, arg db.ClaimDueRepliesParams) ([]db.ClaimDueRepliesRow, error) {
	o.claims = append(o.claims, arg)
	if o.claimErr != nil {
		return nil, o.claimErr
	}
	n := min(int(arg.BatchSize), len(o.due))
	rows := o.due[:n]
	o.due = o.due[n:]
	return rows, nil
}

func (o *outbox) ReplyPartSent(_ context.Context, arg db.ReplyPartSentParams) (int64, error) {
	o.parts = append(o.parts, arg)
	if o.partLost {
		return 0, o.markErr
	}
	return 1, o.markErr
}

func (o *outbox) ReplySent(_ context.Context, arg db.ReplySentParams) (int64, error) {
	o.sent = append(o.sent, arg)
	return 1, o.markErr
}

func (o *outbox) RetryReply(_ context.Context, arg db.RetryReplyParams) (int64, error) {
	o.retried = append(o.retried, arg)
	return 1, o.markErr
}

func (o *outbox) GiveUpReply(_ context.Context, arg db.GiveUpReplyParams) (int64, error) {
	o.gaveUp = append(o.gaveUp, arg)
	return 1, o.markErr
}

// receiver is the other end: it answers status and keeps what it was sent.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	heads  []http.Header
	// answers overrides the status for the nth request, in order.
	answers []int
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		code := status
		if i := len(r.bodies); i < len(r.answers) {
			code = r.answers[i]
		}
		r.bodies, r.heads = append(r.bodies, string(body)), append(r.heads, req.Header.Clone())
		r.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) got() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

type sending struct {
	store   *outbox
	secrets *fakeSecrets
	relay   *relay
	log     *bytes.Buffer
	sender  *Sender
}

func newSending(t *testing.T, providers ...Provider) *sending {
	t.Helper()
	rl := &relay{}
	if len(providers) == 0 {
		providers = []Provider{rl}
	}
	reg, err := NewRegistry(providers...)
	if err != nil {
		t.Fatal(err)
	}
	s := &sending{
		store:   &outbox{},
		secrets: &fakeSecrets{values: map[string]string{KeySigning: "s3cr3t"}},
		relay:   rl,
		log:     &bytes.Buffer{},
	}
	// A transport of its own: httptest.Server.Close closes the idle
	// connections of http.DefaultTransport, so another parallel test's
	// receiver closing would break this one's request mid-flight.
	client := &http.Client{
		Transport:     &http.Transport{},
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	s.sender = NewSender(s.store, s.secrets, reg, client, slog.New(slog.NewTextHandler(s.log, nil)))
	return s
}

func due(url string, attempts int32) db.ClaimDueRepliesRow {
	ref, msg := "D-asha", "m-1"
	return db.ClaimDueRepliesRow{
		ID: 7, RunID: uuid.New(), TeamID: uuid.New(), ChannelID: uuid.New(),
		Kind: "answer", Text: "The nightly export ran out of disk.", Attempts: attempts,
		Provider: "relay", ReplyUrl: &url, SessionRef: &ref, SessionKind: "direct", InReplyTo: &msg,
	}
}

// The whole of it: the reply the provider built from the row, posted with the
// channel's own secret, and marked sent under the claim it was sent on.
func TestADueReplyIsPostedAndMarkedSent(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	rcv := newReceiver(t, http.StatusAccepted)
	row := due(rcv.URL+"/replies", 1)
	s.store.due = []db.ClaimDueRepliesRow{row}

	n, err := s.sender.Drain(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v; want 1 sent", n, err)
	}
	if rcv.got() != 1 || rcv.bodies[0] != row.Text || rcv.heads[0].Get("X-Secret") != "s3cr3t" {
		t.Errorf("the receiver got %q with %v", rcv.bodies, rcv.heads)
	}
	if want := secrets.Path(row.TeamID, secrets.KindChannel, row.ChannelID); len(s.secrets.paths) != 1 || s.secrets.paths[0] != want {
		t.Errorf("read secrets at %v, want only the channel's own %s", s.secrets.paths, want)
	}
	out := s.relay.outs[0]
	if out.ID != row.RunID.String() || out.Session != (Session{Ref: "D-asha", Kind: SessionDirect}) ||
		out.InReplyTo != "m-1" || out.Kind != "answer" || s.relay.chs[0].ReplyURL != rcv.URL+"/replies" {
		t.Errorf("the provider was handed %+v for %+v", out, s.relay.chs[0])
	}
	if len(s.store.sent) != 1 || s.store.sent[0] != (db.ReplySentParams{ID: row.ID, Attempts: 1}) {
		t.Errorf("marked sent %+v, want reply 7 on attempt 1", s.store.sent)
	}
	if len(s.store.retried)+len(s.store.gaveUp) != 0 {
		t.Error("a sent reply was also marked failed")
	}
}

// A refusal is retried on the schedule, the wait growing with the attempt.
func TestARefusedReplyIsRetriedLaterEachTime(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusServiceUnavailable)
	want := []float64{60, 300, 1800, 3600, 3600}
	for attempt := int32(1); attempt < replyAttempts; attempt++ {
		s := newSending(t)
		s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, attempt)}
		if _, err := s.sender.Drain(t.Context()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if len(s.store.retried) != 1 || len(s.store.gaveUp) != 0 {
			t.Fatalf("attempt %d: retried %d, gave up %d; want one retry", attempt, len(s.store.retried), len(s.store.gaveUp))
		}
		r := s.store.retried[0]
		if r.RetrySeconds != want[attempt-1] || r.Attempts != attempt || !strings.Contains(r.Error, "503") {
			t.Errorf("attempt %d: retry %+v, want in %vs naming the 503", attempt, r, want[attempt-1])
		}
	}
}

// The last attempt's failure is the end: failed, logged, and never the text.
func TestTheLastFailedAttemptGivesUp(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	rcv := newReceiver(t, http.StatusInternalServerError)
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, replyAttempts)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(s.store.gaveUp) != 1 || len(s.store.retried) != 0 || !strings.Contains(s.store.gaveUp[0].Error, "500") {
		t.Errorf("gave up %+v, retried %+v; want one give-up naming the 500", s.store.gaveUp, s.store.retried)
	}
	if !strings.Contains(s.log.String(), "gave up") {
		t.Errorf("log = %q, want the give-up", s.log)
	}
	if strings.Contains(s.log.String(), "nightly export") {
		t.Error("the log carries what the team said")
	}
}

// A redirect is the receiver's answer, not a place to go: a 302 would carry
// the reply to an address the URL check never saw.
func TestARedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	elsewhere := newReceiver(t, http.StatusOK)
	redirect := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	t.Cleanup(redirect.Close)

	s := newSending(t)
	s.store.due = []db.ClaimDueRepliesRow{due(redirect.URL, 1)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if elsewhere.got() != 0 {
		t.Error("the reply followed the redirect")
	}
	if len(s.store.sent) != 0 || len(s.store.retried) != 1 {
		t.Errorf("sent %d retried %d; want the 302 retried", len(s.store.sent), len(s.store.retried))
	}
}

// What the provider refuses to build, it will refuse again: no retry, no post.
func TestAReplyTheProviderCannotBuildIsGivenUpAtOnce(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.err = errors.New("the channel has no reply URL")
	rcv := newReceiver(t, http.StatusOK)
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(s.store.gaveUp) != 1 || len(s.store.retried) != 0 || rcv.got() != 0 {
		t.Errorf("gave up %d retried %d posted %d; want given up on the first attempt, unposted",
			len(s.store.gaveUp), len(s.store.retried), rcv.got())
	}
	if !strings.Contains(s.store.gaveUp[0].Error, "no reply URL") {
		t.Errorf("error = %q, want the provider's reason", s.store.gaveUp[0].Error)
	}
}

// A channel whose provider sends no replies is the same: nothing to wait for.
func TestAReplyForAProviderThatCannotReplyIsGivenUpAtOnce(t *testing.T) {
	t.Parallel()

	s := newSending(t, &fetchProvider{})
	row := due("https://support.example.com/replies", 1)
	row.Provider = "fake"
	s.store.due = []db.ClaimDueRepliesRow{row}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(s.store.gaveUp) != 1 || len(s.store.retried) != 0 {
		t.Errorf("gave up %d retried %d; want given up at once", len(s.store.gaveUp), len(s.store.retried))
	}
}

// The secret store being down is ours to fix, and fixed by waiting.
func TestASecretThatCannotBeReadIsRetried(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.secrets.err = errors.New("openbao: sealed")
	rcv := newReceiver(t, http.StatusOK)
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(s.store.retried) != 1 || len(s.store.gaveUp) != 0 || rcv.got() != 0 {
		t.Errorf("retried %d gave up %d posted %d; want a retry and no post",
			len(s.store.retried), len(s.store.gaveUp), rcv.got())
	}
}

// last_error is shown to the team; the URL's path stays out of it.
func TestAnUnreachableReceiverIsRetriedWithoutItsURLInTheError(t *testing.T) {
	t.Parallel()

	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL + "/private-path"
	gone.Close()

	s := newSending(t)
	s.store.due = []db.ClaimDueRepliesRow{due(url, 1)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(s.store.retried) != 1 {
		t.Fatalf("retried %d, want 1", len(s.store.retried))
	}
	if strings.Contains(s.store.retried[0].Error, "private-path") {
		t.Errorf("error %q quotes the URL", s.store.retried[0].Error)
	}
}

// A mark that fails is Postgres failing. Stop: the rest are still leased and
// come back when the lease runs out.
func TestAFailedMarkStopsTheDrain(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	rcv := newReceiver(t, http.StatusOK)
	s.store.markErr = errors.New("connection reset")
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1), due(rcv.URL, 1)}
	if _, err := s.sender.Drain(t.Context()); err == nil {
		t.Fatal("Drain succeeded although it could not record a send")
	}
	if rcv.got() != 1 {
		t.Errorf("posted %d replies after a mark failed, want 1", rcv.got())
	}
}

func TestAFailedClaimIsAnError(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.store.claimErr = errors.New("connection reset")
	if _, err := s.sender.Drain(t.Context()); err == nil {
		t.Error("Drain succeeded although it could not claim")
	}
}

// One drain empties what is due, batch after batch, and stops on a short one.
func TestADrainClaimsUntilABatchComesBackShort(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	rcv := newReceiver(t, http.StatusOK)
	for range replyBatch + 5 {
		s.store.due = append(s.store.due, due(rcv.URL, 1))
	}
	n, err := s.sender.Drain(t.Context())
	if err != nil || n != replyBatch+5 {
		t.Fatalf("Drain = %d, %v; want %d", n, err, replyBatch+5)
	}
	if len(s.store.claims) != 2 {
		t.Errorf("claimed %d times, want 2", len(s.store.claims))
	}
	if c := s.store.claims[0]; c.BatchSize != replyBatch || c.LeaseSeconds != replyLease.Seconds() {
		t.Errorf("claim %+v, want the batch and the lease", c)
	}
}

// A long reply is several requests: each is posted in order and recorded as
// it goes, and the reply is sent once the last one is.
func TestALongReplyIsSentPartByPart(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = 3
	rcv := newReceiver(t, http.StatusOK)
	row := due(rcv.URL, 1)
	s.store.due = []db.ClaimDueRepliesRow{row}

	if n, err := s.sender.Drain(t.Context()); err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v; want 1 sent", n, err)
	}
	want := []string{row.Text + " #1", row.Text + " #2", row.Text + " #3"}
	if !slices.Equal(rcv.bodies, want) {
		t.Errorf("the receiver got %q, want %q", rcv.bodies, want)
	}
	for i, p := range s.store.parts {
		if p != (db.ReplyPartSentParams{ID: row.ID, Attempts: 1, PartsSent: int32(i + 1)}) {
			t.Errorf("part mark %d = %+v", i, p)
		}
	}
	if len(s.store.parts) != 3 || len(s.store.sent) != 1 {
		t.Errorf("marked %d parts and %d sent, want 3 and 1", len(s.store.parts), len(s.store.sent))
	}
}

// A retry starts at the first part not sent: the receiver never sees one twice.
func TestARetryResumesAtThePartNotSent(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = 3
	rcv := newReceiver(t, http.StatusOK)
	row := due(rcv.URL, 2)
	row.PartsSent = 1
	s.store.due = []db.ClaimDueRepliesRow{row}

	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if want := []string{row.Text + " #2", row.Text + " #3"}; !slices.Equal(rcv.bodies, want) {
		t.Errorf("the receiver got %q, want only %q", rcv.bodies, want)
	}
	if len(s.store.parts) != 2 || s.store.parts[0].PartsSent != 2 || s.store.parts[1].PartsSent != 3 {
		t.Errorf("part marks %+v, want 2 then 3", s.store.parts)
	}
}

// A part refused halfway keeps the ones before it, and the reply waits for a
// retry rather than being marked sent or given up.
func TestAPartRefusedKeepsTheEarlierParts(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = 3
	rcv := newReceiver(t, http.StatusOK)
	rcv.answers = []int{http.StatusOK, http.StatusTooManyRequests}
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}

	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if rcv.got() != 2 {
		t.Errorf("the receiver got %d requests, want 2: the third waits", rcv.got())
	}
	if len(s.store.parts) != 1 || s.store.parts[0].PartsSent != 1 {
		t.Errorf("part marks %+v, want only the first", s.store.parts)
	}
	if len(s.store.retried) != 1 || len(s.store.sent)+len(s.store.gaveUp) != 0 {
		t.Errorf("retried %d, sent %d, gave up %d; want one retry", len(s.store.retried), len(s.store.sent), len(s.store.gaveUp))
	}
}

// A sender whose claim another took stops: the next part is that sender's to
// post, and so is every mark.
func TestASenderThatLostItsLeaseStops(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = 3
	s.store.partLost = true
	rcv := newReceiver(t, http.StatusOK)
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}

	if n, err := s.sender.Drain(t.Context()); err != nil || n != 0 {
		t.Fatalf("Drain = %d, %v; want nothing counted sent", n, err)
	}
	if rcv.got() != 1 {
		t.Errorf("the receiver got %d requests, want 1: the rest are another sender's", rcv.got())
	}
	if len(s.store.sent)+len(s.store.retried)+len(s.store.gaveUp) != 0 {
		t.Error("a sender that lost its lease marked the reply")
	}
}

// A provider that builds nothing has sent nothing: marking that sent is the
// stub that reads as success.
func TestAReplyBuiltAsNothingIsGivenUp(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = -1
	rcv := newReceiver(t, http.StatusOK)
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}

	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if rcv.got() != 0 || len(s.store.sent) != 0 || len(s.store.gaveUp) != 1 {
		t.Errorf("posted %d, sent %d, gave up %d; want it given up unposted", rcv.got(), len(s.store.sent), len(s.store.gaveUp))
	}
}

// noticer is a provider that tells strangers: the notice is posted to the
// address the test gives it, and what the sender handed over is kept.
type noticer struct {
	relay
	url string

	nmu     sync.Mutex
	notices []Notice
}

func (*noticer) Name() string { return "noticer" }

func (n *noticer) Notice(_ Channel, notice Notice, creds Credentials) (OutboundRequest, error) {
	n.nmu.Lock()
	n.notices = append(n.notices, notice)
	n.nmu.Unlock()
	h := http.Header{}
	h.Set("X-Secret", creds.Get(KeySigning))
	return OutboundRequest{URL: n.url, Header: h, Body: []byte(notice.Sender.DisplayName + ": " + notice.Text)}, nil
}

func waitingNotice(attempts int32) db.ClaimDueNoticesRow {
	return db.ClaimDueNoticesRow{
		ID: 3, TeamID: uuid.New(), ChannelID: uuid.New(), SenderRef: "4242", SenderName: "Erik",
		SessionRef: "4242", SessionKind: "direct", InReplyTo: "4242:7", Kind: "waiting",
		Attempts: attempts, Provider: "noticer",
	}
}

func TestANoticeIsBuiltByItsProviderPostedAndMarkedSent(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusOK)
	p := &noticer{url: rcv.URL}
	s := newSending(t, p)
	s.store.notices = []db.ClaimDueNoticesRow{waitingNotice(1)}

	if n, err := s.sender.Drain(t.Context()); err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v, want 1", n, err)
	}
	if rcv.got() != 1 || rcv.heads[0].Get("X-Secret") != "s3cr3t" {
		t.Errorf("received %d posts %v", rcv.got(), rcv.heads)
	}
	if len(p.notices) != 1 {
		t.Fatalf("provider built %d notices, want 1", len(p.notices))
	}
	got := p.notices[0]
	if got.Kind != NoticeWaiting || got.Text != NoticeText(NoticeWaiting) || got.InReplyTo != "4242:7" ||
		got.Sender.Ref != "4242" || got.Session.Kind != SessionDirect || got.ID != "notice:3" {
		t.Errorf("notice = %+v", got)
	}
	if len(s.store.noticeSent) != 1 || s.store.noticeSent[0].Attempts != 1 {
		t.Errorf("marked %+v, want sent on attempt 1", s.store.noticeSent)
	}
}

func TestANoticeThatFailsIsRetriedLikeAReplyAndGivenUpAfterSix(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusInternalServerError)
	s := newSending(t, &noticer{url: rcv.URL})
	s.store.notices = []db.ClaimDueNoticesRow{waitingNotice(1)}
	if n, err := s.sender.Drain(t.Context()); err != nil || n != 0 {
		t.Fatalf("Drain = %d, %v, want 0", n, err)
	}
	if len(s.store.noticeRetried) != 1 || s.store.noticeRetried[0].RetrySeconds != time.Minute.Seconds() {
		t.Errorf("retried %+v, want one retry after a minute", s.store.noticeRetried)
	}

	s.store.notices = []db.ClaimDueNoticesRow{waitingNotice(replyAttempts)}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.store.noticeGaveUp) != 1 || len(s.store.noticeSent) != 0 {
		t.Errorf("gave up %+v, sent %+v; want one give-up and nothing sent", s.store.noticeGaveUp, s.store.noticeSent)
	}
}

// A provider that cannot send notices can only be asked by a row that should
// not exist: the answer is the same every time, so it is given up at once.
func TestANoticeForAProviderThatSendsNoneIsGivenUpAtOnce(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	row := waitingNotice(1)
	row.Provider = "relay"
	s.store.notices = []db.ClaimDueNoticesRow{row}

	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.store.noticeGaveUp) != 1 || len(s.store.noticeRetried) != 0 {
		t.Errorf("gave up %d, retried %d; want a give-up and no retry", len(s.store.noticeGaveUp), len(s.store.noticeRetried))
	}
}

// Replies and notices share one sender: a drain sends both.
func TestADrainSendsRepliesAndNotices(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusOK)
	p := &noticer{url: rcv.URL}
	s := newSending(t, p)
	s.store.notices = []db.ClaimDueNoticesRow{waitingNotice(1)}
	s.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, 1)}
	s.store.due[0].Provider = "noticer"

	if n, err := s.sender.Drain(t.Context()); err != nil || n != 2 {
		t.Fatalf("Drain = %d, %v, want 2", n, err)
	}
}
