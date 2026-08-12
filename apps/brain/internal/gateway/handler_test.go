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
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	testChannelID = "ch-1"
	testTeamID    = "t-1"
	testSecret    = "tok_A-1"
)

// The fakes live here and nowhere else — they satisfy the contracts and
// secrets interfaces so the round trip below is the real handler, the real
// adapter and nothing unbuilt.

type failingStore struct{ err error }

func (f failingStore) Get(context.Context, string) (string, error) { return "", f.err }

type fakeSink struct {
	msgs []contracts.Message
	err  error
}

func (s *fakeSink) Deliver(_ context.Context, msg contracts.Message) error {
	if s.err != nil {
		return s.err
	}
	s.msgs = append(s.msgs, msg)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func mustChannelPath(t *testing.T, teamID, channelID string) string {
	t.Helper()

	path, err := secrets.ChannelPath(teamID, channelID)
	if err != nil {
		t.Fatalf("ChannelPath(%q, %q): %v", teamID, channelID, err)
	}
	return path
}

// The helpers below are Telegram-prefixed like the fixtures in
// telegram_test.go: channel-neutral names would be taken the day
// slack_test.go needs its own.
func telegramStore(t *testing.T) secrets.Static {
	t.Helper()
	return secrets.Static{mustChannelPath(t, testTeamID, testChannelID): testSecret}
}

// mount registers the handler on a fresh mux, the way the server does.
func mount(g *Handler) http.Handler {
	mux := http.NewServeMux()
	g.Register(mux)
	return mux
}

func newTelegramHandler(store secrets.Store, sink contracts.Sink) http.Handler {
	return mount(New(discardLogger(), Telegram{}, map[string]string{testChannelID: testTeamID}, store, sink))
}

func post(t *testing.T, h http.Handler, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set(secretTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func telegramWebhookPath() string { return "/webhook/telegram/" + testChannelID }

// The whole point, end to end: a real Telegram update through the real
// handler and adapter lands in the sink as exactly one exact Message, with
// the wiring-derived fields stamped by the handler.
func TestWebhookDeliversARealUpdate(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	rec := post(t, newTelegramHandler(telegramStore(t), sink), telegramWebhookPath(), telegramUpdateJSON, testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(sink.msgs) != 1 {
		t.Fatalf("sink got %d messages, want 1", len(sink.msgs))
	}

	got := sink.msgs[0]
	if !got.SentAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("SentAt = %v, want unix 1700000000", got.SentAt)
	}
	got.SentAt = time.Time{}

	want := contracts.Message{
		Channel:           "telegram",
		ChannelID:         testChannelID,
		TeamID:            testTeamID,
		ConversationID:    "-1001234567890",
		ProviderMessageID: "42",
		ProviderUserID:    "5561234567",
		Text:              "deploy the thing",
	}
	if got != want {
		t.Errorf("delivered = %+v, want %+v", got, want)
	}
}

// The mux pattern comes from the adapter, not a literal — a second channel
// is New with its adapter and a registration, no handler edit. The fake
// pins that: its route serves, and Telegram's route does not exist on it.
func TestNewRoutesByTheAdaptersChannel(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	h := mount(New(discardLogger(), fakeAdapter{}, map[string]string{testChannelID: testTeamID}, telegramStore(t), sink))

	if rec := post(t, h, "/webhook/fake/"+testChannelID, `{}`, ""); rec.Code != http.StatusOK {
		t.Errorf("the fake adapter's route = %d, want 200", rec.Code)
	}
	if len(sink.msgs) != 1 {
		t.Errorf("sink got %d messages, want 1", len(sink.msgs))
	}
	if rec := post(t, h, telegramWebhookPath(), `{}`, ""); rec.Code != http.StatusNotFound {
		t.Errorf("telegram's route on a fake-adapter handler = %d, want 404", rec.Code)
	}
}

type fakeAdapter struct{}

func (fakeAdapter) Channel() string                            { return "fake" }
func (fakeAdapter) Verify(*http.Request, []byte, string) error { return nil }
func (fakeAdapter) Parse([]byte) (contracts.Message, error) {
	return contracts.Message{
		ConversationID:    "c-1",
		ProviderMessageID: "m-1",
		ProviderUserID:    "u-1",
		Text:              "hi",
		SentAt:            time.Unix(1700000000, 0).UTC(),
	}, nil
}

// New copies the registry: a caller mutating its map afterwards must not
// race — or change — the per-request lookups.
func TestNewCopiesTheChannelRegistry(t *testing.T) {
	t.Parallel()

	channels := map[string]string{testChannelID: testTeamID}
	sink := &fakeSink{}
	h := mount(New(discardLogger(), Telegram{}, channels, telegramStore(t), sink))

	delete(channels, testChannelID)

	if rec := post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret); rec.Code != http.StatusOK {
		t.Errorf("status = %d after mutating the caller's map, want 200", rec.Code)
	}
	if len(sink.msgs) != 1 {
		t.Errorf("sink got %d messages, want 1", len(sink.msgs))
	}
}

// Authentication failures are 401 and the sink is never touched. Every case
// shares one shape: whatever went wrong, nothing unverified gets delivered.
func TestWebhookRejectsUnauthenticatedRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		store    secrets.Store
		channels map[string]string
		path     string
		token    string
	}{
		"wrong token":          {telegramStore(t), nil, telegramWebhookPath(), "wrong"},
		"missing token":        {telegramStore(t), nil, telegramWebhookPath(), ""},
		"unknown channel":      {telegramStore(t), nil, "/webhook/telegram/nope", testSecret},
		"no secret configured": {secrets.Static{}, nil, telegramWebhookPath(), testSecret},
		"unusable stored secret": {
			secrets.Static{mustChannelPath(t, testTeamID, testChannelID): "123456:AAHtoken"},
			nil, telegramWebhookPath(), "123456:AAHtoken",
		},
		// %2F..%2F decodes to a path-traversal attempt in PathValue. It must
		// die at the channel lookup — the secret path is composed from the
		// registration, never from the URL.
		"escaped path traversal": {telegramStore(t), nil, "/webhook/telegram/" + testChannelID + "%2F..%2Fother", testSecret},
		// A registration whose team cannot form a secret path fails closed
		// before the store is asked — a poisoned channels-table row must not
		// read across the team namespace.
		"unusable registration": {
			telegramStore(t),
			map[string]string{testChannelID: "../" + testTeamID},
			telegramWebhookPath(), testSecret,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			channels := tc.channels
			if channels == nil {
				channels = map[string]string{testChannelID: testTeamID}
			}
			h := mount(New(discardLogger(), Telegram{}, channels, tc.store, sink))
			rec := post(t, h, tc.path, telegramUpdateJSON, tc.token)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if len(sink.msgs) != 0 {
				t.Errorf("sink was called %d times for an unauthenticated request", len(sink.msgs))
			}
		})
	}
}

