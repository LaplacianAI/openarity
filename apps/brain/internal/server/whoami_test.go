package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

// principalVerifier answers with whatever principal the test wants, so the
// response body can be checked against a known caller.
type principalVerifier struct {
	principal *auth.Principal
}

func (v principalVerifier) Verify(_ context.Context, _ string) (*auth.Principal, error) {
	return v.principal, nil
}

// apiFor builds the API mux with a verifier the test controls.
func apiFor(t *testing.T, v auth.Verifier) http.Handler {
	t.Helper()
	return New(testConfig(), discardLogger(), healthyDB(), v).apiHandler()
}

// decode reads a whoamiResponse, failing the test if the body is not the JSON
// the CLI will expect.
func decode(t *testing.T, body string) whoamiResponse {
	t.Helper()

	var got whoamiResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not valid JSON (%v): %s", err, body)
	}
	return got
}

func TestWhoamiRequiresAToken(t *testing.T) {
	t.Parallel()

	api := handlers(t, healthyDB())["api"]

	for _, tc := range []struct{ name, token, header string }{
		{name: "no header"},
		{name: "wrong token", token: "not-the-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := request(t, api, http.MethodGet, "/whoami", tc.token)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "subject") {
				t.Errorf("a rejected request received a principal: %s", rec.Body)
			}
		})
	}
}

func TestWhoamiReturnsTheAuthenticatedCaller(t *testing.T) {
	t.Parallel()

	want := &auth.Principal{
		Kind:    auth.KindUser,
		Issuer:  "https://idp.example.com",
		Subject: "user-42",
		Email:   "someone@example.com",
	}

	rec := request(t, apiFor(t, principalVerifier{principal: want}), http.MethodGet, "/whoami", "token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := decode(t, rec.Body.String())
	if got.Kind != string(want.Kind) || got.Issuer != want.Issuer ||
		got.Subject != want.Subject || got.Email != want.Email {
		t.Errorf("body = %+v, want %+v", got, *want)
	}
}

// A dev principal has no issuer and no email. Absent keys, not empty strings —
// a client rendering "" as a value shows a caller who has an issuer named
// nothing.
func TestWhoamiOmitsWhatADevPrincipalDoesNotHave(t *testing.T) {
	t.Parallel()

	v := principalVerifier{principal: &auth.Principal{Kind: auth.KindDev, Subject: "dev"}}
	rec := request(t, apiFor(t, v), http.MethodGet, "/whoami", "token")

	body := rec.Body.String()
	for _, absent := range []string{"issuer", "email"} {
		if strings.Contains(body, absent) {
			t.Errorf("body contains %q for a principal that has none: %s", absent, body)
		}
	}
	if got := decode(t, body); got.Kind != string(auth.KindDev) {
		t.Errorf("kind = %q, want %q", got.Kind, auth.KindDev)
	}
}

// The CLI parses this. A missing or wrong content type sends it down a text
// path and it never reaches the decoder.
//
// Read the header off Result(), not Header(). Header() is the live map the
// handler writes to, so a Set that lands after WriteHeader — too late to reach
// the wire — still appears there. Result() is the snapshot taken when the
// status was written, which is what a client actually receives.
func TestWhoamiIsJSON(t *testing.T) {
	t.Parallel()

	rec := request(t, handlers(t, healthyDB())["api"], http.MethodGet, "/whoami", testToken)

	got := rec.Result().Header.Get("Content-Type") //nolint:bodyclose // ResponseRecorder body needs no close
	if !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// The response is a wire contract, not the Principal struct. Fields Principal
// grows must not appear here by accident.
func TestWhoamiExposesOnlyTheContractedFields(t *testing.T) {
	t.Parallel()

	v := principalVerifier{principal: &auth.Principal{
		Kind: auth.KindUser, Issuer: "https://idp", Subject: "u1", Email: "a@b.c",
	}}
	rec := request(t, apiFor(t, v), http.MethodGet, "/whoami", "token")

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	allowed := map[string]bool{"kind": true, "issuer": true, "subject": true, "email": true}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("unexpected field %q in the response: %s", key, rec.Body)
		}
	}
}

