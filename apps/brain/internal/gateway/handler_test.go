package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const testSigHeader = "X-Test-Signature"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// hookProvider stands in for every adapter. The handler test must not import
// custom — that would be an import cycle, and it would also tie the request
// path to one adapter's signature scheme.
type hookProvider struct {
	name   string
	routes []Route
	keys   []string

	result   Result
	parseErr error

	verifies int
	parses   int
	sawReq   WebhookRequest
	sawCreds Credentials

	verifiedBody []byte
}

func newHookProvider() *hookProvider {
	return &hookProvider{
		name:   "stub",
		routes: []Route{{Method: http.MethodPost}},
		keys:   []string{KeySigning},
		result: Result{Messages: []Inbound{validInbound()}},
	}
}

func validInbound() Inbound {
	return Inbound{
		ExternalID: "m-1",
		Author:     Author{Ref: "u-1", DisplayName: "Asha"},
		Session:    Session{Ref: "c-1", Kind: SessionDirect},
		Text:       "what's our deploy status?",
	}
}

func (p *hookProvider) Name() string    { return p.name }
func (p *hookProvider) Routes() []Route { return p.routes }
func (p *hookProvider) Keys() []string  { return p.keys }

func (p *hookProvider) Verify(req WebhookRequest, creds Credentials) error {
	p.verifies++
	p.sawCreds = creds
	p.verifiedBody = req.Body

	secret := creds.Get(KeySigning)
	if secret == "" {
		return errors.New("stub: no signing secret")
	}
	if req.Header.Get(testSigHeader) != secret {
		return errors.New("stub: bad signature")
	}
	return nil
}

func (p *hookProvider) Parse(req WebhookRequest) (Result, error) {
	p.parses++
	p.sawReq = req
	if p.parseErr != nil {
		return Result{}, p.parseErr
	}
	return p.result, nil
}

type fakeStore struct {
	channel db.Channel
	getErr  error

	senders   map[string]uuid.UUID
	findErr   error
	recordErr error

	gets    int
	pending []db.RecordPendingSenderParams

	// bodyDrained is what the caller's request body reader sets, so a test can
	// assert the order the handler did things in.
	bodyDrained  *bool
	drainedOnGet bool
}

func (s *fakeStore) GetChannel(_ context.Context, id uuid.UUID) (db.Channel, error) {
	s.gets++
	if s.bodyDrained != nil {
		s.drainedOnGet = *s.bodyDrained
	}
	if s.getErr != nil {
		return db.Channel{}, s.getErr
	}
	if id != s.channel.ID {
		return db.Channel{}, pgx.ErrNoRows
	}
	return s.channel, nil
}

func (s *fakeStore) FindChannelSender(_ context.Context, arg db.FindChannelSenderParams) (uuid.UUID, error) {
	if s.findErr != nil {
		return uuid.Nil, s.findErr
	}
	if id, ok := s.senders[arg.SenderRef]; ok {
		return id, nil
	}
	return uuid.Nil, pgx.ErrNoRows
}

func (s *fakeStore) RecordPendingSender(_ context.Context, arg db.RecordPendingSenderParams) (int64, error) {
	if s.recordErr != nil {
		return 0, s.recordErr
	}
	s.pending = append(s.pending, arg)
	return 1, nil
}

type fakeSecrets struct {
	values map[string]string
	err    error

	paths []string
	keys  []string
}

