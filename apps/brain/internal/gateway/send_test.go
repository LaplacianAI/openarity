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
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
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

	// files are the file replies' files, by reply id; fileErr fails the read.
	files     map[int64]db.GetReplyFileRow
	fileErr   error
	fileReads []int64

	notices       []db.ClaimDueNoticesRow
	noticeClaims  []db.ClaimDueNoticesParams
	noticeSent    []db.NoticeSentParams
	noticeRetried []db.RetryNoticeParams
	noticeGaveUp  []db.GiveUpNoticeParams
}

func (o *outbox) ClaimDueNotices(_ context.Context, arg db.ClaimDueNoticesParams) ([]db.ClaimDueNoticesRow, error) {
	o.noticeClaims = append(o.noticeClaims, arg)
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

func (o *outbox) GetReplyFile(_ context.Context, id int64) (db.GetReplyFileRow, error) {
	o.fileReads = append(o.fileReads, id)
	if o.fileErr != nil {
		return db.GetReplyFileRow{}, o.fileErr
	}
	f, ok := o.files[id]
	if !ok {
		return db.GetReplyFileRow{}, pgx.ErrNoRows
	}
	return f, nil
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
	s.sender = NewSender(s.store, s.secrets, reg, HTTPPoster(client), Scope{}, slog.New(slog.NewTextHandler(s.log, nil)))
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

func dueEvent(url string, attempts, seq int32) db.ClaimDueRepliesRow {
	row := due(url, attempts)
	row.Kind, row.Text, row.EventSeq, row.InReplyTo = "event", "Using `read_logs`…", &seq, nil
	return row
}

// A step carries the run event it came from beside the run, so a receiver
// drops a repeat of it without dropping the run's other steps or its answer.
func TestAStepIsSentWithAnIDOfItsOwn(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	rcv := newReceiver(t, http.StatusOK)
	row := dueEvent(rcv.URL, 1, 12)
	s.store.due = []db.ClaimDueRepliesRow{row}
	if n, err := s.sender.Drain(t.Context()); err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	out := s.relay.outs[0]
	if out.ID != row.RunID.String()+":12" || out.Kind != "event" || out.InReplyTo != "" {
		t.Errorf("the provider was handed %+v", out)
	}
}

// Only a run's answer, or the apology in its place, carries the run's id. On
// staging the notice that an answer in the chat was taken went out under it
// first, and WhatsApp's holder dropped the answer as its repeat.
func TestOnlyARunsAnswerCarriesItsID(t *testing.T) {
	t.Parallel()

	run, seq := uuid.New(), int32(13)
	row := func(id int64, kind string, event *int32) db.ClaimDueRepliesRow {
		return db.ClaimDueRepliesRow{ID: id, RunID: run, Kind: kind, EventSeq: event}
	}
	for _, c := range []struct {
		name  string
		row   db.ClaimDueRepliesRow
		id    string
		tries int32
	}{
		{"answer", row(113, "answer", nil), run.String(), replyAttempts},
		{"apology", row(114, "apology", nil), run.String(), replyAttempts},
		{"notice", row(112, "notice", nil), run.String() + ":notice:112", replyAttempts},
		{"another notice", row(120, "notice", nil), run.String() + ":notice:120", replyAttempts},
		{"approval of an event", row(111, "approval", &seq), run.String() + ":13", eventAttempts},
		{"event", row(115, "event", &seq), run.String() + ":13", eventAttempts},
		{"approval of no event", row(116, "approval", nil), run.String() + ":approval:116", replyAttempts},
		{"file", row(118, "file", nil), run.String() + ":file:118", eventAttempts},
		{"no kind", row(117, "", nil), run.String() + "::117", replyAttempts},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if id, tries := replyID(c.row); id != c.id || tries != c.tries {
				t.Errorf("replyID = %q, %d; want %q, %d", id, tries, c.id, c.tries)
			}
		})
	}
}