// The method is part of the pattern, so POST /whoami is not the route. It
// falls through to the catch-all and is authenticated before it 404s.
func TestWhoamiIsGETOnly(t *testing.T) {
	t.Parallel()

	api := handlers(t, healthyDB())["api"]

	if rec := request(t, api, http.MethodPost, "/whoami", testToken); rec.Code == http.StatusOK {
		t.Errorf("POST /whoami returned 200: %s", rec.Body)
	}
	if rec := request(t, api, http.MethodPost, "/whoami", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /whoami without a token = %d, want 401", rec.Code)
	}
}

// Kubernetes probes the pod IP with no credentials. If the probes ever land
// behind authentication, every pod fails its readiness check and the
// Deployment never rolls.
func TestHealthProbesNeedNoToken(t *testing.T) {
	t.Parallel()

	for name, h := range handlers(t, healthyDB()) {
		for _, path := range []string{"/healthz", "/readyz"} {
			rec := request(t, h, http.MethodGet, path, "")
			if rec.Code != http.StatusOK {
				t.Errorf("%s GET %s without a token = %d, want 200: %s", name, path, rec.Code, rec.Body)
			}
		}
	}
}

// whoami is an API route. The webhook listener is public, and a principal
// endpoint there would answer to anyone who reached the pod.
func TestWhoamiIsNotOnTheWebhookListener(t *testing.T) {
	t.Parallel()

	rec := request(t, handlers(t, healthyDB())["webhook"], http.MethodGet, "/whoami", testToken)
	if rec.Code != http.StatusNotFound {
		t.Errorf("webhook GET /whoami = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// New builds the API handler inside itself, so the verifier has to be set
// before that happens. Assigning it afterwards leaves the middleware closed
// over a nil interface: it compiles, it starts, and the first request panics
// inside Verify. A 401 here proves the wiring order.
func TestApiHandlerUsesTheVerifierGivenToNew(t *testing.T) {
	t.Parallel()

	srv := New(testConfig(), discardLogger(), healthyDB(), testVerifier())

	if srv.verifier == nil {
		t.Fatal("New did not store the verifier")
	}
	if rec := request(t, srv.apiHandler(), http.MethodGet, "/whoami", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — the handler is not using the verifier", rec.Code)
	}
}

// The 500 branch is unreachable behind the middleware: the request cannot
// arrive without a principal. Call the handler directly to prove it answers
// 500 rather than panicking, and that it does not answer 401 — that would tell
// a client to fetch a new token when the bug is a route on the wrong mux.
func TestWhoamiReportsAServerErrorWithoutAPrincipal(t *testing.T) {
	t.Parallel()

	srv := New(testConfig(), discardLogger(), healthyDB(), testVerifier())
	rec := request(t, http.HandlerFunc(srv.whoami), http.MethodGet, "/whoami", "")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// failingWriter accepts headers and the status, then fails the body write —
// the shape of a client that disconnects mid-response.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failingWriter) WriteHeader(status int)    { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }

// By the time encoding fails the status is already on the wire, so there is
// nothing to send the client. It must be logged, not swallowed, and it must
// not panic — a panic in a handler takes the connection down without a reply.
func TestWriteJSONLogsAFailedBodyWrite(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, healthyDB(), testVerifier())

	w := &failingWriter{}
	srv.writeJSON(w, http.StatusOK, whoamiResponse{Subject: "u1"})

	if w.status != http.StatusOK {
		t.Errorf("status = %d, want 200 — it is written before the body fails", w.status)
	}
	if !strings.Contains(buf.String(), "connection reset") {
		t.Errorf("the write failure was not logged: %s", buf.String())
	}
}