func (f *fakeSecrets) Get(_ context.Context, path, key string) (string, error) {
	f.paths = append(f.paths, path)
	f.keys = append(f.keys, key)
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.values[key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

type fakeSink struct {
	err error

	batches [][]Delivery
	channel Channel
}

func (f *fakeSink) Deliver(_ context.Context, ch Channel, msgs []Delivery) error {
	f.channel = ch
	f.batches = append(f.batches, msgs)
	return f.err
}

func (f *fakeSink) delivered() []Delivery {
	var out []Delivery
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

const testSecret = "oawh_test"

type harness struct {
	provider *hookProvider
	store    *fakeStore
	secrets  *fakeSecrets
	sink     *fakeSink
	mux      *http.ServeMux

	channelID uuid.UUID
	teamID    uuid.UUID
	userID    uuid.UUID
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		provider:  newHookProvider(),
		channelID: uuid.New(),
		teamID:    uuid.New(),
		userID:    uuid.New(),
	}

	h.store = &fakeStore{
		channel: db.Channel{ID: h.channelID, TeamID: h.teamID, Provider: "stub", Name: "support"},
		senders: map[string]uuid.UUID{"u-1": h.userID},
	}
	h.secrets = &fakeSecrets{values: map[string]string{KeySigning: testSecret}}
	h.sink = &fakeSink{}

	h.mount(t)
	return h
}

// mount rebuilds the router, so a test that changes the provider's routes can
// call it again.
func (h *harness) mount(t *testing.T) {
	t.Helper()

	registry, err := NewRegistry(h.provider)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	h.mux = http.NewServeMux()
	New(discardLogger(), h.store, h.secrets, registry, h.sink).Register(h.mux, nil)
}

func (h *harness) path() string {
	return "/hooks/stub/" + h.channelID.String()
}

// send is the whole request path: a signed POST to this harness's channel.
func (h *harness) send(t *testing.T, body string, signed bool) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, h.request(t, http.MethodPost, h.path(), strings.NewReader(body), signed))
}

func (h *harness) request(t *testing.T, method, path string, body io.Reader, signed bool) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, body)
	if signed {
		req.Header.Set(testSigHeader, testSecret)
	}
	return req
}

