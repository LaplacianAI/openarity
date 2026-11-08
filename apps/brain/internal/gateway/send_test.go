package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	mu   sync.Mutex
	outs []Outbound
	chs  []Channel
}

func (*relay) Name() string { return "relay" }

func (r *relay) Reply(ch Channel, out Outbound, creds Credentials, _ time.Time) (OutboundRequest, error) {
	r.mu.Lock()
	r.outs, r.chs = append(r.outs, out), append(r.chs, ch)
	r.mu.Unlock()
	if r.err != nil {
		return OutboundRequest{}, r.err
	}
	h := http.Header{}
	h.Set("X-Secret", creds.Get(KeySigning))
	return OutboundRequest{URL: ch.ReplyURL, Header: h, Body: []byte(out.Text)}, nil
}

// outbox hands out its due rows a batch at a time and records every mark.
type outbox struct {
	due      []db.ClaimDueRepliesRow
	claimErr error
	markErr  error

	claims  []db.ClaimDueRepliesParams
	sent    []db.ReplySentParams
	retried []db.RetryReplyParams
	gaveUp  []db.GiveUpReplyParams
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
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies, r.heads = append(r.bodies, string(body)), append(r.heads, req.Header.Clone())
		r.mu.Unlock()
		w.WriteHeader(status)
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
	client := &http.Client{
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
