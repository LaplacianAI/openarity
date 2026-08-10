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

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	testChannelID = "ch-1"
	testTenantID  = "t-1"
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

func testStore() secrets.Static {
	return secrets.Static{secrets.ChannelPath(testTenantID, testChannelID): testSecret}
}

func newTestHandler(store secrets.SecretStore, sink contracts.Sink) http.Handler {
	return New(discardLogger(), Telegram{}, map[string]string{testChannelID: testTenantID}, store, sink)
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

func webhookPath() string { return "/webhook/telegram/" + testChannelID }

// The whole point, end to end: a real Telegram update through the real
// handler and adapter lands in the sink as exactly one exact Message, with
// the wiring-derived fields stamped by the handler.
func TestWebhookDeliversARealUpdate(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	rec := post(t, newTestHandler(testStore(), sink), webhookPath(), updateJSON, testSecret)

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
		TenantID:          testTenantID,
		ConversationID:    "-1001234567890",
		ProviderMessageID: "42",
		ProviderUserID:    "5561234567",
		Text:              "deploy the thing",
	}
	if got != want {
		t.Errorf("delivered = %+v, want %+v", got, want)
	}
}

// Authentication failures are 401 and the sink is never touched. Every case
// shares one shape: whatever went wrong, nothing unverified gets delivered.
func TestWebhookRejectsUnauthenticatedRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		store secrets.SecretStore
		path  string
		token string
	}{
		"wrong token":          {testStore(), webhookPath(), "wrong"},
		"missing token":        {testStore(), webhookPath(), ""},
		"unknown channel":      {testStore(), "/webhook/telegram/nope", testSecret},
		"no secret configured": {secrets.Static{}, webhookPath(), testSecret},
		"unusable stored secret": {
			secrets.Static{secrets.ChannelPath(testTenantID, testChannelID): "123456:AAHtoken"},
			webhookPath(), "123456:AAHtoken",
		},
		// %2F..%2F decodes to a path-traversal attempt in PathValue. It must
		// die at the channel lookup — the secret path is composed from the
		// registration, never from the URL.
		"escaped path traversal": {testStore(), "/webhook/telegram/" + testChannelID + "%2F..%2Fother", testSecret},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			rec := post(t, newTestHandler(tc.store, sink), tc.path, updateJSON, tc.token)

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
	rec := post(t, newTestHandler(failingStore{err: errors.New("vault sealed")}, sink),
		webhookPath(), updateJSON, testSecret)

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
			rec := post(t, newTestHandler(testStore(), sink), webhookPath(), body, testSecret)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 ack-and-drop", rec.Code)
			}
			if len(sink.msgs) != 0 {
				t.Errorf("sink got %d messages for an undeliverable payload", len(sink.msgs))
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// A body that dies mid-read is the connection failing, not the payload —
// 503 so the provider redelivers, and never a 4xx that blames the client
// for the network.
func TestWebhookBodyReadFailureIs503(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	h := newTestHandler(testStore(), sink)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhookPath(), errReader{})
	req.Header.Set(secretTokenHeader, testSecret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("sink was called despite the body never arriving")
	}
}

// A failing sink is transient — 503, so the update is redelivered rather
// than lost.
func TestWebhookSinkFailureIs503(t *testing.T) {
	t.Parallel()

	rec := post(t, newTestHandler(testStore(), &fakeSink{err: errors.New("bus full")}),
		webhookPath(), updateJSON, testSecret)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// Pins that the gateway does not dedup: the same update delivered twice
// reaches the sink twice. At-least-once is the Sink contract; dedup belongs
// downstream, keyed on (Channel, ChannelID, ConversationID,
// ProviderMessageID).
func TestWebhookDeliversDuplicateUpdatesTwice(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	h := newTestHandler(testStore(), sink)

	for range 2 {
		if rec := post(t, h, webhookPath(), updateJSON, testSecret); rec.Code != http.StatusOK {
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

	mutated := strings.Replace(updateJSON, "deploy the thing", "drop the tables", 1)

	sink := &fakeSink{}
	rec := post(t, newTestHandler(testStore(), sink), webhookPath(), mutated, testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(sink.msgs) != 1 || sink.msgs[0].Text != "drop the tables" {
		t.Errorf("mutated-but-authenticated body was not delivered: %+v", sink.msgs)
	}
}

// The route is method-scoped and single-segment. Everything else on the
// gateway's mux is 404/405 — including POST /healthz, which falls through
// from the server mux and must not look like a webhook.
func TestWebhookRoutingEdges(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testStore(), &fakeSink{})

	tests := map[string]struct {
		method, path string
		want         int
	}{
		"GET on the webhook route": {http.MethodGet, webhookPath(), http.StatusMethodNotAllowed},
		"missing channel segment":  {http.MethodPost, "/webhook/telegram/", http.StatusNotFound},
		"extra path segment":       {http.MethodPost, webhookPath() + "/extra", http.StatusNotFound},
		"POST healthz":             {http.MethodPost, "/healthz", http.StatusNotFound},
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
	h := New(logger, Telegram{}, map[string]string{testChannelID: testTenantID}, testStore(), &fakeSink{})

	post(t, h, webhookPath(), updateJSON, testSecret)
	if !strings.Contains(buf.String(), `"outcome":"delivered"`) {
		t.Errorf("delivered outcome not logged: %s", buf.String())
	}

	buf.Reset()
	post(t, h, webhookPath(), `{"update_id": 1, "edited_message": {"message_id": 1}}`, testSecret)
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
	h := New(logger, Telegram{}, map[string]string{testChannelID: testTenantID}, testStore(), &fakeSink{})

	post(t, h, webhookPath(), updateJSON, testSecret)
	post(t, h, webhookPath(), updateJSON, "wrong")
	post(t, h, webhookPath(), `not json`, testSecret)

	if strings.Contains(buf.String(), testSecret) {
		t.Errorf("the secret appeared in a log record: %s", buf.String())
	}
}

// Attacker-controlled path values are logged as capped fields. A kilobyte
// channel id must not become a kilobyte log line.
func TestWebhookCapsTheLoggedChannelID(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	h := New(logger, Telegram{}, map[string]string{}, secrets.Static{}, &fakeSink{})

	long := strings.Repeat("x", 4096)
	post(t, h, "/webhook/telegram/"+long, updateJSON, testSecret)

	if strings.Contains(buf.String(), long) {
		t.Error("an uncapped channel id reached the log")
	}
	if !strings.Contains(buf.String(), strings.Repeat("x", maxLoggedID)) {
		t.Errorf("the capped channel id is missing from the log: %s", buf.String())
	}
}