func (h *harness) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// A signed delivery from a known sender is the entire point of the package.
func TestASignedDeliveryReachesTheSink(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if rec := h.send(t, `{"id":"m-1"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := h.sink.delivered()
	if len(got) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(got))
	}
	if got[0].ExternalID != "m-1" {
		t.Errorf("ExternalID = %q, want m-1", got[0].ExternalID)
	}
	if got[0].UserID != h.userID {
		t.Errorf("UserID = %s, want the resolved sender %s", got[0].UserID, h.userID)
	}
	if h.sink.channel.ID != h.channelID {
		t.Errorf("delivered to channel %s, want %s", h.sink.channel.ID, h.channelID)
	}

	// The team travels with it. Without it the sink cannot write a session,
	// and authorising one later would need a second lookup of the channel.
	if h.sink.channel.TeamID != h.teamID {
		t.Errorf("delivered with team %s, want %s", h.sink.channel.TeamID, h.teamID)
	}
}

// A provider retries on any non-2xx, so refusing must be reserved for a
// request we genuinely cannot accept. Everything we choose to drop is a 200.
func TestOnlyABadSignatureIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		setup    func(*harness)
		signed   bool
		wantCode int
	}{
		"a signed message": {
			signed: true, wantCode: http.StatusOK,
		},
		"a forged signature": {
			signed: false, wantCode: http.StatusForbidden,
		},
		"a result with no messages": {
			setup:  func(h *harness) { h.provider.result = Result{} },
			signed: true, wantCode: http.StatusOK,
		},
		"an unknown sender": {
			setup:  func(h *harness) { h.store.senders = nil },
			signed: true, wantCode: http.StatusOK,
		},
		"a payload that does not parse": {
			setup:  func(h *harness) { h.provider.parseErr = errors.New("not json") },
			signed: true, wantCode: http.StatusOK,
		},
		"a message the adapter built wrong": {
			setup: func(h *harness) {
				bad := validInbound()
				bad.ExternalID = ""
				h.provider.result = Result{Messages: []Inbound{bad}}
			},
			signed: true, wantCode: http.StatusOK,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}

			if rec := h.send(t, `{}`, tc.signed); rec.Code != tc.wantCode {
				t.Errorf("%s = %d, want %d: %s", name, rec.Code, tc.wantCode, rec.Body)
			}
		})
	}
}

// Parse must never run on bytes Verify has not accepted, or a forged request
// reaches a JSON decoder.
func TestParseIsNotCalledOnAForgedRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.send(t, `{}`, false)

	if h.provider.parses != 0 {
		t.Errorf("Parse ran %d times on an unverified body, want 0", h.provider.parses)
	}
	if len(h.sink.batches) != 0 {
		t.Error("a forged request reached the sink")
	}
}

// Nothing about the request may leak whether a channel id exists. The body is
// read first so a stranger cannot time the difference, and the answer is the
// same 403 a wrong signature gets.
func TestAnUnknownChannelIsRefusedAfterTheBodyIsRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	drained := false
	h.store.bodyDrained = &drained
	body := &watchedBody{data: []byte(`{"id":"m-1"}`), done: &drained}

	rec := h.do(t, h.request(t, http.MethodPost,
		"/hooks/stub/"+uuid.New().String(), body, true))

	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown channel = %d, want 403", rec.Code)
	}
	if !h.store.drainedOnGet {
		t.Error("the channel was looked up before the body was read — that is a timing oracle")
	}
}

// A malformed id is answered like a missing one. Telling the sender their
// uuid was the wrong shape is still a signal.
func TestAMalformedChannelIDIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	rec := h.do(t, h.request(t, http.MethodPost, "/hooks/stub/not-a-uuid", strings.NewReader(`{}`), true))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if h.store.gets != 0 {
		t.Error("an unparseable id reached the database")
	}
}

// The path chooses the adapter; the row says which adapter the channel was
// created for. A Slack channel id posted to the Telegram path is refused, and
// refused the same way a bad signature is.
func TestAChannelBelongingToAnotherProviderIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.channel.Provider = "telegram"

	if rec := h.send(t, `{}`, true); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if h.provider.verifies != 0 {
		t.Error("Verify ran for a channel belonging to another provider")
	}
}

// Fail closed. Verify must never be called with an empty secret, because
// hmac.Equal("", "") is true and a missing credential would become a
// forgery oracle.
func TestAnUnreachableSecretStoreIs503(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"the store is down":     secrets.ErrUnavailable,
		"the secret is missing": secrets.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.secrets.err = err

			if rec := h.send(t, `{}`, true); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if h.provider.verifies != 0 {
				t.Error("Verify ran without a credential")
			}
		})
	}
}

// A key the adapter declared but the store does not hold is our configuration
// problem, not the sender's — and it must not be handed to Verify as "".
func TestADeclaredKeyThatIsMissingIs503(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.provider.keys = []string{KeySigning, KeyVerifyToken}
	h.mount(t)

	if rec := h.send(t, `{}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if h.provider.verifies != 0 {
		t.Error("Verify ran with an incomplete credential set")
	}
}

// The adapter is given exactly the keys it declared, scoped to the channel in
// the URL. Anything wider would let one adapter read another channel's
// credentials.
func TestTheAdapterGetsOnlyTheKeysItDeclared(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.secrets.values[KeyVerifyToken] = "not-yours"

	h.send(t, `{}`, true)

	if got := h.provider.sawCreds; len(got) != 1 || got.Get(KeySigning) != testSecret {
		t.Errorf("credentials = %v, want only the declared %s", got, KeySigning)
	}

	want := secrets.Path(h.teamID, secrets.KindChannel, h.channelID)
	for _, path := range h.secrets.paths {
		if path != want {
			t.Errorf("read %q, want the channel's own path %q", path, want)
		}
	}
}

func TestAnOversizedBodyIs413(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	huge := strings.Repeat("a", maxBody+1)
	if rec := h.send(t, huge, true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", rec.Code)
	}
	if h.provider.verifies != 0 {
		t.Error("an oversized body reached Verify")
	}
}

// A body that stops arriving is not a body we can verify. Treating a short
// read as an empty body would hand Verify bytes the sender never finished
// sending.
func TestAnUnreadableBodyIs400(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	rec := h.do(t, h.request(t, http.MethodPost, h.path(), failingBody{}, true))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if h.provider.verifies != 0 {
		t.Error("a truncated body reached Verify")
	}
}

func TestABodyAtTheLimitIsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if rec := h.send(t, strings.Repeat("a", maxBody), true); rec.Code != http.StatusOK {
		t.Errorf("a body at the limit = %d, want 200", rec.Code)
	}
}