// A store outage is transient: 503 so Telegram redelivers once Vault is back.
// A missing secret is 401 — the distinction is the difference between "sealed
// Vault at 3am" and "channel never provisioned".
func TestWebhookStoreOutageIs503(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	rec := post(t, newTelegramHandler(failingStore{err: errors.New("vault sealed")}, sink),
		telegramWebhookPath(), telegramUpdateJSON, testSecret)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("sink was called during a store outage")
	}
}

// Verified garbage is acked with 200 and dropped: Telegram retries every
// non-2xx for 24 hours, so answering 4xx to a body that will never parse is
// a self-inflicted retry storm. "Rejected" in the attack list means the sink
// never sees it — not that the provider gets an error code.
func TestWebhookAcksAndDropsUndeliverablePayloads(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"malformed json": `secret-token=hunter2`,
		"empty body":     ``,
		"null":           `null`,
		"empty object":   `{}`,
		"edited message": `{"update_id": 1, "edited_message": {"message_id": 1}}`,
		"bot author":     `{"message": {"message_id": 1, "from": {"id": 2, "is_bot": true}, "chat": {"id": 3}, "date": 4, "text": "x"}}`,
		"no text":        `{"message": {"message_id": 1, "from": {"id": 2}, "chat": {"id": 3}, "date": 4}}`,
		"oversized body": `{"pad": "` + strings.Repeat("a", maxBodyBytes+1) + `"}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			rec := post(t, newTelegramHandler(telegramStore(t), sink), telegramWebhookPath(), body, testSecret)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 ack-and-drop", rec.Code)
			}
			if len(sink.msgs) != 0 {
				t.Errorf("sink got %d messages for an undeliverable payload", len(sink.msgs))
			}
		})
	}
}

// The logging middleware's wrapper hides the response writer from net/http's
// oversize detection, so the handler closes the connection itself — without
// it, net/http drains the unread body and keeps serving the attacker's
// connection. The drop is also the one silent permanent loss of a possibly
// legitimate message, so it logs above Info.
func TestWebhookOversizedBodyClosesTheConnection(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID}, telegramStore(t), &fakeSink{}))

	body := `{"pad": "` + strings.Repeat("a", maxBodyBytes+1) + `"}`
	rec := post(t, h, telegramWebhookPath(), body, testSecret)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 ack-and-drop", rec.Code)
	}
	if got := rec.Header().Get("Connection"); got != "close" {
		t.Errorf("Connection header = %q, want close", got)
	}
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("oversized drop was not logged at WARN: %s", buf.String())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// A body that dies mid-read is the connection failing, not the payload —
// 503 so the provider redelivers, and never a 4xx that blames the client
// for the network. It logs as "aborted" at WARN, never ERROR: the branch
// is reachable without authentication, so an attacker hanging up
// mid-request must not be able to drive the level operators page on.
func TestWebhookBodyReadFailureIsAborted503(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	sink := &fakeSink{}
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID}, telegramStore(t), sink))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, telegramWebhookPath(), errReader{})
	req.Header.Set(secretTokenHeader, testSecret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("sink was called despite the body never arriving")
	}
	if !strings.Contains(buf.String(), `"outcome":"aborted"`) {
		t.Errorf("outcome not logged as aborted: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("an unauthenticated client abort was logged at ERROR: %s", buf.String())
	}
}

// A failing sink is transient — 503, so the update is redelivered rather
// than lost. The cause reaches the log: during an outage, "delivery failed"
// with no error attached is indistinguishable from every other failure.
func TestWebhookSinkFailureIs503AndLogsTheCause(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID},
		telegramStore(t), &fakeSink{err: errors.New("bus full")}))

	rec := post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(buf.String(), "bus full") {
		t.Errorf("the delivery error did not reach the log: %s", buf.String())
	}
}

// Pins that the gateway does not dedup: the same update delivered twice
// reaches the sink twice. At-least-once is the Sink contract; dedup belongs
// downstream, keyed on (Channel, ChannelID, ConversationID,
// ProviderMessageID).
func TestWebhookDeliversDuplicateUpdatesTwice(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	h := newTelegramHandler(telegramStore(t), sink)

	for range 2 {
		if rec := post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if len(sink.msgs) != 2 {
		t.Errorf("sink got %d messages, want 2 — the gateway must not dedup", len(sink.msgs))
	}
}

// Pins what Telegram's scheme can and cannot do: the token authenticates the
// sender, not the bytes, so a mutated body with a valid token still delivers.
// Do not "fix" this test — body integrity requires an HMAC channel (Slack,
// WhatsApp), and pretending Telegram has it would be a lie in the test suite.
func TestWebhookVerifiesTheSenderNotTheBytes(t *testing.T) {
	t.Parallel()

	mutated := strings.Replace(telegramUpdateJSON, "deploy the thing", "drop the tables", 1)

	sink := &fakeSink{}
	rec := post(t, newTelegramHandler(telegramStore(t), sink), telegramWebhookPath(), mutated, testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(sink.msgs) != 1 || sink.msgs[0].Text != "drop the tables" {
		t.Errorf("mutated-but-authenticated body was not delivered: %+v", sink.msgs)
	}
}

// The route is method-scoped and single-segment; everything else on the
// gateway's mux is 404/405. In production the server mux owns /healthz and
// /readyz on both listeners, so probe paths never reach this mux at all —
// server/webhook.go pins that boundary.
func TestWebhookRoutingEdges(t *testing.T) {
	t.Parallel()

	h := newTelegramHandler(telegramStore(t), &fakeSink{})

	tests := map[string]struct {
		method, path string
		want         int
	}{
		"GET on the webhook route": {http.MethodGet, telegramWebhookPath(), http.StatusMethodNotAllowed},
		"missing channel segment":  {http.MethodPost, "/webhook/telegram/", http.StatusNotFound},
		"extra path segment":       {http.MethodPost, telegramWebhookPath() + "/extra", http.StatusNotFound},
		"unknown path":             {http.MethodGet, "/", http.StatusNotFound},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}

// One exit line per request, fields only: outcome for querying, reason for
// the ignorable cases. The middleware owns method/path/status; this line
// owns why.
func TestWebhookLogsTheOutcome(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID}, telegramStore(t), &fakeSink{}))

	post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret)
	if !strings.Contains(buf.String(), `"outcome":"delivered"`) {
		t.Errorf("delivered outcome not logged: %s", buf.String())
	}

	buf.Reset()
	post(t, h, telegramWebhookPath(), `{"update_id": 1, "edited_message": {"message_id": 1}}`, testSecret)
	out := buf.String()
	if !strings.Contains(out, `"outcome":"ignored"`) || !strings.Contains(out, "not a message update") {
		t.Errorf("ignored outcome and reason not logged: %s", out)
	}
}

// The secret must never reach a log record — not on success, not on any
// failure path.
func TestWebhookNeverLogsTheSecret(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID}, telegramStore(t), &fakeSink{}))

	post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret)
	post(t, h, telegramWebhookPath(), telegramUpdateJSON, "wrong")
	post(t, h, telegramWebhookPath(), `not json`, testSecret)

	if strings.Contains(buf.String(), testSecret) {
		t.Errorf("the secret appeared in a log record: %s", buf.String())
	}
}

// Attacker-controlled path values are logged as capped fields. A kilobyte
// channel id must not become a kilobyte log line.
func TestWebhookCapsTheLoggedChannelID(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{}, secrets.Static{}, &fakeSink{}))

	long := strings.Repeat("x", 4096)
	post(t, h, "/webhook/telegram/"+long, telegramUpdateJSON, testSecret)

	if strings.Contains(buf.String(), long) {
		t.Error("an uncapped channel id reached the log")
	}
	if !strings.Contains(buf.String(), strings.Repeat("x", maxLoggedField)) {
		t.Errorf("the capped channel id is missing from the log: %s", buf.String())
	}
}

// The cap cuts on a rune boundary: a multi-byte character straddling the
// limit is dropped whole, never split into invalid UTF-8 — the whole point
// of clipField is keeping attacker-controlled input log-safe.
func TestClipFieldCutsOnARuneBoundary(t *testing.T) {
	t.Parallel()

	// 63 ASCII bytes, then a 3-byte rune straddling the 64-byte limit.
	s := strings.Repeat("a", 63) + "€"
	got := clipField(s, maxLoggedField)

	if !utf8.ValidString(got) {
		t.Fatalf("clipField produced invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("a", 63); got != want {
		t.Errorf("clipField = %q, want the straddling rune dropped whole", got)
	}
}

// The ErrSecretUnusable sentinel exists so a misconfigured secret is
// tellable apart from a plain verification failure — pin the distinct
// reason at the handler, or deleting that branch passes every other test.
func TestWebhookLogsSecretUnusableAsItsOwnReason(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	store := secrets.Static{mustChannelPath(t, testTeamID, testChannelID): "123456:AAHtoken"}
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID}, store, &fakeSink{}))

	post(t, h, telegramWebhookPath(), telegramUpdateJSON, "123456:AAHtoken")

	if !strings.Contains(buf.String(), `"reason":"secret unusable"`) {
		t.Errorf("the secret-unusable reason was not logged: %s", buf.String())
	}
}

// Every answer before verification must be identical whether or not the
// channel exists — the body is read before the channel lookup so an
// oversized post cannot be used to enumerate registered channel ids by
// status code (200 for registered vs 401 for unknown).
func TestWebhookOversizedBodyDoesNotRevealChannelExistence(t *testing.T) {
	t.Parallel()

	body := `{"pad": "` + strings.Repeat("a", maxBodyBytes+1) + `"}`

	for name, path := range map[string]string{
		"registered channel": telegramWebhookPath(),
		"unknown channel":    "/webhook/telegram/nope",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			rec := post(t, newTelegramHandler(telegramStore(t), sink), path, body, testSecret)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want the uniform 200 ack-and-drop", rec.Code)
			}
			if len(sink.msgs) != 0 {
				t.Errorf("sink got %d messages for an oversized body", len(sink.msgs))
			}
		})
	}
}

// Transient failures must be visible to level-based alerting: a sealed
// Vault or a failing sink is level=ERROR, not another Info line.
func TestWebhookLogsTransientFailuresAtError(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := mount(New(logger, Telegram{}, map[string]string{testChannelID: testTeamID},
		failingStore{err: errors.New("vault sealed")}, &fakeSink{}))

	post(t, h, telegramWebhookPath(), telegramUpdateJSON, testSecret)

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("a store outage was not logged at ERROR: %s", buf.String())
	}
}