// Drained together, a run's notice and answer go out under different ids, and
// a retry of the notice is the same notice.
func TestANoticeOfARunIsNotTakenForItsAnswer(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusOK)
	notice := due(rcv.URL, 1)
	notice.ID, notice.Kind, notice.Text = 112, "notice", "Approved: get_file_contents on github."
	answer := due(rcv.URL, 1)
	answer.ID, answer.RunID, answer.Kind = 113, notice.RunID, "answer"

	s := newSending(t)
	s.store.due = []db.ClaimDueRepliesRow{notice, answer}
	if n, err := s.sender.Drain(t.Context()); err != nil || n != 2 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if len(s.relay.outs) != 2 {
		t.Fatalf("%d sent, want the notice and the answer", len(s.relay.outs))
	}
	sentNotice, sentAnswer := s.relay.outs[0].ID, s.relay.outs[1].ID
	if sentNotice == sentAnswer || sentAnswer != notice.RunID.String() {
		t.Errorf("notice %q, answer %q: the answer carries the run's id, the notice its own", sentNotice, sentAnswer)
	}

	again := newSending(t)
	notice.Attempts = 2
	again.store.due = []db.ClaimDueRepliesRow{notice}
	if _, err := again.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := again.relay.outs[0].ID; got != sentNotice {
		t.Errorf("a retry of the notice is %q, want %q", got, sentNotice)
	}
}

// A broken step is tried twice, not six times: its answer waits behind it.
// The answer keeps its own schedule.
func TestAStepThatFailsTwiceIsGivenUp(t *testing.T) {
	t.Parallel()

	rcv := newReceiver(t, http.StatusServiceUnavailable)
	first := newSending(t)
	first.store.due = []db.ClaimDueRepliesRow{dueEvent(rcv.URL, 1, 3)}
	if _, err := first.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(first.store.retried) != 1 || first.store.retried[0].RetrySeconds != 60 {
		t.Errorf("a step's first failure: retried %+v, want once in a minute", first.store.retried)
	}

	second := newSending(t)
	second.store.due = []db.ClaimDueRepliesRow{dueEvent(rcv.URL, eventAttempts, 3)}
	if _, err := second.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(second.store.gaveUp) != 1 || len(second.store.retried) != 0 {
		t.Errorf("a step's second failure: gave up %d, retried %d; want given up", len(second.store.gaveUp), len(second.store.retried))
	}

	answer := newSending(t)
	answer.store.due = []db.ClaimDueRepliesRow{due(rcv.URL, eventAttempts)}
	if _, err := answer.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(answer.store.retried) != 1 || len(answer.store.gaveUp) != 0 {
		t.Errorf("an answer failing as often was given up")
	}
}

func TestOnlyAnEventReplierTakesEvents(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(&relay{}, &stepRelay{})
	if err != nil {
		t.Fatal(err)
	}
	if reg.RepliesWithEvents("relay") || !reg.RepliesWithEvents("steps") || reg.RepliesWithEvents("nope") {
		t.Error("RepliesWithEvents does not follow the marker")
	}
}

type stepRelay struct{ relay }

func (*stepRelay) Name() string       { return "steps" }
func (*stepRelay) RepliesWithEvents() {}

// --- whose replies a sender claims, and how it sends them ---

// posted is a Poster that keeps what it was handed, and answers err.
type posted struct {
	mu       sync.Mutex
	channels []uuid.UUID
	reqs     []OutboundRequest
	err      error
}

func (p *posted) Post(_ context.Context, ch uuid.UUID, req OutboundRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.channels, p.reqs = append(p.channels, ch), append(p.reqs, req)
	return p.err
}

func senderWith(t *testing.T, s *sending, reg Registry, p Poster, scope Scope) *Sender {
	t.Helper()
	return NewSender(s.store, s.secrets, reg, p, scope, slog.New(slog.NewTextHandler(s.log, nil)))
}