// Slack will not accept a webhook URL until the endpoint echoes its
// challenge, so Result.Ack has to reach the wire.
func TestResultAckIsEchoed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.provider.result = Result{Ack: []byte("challenge-value")}

	rec := h.send(t, `{}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "challenge-value" {
		t.Errorf("body = %q, want the handshake echo", rec.Body.String())
	}
}

// An adapter may acknowledge and deliver in the same request, so the ack must
// not depend on there being no messages.
func TestAnAckAndAMessageTogether(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.provider.result = Result{Messages: []Inbound{validInbound()}, Ack: []byte("ok")}

	rec := h.send(t, `{}`, true)
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want the ack", rec.Body.String())
	}
	if len(h.sink.delivered()) != 1 {
		t.Error("the message was dropped because there was an ack")
	}
}

// The adapter sees values, not the request. Anything it could reach through
// *http.Request — the socket, the context, the caller's identity — is
// deliberately not there.
func TestTheAdapterSeesTheRequestAsValues(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.provider.routes = []Route{{Method: http.MethodPost, Suffix: "/events"}}
	h.mount(t)

	before := time.Now()
	req := h.request(t, http.MethodPost, h.path()+"/events?team=T1", strings.NewReader(`{"id":"m-1"}`), true)
	req.Header.Set("X-Custom", "kept")
	h.do(t, req)

	got := h.provider.sawReq
	switch {
	case got.Method != http.MethodPost:
		t.Errorf("Method = %q", got.Method)
	case got.Suffix != "/events":
		t.Errorf("Suffix = %q, want the route's own suffix", got.Suffix)
	case string(got.Body) != `{"id":"m-1"}`:
		t.Errorf("Body = %q", got.Body)
	case got.Header.Get("X-Custom") != "kept":
		t.Error("headers were not passed through")
	case got.Query.Get("team") != "T1":
		t.Errorf("Query = %v", got.Query)
	}

	// ReceivedAt is the handler's clock. An adapter reaching for time.Now
	// cannot be tested for freshness, so the value has to arrive with the
	// request.
	if got.ReceivedAt.Before(before) || got.ReceivedAt.After(time.Now()) {
		t.Errorf("ReceivedAt = %v, want a time from this request", got.ReceivedAt)
	}
}

// Verify sees the same bytes Parse does. A middleware or a handler that
// decoded first would break every signature scheme, because a signature covers
// the bytes on the wire and not their meaning.
func TestVerifyAndParseSeeTheSameBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	body := `{"id":"m-1","text":"hi"}`
	h.send(t, body, true)

	if string(h.provider.verifiedBody) != body {
		t.Errorf("Verify saw %q, want the exact bytes %q", h.provider.verifiedBody, body)
	}
	if string(h.provider.sawReq.Body) != body {
		t.Errorf("Parse saw %q, want the exact bytes %q", h.provider.sawReq.Body, body)
	}
}

// An unapproved sender's message is dropped and their ref queued. The text
// they sent is never stored — that is the whole reason approval exists.
func TestAnUnknownSenderIsQueuedAndNotDelivered(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.senders = nil

	if rec := h.send(t, `{}`, true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(h.sink.batches) != 0 {
		t.Error("an unapproved sender's message reached the sink")
	}
	if len(h.store.pending) != 1 {
		t.Fatalf("queued %d senders, want 1", len(h.store.pending))
	}
	if h.store.pending[0].SenderRef != "u-1" {
		t.Errorf("queued %q", h.store.pending[0].SenderRef)
	}
}

// One unapproved sender in a batch must not drop the approved ones with them.
func TestAKnownSenderIsDeliveredAlongsideAnUnknownOne(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	stranger := validInbound()
	stranger.ExternalID = "m-2"
	stranger.Author.Ref = "u-2"
	h.provider.result = Result{Messages: []Inbound{validInbound(), stranger}}

	h.send(t, `{}`, true)

	got := h.sink.delivered()
	if len(got) != 1 || got[0].ExternalID != "m-1" {
		t.Errorf("delivered %d messages, want only the approved sender's", len(got))
	}
	if len(h.store.pending) != 1 {
		t.Errorf("queued %d senders, want the stranger", len(h.store.pending))
	}
}

// A message the adapter built wrong is dropped, not delivered. Validate is on
// the type, so the handler is the one place it can be enforced for every
// adapter at once.
func TestAnInvalidMessageIsDroppedAndTheRestSurvive(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	bad := validInbound()
	bad.ExternalID = ""
	good := validInbound()
	good.ExternalID = "m-2"
	h.provider.result = Result{Messages: []Inbound{bad, good}}

	if rec := h.send(t, `{}`, true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	got := h.sink.delivered()
	if len(got) != 1 || got[0].ExternalID != "m-2" {
		t.Errorf("delivered %v, want only the valid message", got)
	}
}

// An invalid message must not be queued as a pending sender either — an
// adapter bug would otherwise fill a channel's approval list.
func TestAnInvalidMessageIsNotResolved(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	bad := validInbound()
	bad.Session.Kind = "broadcast"
	h.provider.result = Result{Messages: []Inbound{bad}}

	h.send(t, `{}`, true)

	if len(h.store.pending) != 0 {
		t.Errorf("an unvalidated message queued %d senders", len(h.store.pending))
	}
}

// Our database being unreachable is not the sender's problem to interpret, and
// it is the one failure a retry actually fixes. Every write on this path is
// idempotent, so asking for the message again is free.
func TestADatabaseFailureAsksForARetry(t *testing.T) {
	t.Parallel()

	for name, setup := range map[string]func(*harness){
		"the channel lookup fails": func(h *harness) { h.store.getErr = errors.New("connection reset") },
		"resolving a sender fails": func(h *harness) { h.store.findErr = errors.New("connection reset") },
		"queueing a sender fails": func(h *harness) {
			h.store.senders = nil
			h.store.recordErr = errors.New("connection reset")
		},
		"the sink fails": func(h *harness) { h.sink.err = errors.New("connection reset") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			setup(h)

			if rec := h.send(t, `{}`, true); rec.Code != http.StatusInternalServerError {
				t.Errorf("%s = %d, want 500 so the provider sends it again", name, rec.Code)
			}
		})
	}
}

// A channel that has been deleted is gone, not broken. It must stay a 403
// rather than becoming the 500 a real database failure gets.
func TestADeletedChannelIsRefusedNotRetried(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.getErr = pgx.ErrNoRows

	if rec := h.send(t, `{}`, true); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// Every route an adapter declares is mounted, and the adapter is chosen by the
// path rather than by a lookup — so an unknown provider is a 404 from the mux
// before any query runs.
func TestEveryDeclaredRouteIsMounted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.provider.routes = []Route{
		{Method: http.MethodPost},
		{Method: http.MethodPost, Suffix: "/events"},
		{Method: http.MethodGet, Suffix: "/events"},
	}
	h.mount(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, h.path()},
		{http.MethodPost, h.path() + "/events"},
		{http.MethodGet, h.path() + "/events"},
	} {
		rec := h.do(t, h.request(t, tc.method, tc.path, strings.NewReader(`{}`), true))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s is not mounted", tc.method, tc.path)
		}
	}
}

func TestAnUnknownProviderIs404(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	rec := h.do(t, h.request(t, http.MethodPost,
		"/hooks/telegram/"+h.channelID.String(), strings.NewReader(`{}`), true))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if h.store.gets != 0 {
		t.Error("an unknown provider reached the database")
	}
}

// A method the adapter did not declare is not served. Registering the handler
// for every verb would let a GET with a query string carry a payload past a
// signature scheme that only covers the body.
func TestAnUndeclaredMethodIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	rec := h.do(t, h.request(t, http.MethodGet, h.path(), nil, true))
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("GET on a POST-only route = %d, want 405 or 404", rec.Code)
	}
}

// The routes exist to be registered on the webhook listener, which has no
// guard. A router that is not public would panic on a nil one.
func TestTheGatewayRouterIsPublic(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	registry, err := NewRegistry(h.provider)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if r := New(discardLogger(), h.store, h.secrets, registry, h.sink); !r.Public() {
		t.Error("the gateway router is not public, so registering it needs a guard it will never have")
	}
}

// failingBody is a client that stops sending part way through.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) {
	return 0, errors.New("connection reset by peer")
}

// watchedBody records the moment the last byte is read, which is how the
// ordering test above distinguishes "read then look up" from the reverse.
type watchedBody struct {
	data []byte
	pos  int
	done *bool
}

func (b *watchedBody) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		*b.done = true
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