// The worker claims every channel but a socket's, whose replies only the
// process holding the socket can send.
func TestTheWorkersSenderLeavesASocketsChannelsAlone(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	reg, err := NewRegistry(&relay{}, socketProvider{stubProvider{name: "whatsapp", routes: []Route{}, keys: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := senderWith(t, s, reg, &posted{}, Scope{}).Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, n := s.store.claims[0], s.store.noticeClaims[0]
	if r.HeldOnly || len(r.Sockets) != 1 || r.Sockets[0] != "whatsapp" {
		t.Errorf("replies claimed with %+v; want every channel but whatsapp's", r)
	}
	if n.HeldOnly || len(n.Sockets) != 1 || n.Sockets[0] != "whatsapp" {
		t.Errorf("notices claimed with %+v; want every channel but whatsapp's", n)
	}
}

// A socket's process claims the channels it holds and nothing else, read
// again at each claim, so a channel let go is not claimed after.
func TestASocketsSenderClaimsOnlyWhatItHolds(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	held := []uuid.UUID{uuid.New()}
	sender := senderWith(t, s, mustRegistry(t), &posted{}, Scope{Held: func() []uuid.UUID { return held }})
	if _, err := sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	held = nil
	if _, err := sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := s.store.claims; len(c) != 2 || !c[0].HeldOnly || len(c[0].Held) != 1 || !c[1].HeldOnly || len(c[1].Held) != 0 || c[0].Sockets != nil {
		t.Errorf("claims = %+v; want the held channel, then none, and never all channels", c)
	}
	if c := s.store.noticeClaims; len(c) != 2 || !c[0].HeldOnly || len(c[0].Held) != 1 {
		t.Errorf("notice claims = %+v; want the held channel", c)
	}
}

func mustRegistry(t *testing.T) Registry {
	t.Helper()
	reg, err := NewRegistry(&relay{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// Each part goes to the Poster with the channel it is for; a part the Poster
// can never send gives the reply up at once rather than retrying it.
func TestAPosterSendsEachPartForItsChannel(t *testing.T) {
	t.Parallel()

	s := newSending(t)
	s.relay.parts = 2
	row := due("https://example.com/r", 1)
	s.store.due = []db.ClaimDueRepliesRow{row}
	reg, err := NewRegistry(s.relay)
	if err != nil {
		t.Fatal(err)
	}
	p := &posted{}
	if n, err := senderWith(t, s, reg, p, Scope{}).Drain(t.Context()); err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if len(p.reqs) != 2 || p.channels[0] != row.ChannelID || p.channels[1] != row.ChannelID {
		t.Errorf("the poster got %d parts for %v; want 2 for %s", len(p.reqs), p.channels, row.ChannelID)
	}

	s2 := newSending(t)
	s2.store.due = []db.ClaimDueRepliesRow{due("https://example.com/r", 1)}
	never := &posted{err: fmt.Errorf("%w: not a WhatsApp address", ErrUnsendable)}
	if _, err := senderWith(t, s2, mustRegistry(t), never, Scope{}).Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s2.store.gaveUp) != 1 || len(s2.store.retried) != 0 {
		t.Errorf("gave up %d, retried %d; want an unsendable part given up at once", len(s2.store.gaveUp), len(s2.store.retried))
	}
}

// A provider that says no inside a 200, as Slack does, reads its own answer:
// Accepted is handed the body of a 2xx and its error is the post's, so a
// refusal it calls final gives up and any other is retried. A non-2xx never
// reaches it, and a request without one takes every 2xx.
func TestAPostReadsTheAnswerOfAProviderThatSaysNoInA200(t *testing.T) {
	t.Parallel()

	status, answer := http.StatusOK, `{"ok":false,"error":"not_in_channel"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	poster := HTTPPoster(&http.Client{Transport: &http.Transport{}})

	var read []string
	check := func(code int, body []byte) error {
		read = append(read, fmt.Sprint(code, " ", string(body)))
		if strings.Contains(string(body), `"ok":false`) {
			return fmt.Errorf("%w: refused", ErrUnsendable)
		}
		return nil
	}
	err := poster.Post(t.Context(), uuid.New(), OutboundRequest{URL: srv.URL, Accepted: check})
	if !errors.Is(err, ErrUnsendable) || len(read) != 1 || read[0] != "200 "+answer {
		t.Errorf("Post = %v after reading %q, want the refusal from the answer", err, read)
	}

	answer = `{"ok":true}`
	if err := poster.Post(t.Context(), uuid.New(), OutboundRequest{URL: srv.URL, Accepted: check}); err != nil {
		t.Errorf("an accepted post failed: %v", err)
	}

	status, answer, read = http.StatusInternalServerError, `{"ok":false}`, nil
	if err := poster.Post(t.Context(), uuid.New(), OutboundRequest{URL: srv.URL, Accepted: check}); err == nil || len(read) != 0 {
		t.Errorf("a 500 = %v, read %q; want it failed without asking the provider", err, read)
	}

	status, answer = http.StatusOK, `{"ok":false}`
	if err := poster.Post(t.Context(), uuid.New(), OutboundRequest{URL: srv.URL}); err != nil {
		t.Errorf("with nothing to read the answer, a 200 failed: %v", err)
	}
}

// --- a file a run sent ---

// fileRelay is a relay that also sends files.
type fileRelay struct{ relay }

func (*fileRelay) Name() string      { return "filer" }
func (*fileRelay) RepliesWithFiles() {}

// keptFiles is the attachment store: its objects by team and key, and err for
// every read.
type keptFiles struct {
	mu    sync.Mutex
	files map[string][]byte
	err   error
	reads int
}

func (s *keptFiles) Get(_ context.Context, teamID uuid.UUID, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	b, ok := s.files[teamID.String()+"/"+key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	}
	return b, nil
}

// fileSending is a sender of file replies through a fileRelay, with one file
// reply due whose file is kept.
func fileSending(t *testing.T) (*sending, *fileRelay, *keptFiles, db.ClaimDueRepliesRow, *receiver) {
	t.Helper()
	fr := &fileRelay{}
	s := newSending(t, fr)
	rcv := newReceiver(t, http.StatusOK)
	row := due(rcv.URL+"/replies", 1)
	row.Provider, row.Kind, row.Text, row.InReplyTo = "filer", "file", "report.pdf", nil
	row.Detail = []byte(`{"caption":"The Q3 numbers"}`)
	team := uuid.New()
	s.store.due = []db.ClaimDueRepliesRow{row}
	s.store.files = map[int64]db.GetReplyFileRow{row.ID: {
		TeamID: team, ObjectKey: "objects/f1", Filename: "report.pdf", MediaType: "application/pdf", SizeBytes: 9,
	}}
	objs := &keptFiles{files: map[string][]byte{team.String() + "/objects/f1": []byte("%PDF-1.7\n")}}
	s.sender.WithFiles(objs)
	return s, fr, objs, row, rcv
}

// The provider is handed the file's bytes, its name and type as kept, and
// the caption the run wrote, under the reply's own id; then it is sent.
func TestAFileReplyCarriesItsFileAndItsCaption(t *testing.T) {
	t.Parallel()

	s, fr, _, row, rcv := fileSending(t)
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fr.outs) != 1 {
		t.Fatalf("the provider was asked %d times", len(fr.outs))
	}
	out := fr.outs[0]
	want := &OutFile{Name: "report.pdf", MediaType: "application/pdf", Caption: "The Q3 numbers", Data: []byte("%PDF-1.7\n")}
	if out.Kind != "file" || out.Text != "report.pdf" || !reflect.DeepEqual(out.File, want) {
		t.Errorf("out = %+v, file %+v", out, out.File)
	}
	if wantID := row.RunID.String() + ":file:7"; out.ID != wantID {
		t.Errorf("id = %q, want %q", out.ID, wantID)
	}
	if rcv.got() != 1 || len(s.store.sent) != 1 || len(s.store.gaveUp)+len(s.store.retried) != 0 {
		t.Errorf("posted %d, marks %+v %+v %+v", rcv.got(), s.store.sent, s.store.gaveUp, s.store.retried)
	}
}

// A file gone since it was sent, its row or its object, has nothing to send
// however often it is tried: given up, and nothing posted.
func TestAFileGoneSinceIsGivenUp(t *testing.T) {
	t.Parallel()

	for name, gone := range map[string]func(*sending, *keptFiles){
		"its row":    func(s *sending, _ *keptFiles) { s.store.files = nil },
		"its object": func(_ *sending, o *keptFiles) { o.files = nil },
		// Bytes that cannot be read fail the same way on every attempt.
		"its bytes corrupt":    func(_ *sending, o *keptFiles) { o.err = fmt.Errorf("%w: k", objects.ErrCorrupt) },
		"another team's bytes": func(_ *sending, o *keptFiles) { o.err = fmt.Errorf("%w: k", objects.ErrWrongTeam) },
		"its bytes empty": func(_ *sending, o *keptFiles) {
			for k := range o.files {
				o.files[k] = nil
			}
		},
		"a caption that is not one": func(s *sending, _ *keptFiles) { s.store.due[0].Detail = []byte(`{"caption":5}`) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, fr, objs, _, rcv := fileSending(t)
			gone(s, objs)
			if _, err := s.sender.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(s.store.gaveUp) != 1 || len(s.store.retried) != 0 || rcv.got() != 0 || len(fr.outs) != 0 {
				t.Errorf("gave up %v, retried %v, posted %d, built %d", s.store.gaveUp, s.store.retried, rcv.got(), len(fr.outs))
			}
		})
	}
}

// A store that fails now may not later: the reply is tried again.
func TestAFileThatCannotBeReadNowIsTriedAgain(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]func(*sending, *keptFiles){
		"the row":    func(s *sending, _ *keptFiles) { s.store.fileErr = errors.New("connection reset") },
		"the object": func(_ *sending, o *keptFiles) { o.err = errors.New("bucket unreachable") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, objs, _, rcv := fileSending(t)
			fail(s, objs)
			if _, err := s.sender.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(s.store.retried) != 1 || len(s.store.gaveUp) != 0 || rcv.got() != 0 {
				t.Errorf("retried %v, gave up %v, posted %d", s.store.retried, s.store.gaveUp, rcv.got())
			}
		})
	}
}

// A file reply for a provider that sends no files, or a sender that reads
// none, can never be sent: given up before anything is read.
func TestAFileNobodyHereCanSendIsGivenUp(t *testing.T) {
	t.Parallel()

	t.Run("a provider that sends none", func(t *testing.T) {
		t.Parallel()
		s, _, objs, row, rcv := fileSending(t)
		row.Provider = "relay"
		reg, err := NewRegistry(&relay{})
		if err != nil {
			t.Fatal(err)
		}
		s.store.due = []db.ClaimDueRepliesRow{row}
		sender := senderWith(t, s, reg, HTTPPoster(&http.Client{}), Scope{}).WithFiles(objs)
		if _, err := sender.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(s.store.gaveUp) != 1 || rcv.got() != 0 || objs.reads != 0 || len(s.store.fileReads) != 0 {
			t.Errorf("gave up %v, posted %d, read %d objects and %d rows", s.store.gaveUp, rcv.got(), objs.reads, len(s.store.fileReads))
		}
	})
	t.Run("a sender with no files", func(t *testing.T) {
		t.Parallel()
		s, fr, _, row, rcv := fileSending(t)
		reg, err := NewRegistry(fr)
		if err != nil {
			t.Fatal(err)
		}
		s.store.due = []db.ClaimDueRepliesRow{row}
		sender := senderWith(t, s, reg, HTTPPoster(&http.Client{}), Scope{})
		if _, err := sender.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(s.store.gaveUp) != 1 || rcv.got() != 0 || len(s.store.fileReads) != 0 {
			t.Errorf("gave up %v, posted %d, read %d rows", s.store.gaveUp, rcv.got(), len(s.store.fileReads))
		}
	})
}

// Only a file reply reads a file: an answer is built from its row alone.
func TestAnAnswerReadsNoFile(t *testing.T) {
	t.Parallel()

	s, fr, objs, row, _ := fileSending(t)
	row.Kind, row.Detail = "answer", nil
	s.store.due = []db.ClaimDueRepliesRow{row}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.store.fileReads) != 0 || objs.reads != 0 || len(fr.outs) != 1 || fr.outs[0].File != nil {
		t.Errorf("read %d rows and %d objects; out %+v", len(s.store.fileReads), objs.reads, fr.outs)
	}
}

func TestOnlyAFileReplierSendsFiles(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(&relay{}, &fileRelay{})
	if err != nil {
		t.Fatal(err)
	}
	if reg.SendsFiles("relay") || !reg.SendsFiles("filer") || reg.SendsFiles("nope") {
		t.Error("SendsFiles does not follow the marker")
	}
}

// A file is tried twice, as a step is: one that cannot go must not hold the
// answer behind it through the answer's six.
func TestAFileIsTriedTwiceAtMost(t *testing.T) {
	t.Parallel()

	s, _, _, row, rcv := fileSending(t)
	rcv.Close()
	row.Attempts = eventAttempts
	s.store.due = []db.ClaimDueRepliesRow{row}
	if _, err := s.sender.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.store.gaveUp) != 1 || len(s.store.retried) != 0 {
		t.Errorf("a file's second failure: gave up %d, retried %d", len(s.store.gaveUp), len(s.store.retried))
	}
}

// A post's deadline grows with what it carries, from a text's ten seconds to
// a minute at most, so a 25 MiB file is not cut short and nothing waits long.
func TestAPostsDeadlineIsByItsSize(t *testing.T) {
	t.Parallel()

	for n, want := range map[int]time.Duration{
		0:           10 * time.Second,
		512<<10 - 1: 10 * time.Second,
		512 << 10:   11 * time.Second,
		10 << 20:    30 * time.Second,
		25 << 20:    maxPost,
		35_000_000:  maxPost,
	} {
		if got := PostTimeout(n); got != want {
			t.Errorf("PostTimeout(%d) = %v, want %v", n, got, want)
		}
	}
	if maxPost >= replyLease {
		t.Errorf("a post may take %v, past the %v lease it is sent under", maxPost, replyLease)
	}
}

// The deadline holds over a receiver that never answers, whatever the
// client's own timeout.
func TestAPostThatHangsMeetsItsDeadline(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := HTTPPoster(&http.Client{Transport: &http.Transport{}}).Post(ctx, uuid.New(), OutboundRequest{URL: srv.URL, Body: []byte("hi")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the post's deadline", err)
	}
	// Its own deadline, not the caller's thirty seconds.
	if took := time.Since(start); took < postBase-time.Second || took >= 25*time.Second {
		t.Errorf("gave up after %v, want about %v", took, postBase)
	}
}

// The poster keeps no client Timeout: one would cut a large file short
// however long its own deadline.
func TestAPostOutlastsItsClientsTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Timeout: 50 * time.Millisecond, Transport: &http.Transport{}}
	if err := HTTPPoster(client).Post(t.Context(), uuid.New(), OutboundRequest{URL: srv.URL, Body: []byte("hi")}); err != nil {
		t.Fatalf("Post: %v, want the client's timeout dropped", err)
	}
	if client.Timeout != 50*time.Millisecond {
		t.Errorf("the caller's client was changed: timeout %v", client.Timeout)
	}
}
