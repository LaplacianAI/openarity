package secrets

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const loginPath = "/v1/auth/approle/login"

// loginBody is what a real OpenBao answers an AppRole login with. Verified
// against OpenBao 2.6.1 — the token is nested under "auth", not at the top.
const loginBody = `{"auth":{"client_token":"tok-1","lease_duration":60,"renewable":true}}`

// call is one request the stub saw. Recorded under a mutex because the
// handler runs on the server's goroutine and the assertions run on the
// test's; without it the race detector fails these tests, correctly.
type call struct {
	method string
	path   string
	token  string
	body   string
}

type baoStub struct {
	*httptest.Server

	mu     sync.Mutex
	calls  []call
	logins int
}

func (s *baoStub) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	defer s.mu.Unlock()

	if r.URL.Path == loginPath {
		s.logins++
	}
	s.calls = append(s.calls, call{
		method: r.Method,
		path:   r.URL.Path,
		token:  r.Header.Get("X-Vault-Token"),
		body:   string(body),
	})
}

func (s *baoStub) snapshot() ([]call, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...), s.logins
}

// last returns the last non-login call, which is the one under test.
func (s *baoStub) last(t *testing.T) call {
	t.Helper()

	calls, _ := s.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].path != loginPath {
			return calls[i]
		}
	}
	t.Fatal("the stub saw no call other than the login")
	return call{}
}

// newStub answers the login, then hands everything else to reply.
func newStub(t *testing.T, reply http.HandlerFunc) *baoStub {
	t.Helper()

	stub := &baoStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			stub.record(r)

			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == loginPath {
				_, _ = w.Write([]byte(loginBody))
				return
			}
			reply(w, r)
		}))
	t.Cleanup(stub.Close)
	return stub
}

// answering replies with one status and body to every read or write.
func answering(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func newTestBao(t *testing.T, stub *baoStub) Store {
	t.Helper()
	return NewOpenBao(stub.URL, "role", "secret-id", "secret", stub.Client())
}

// The constructor returns Store, so reaching the write and probe halves means
// asserting. A bare s.(Writer) would panic rather than fail, and errcheck
// rejects the single-value form for exactly that reason.
func asWriter(t *testing.T, s Store) Writer {
	t.Helper()

	w, ok := s.(Writer)
	if !ok {
		t.Fatalf("%T does not implement Writer", s)
	}
	return w
}

func asProber(t *testing.T, s Store) Prober {
	t.Helper()

	p, ok := s.(Prober)
	if !ok {
		t.Fatalf("%T does not implement Prober", s)
	}
	return p
}

func TestOpenBaoReadsAKVv2Value(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK,
		`{"data":{"data":{"signing_secret":"s3cr3t"}}}`))

	got, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "signing_secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Get = %q, want %q", got, "s3cr3t")
	}
}

// KV v2 puts a "data" segment in the URL that the caller never writes.
// Omitting it 404s, which is indistinguishable from a missing secret, so
// the mistake would look like a data problem rather than a code one.
func TestOpenBaoUsesTheKVv2DataSegment(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{"data":{"data":{"k":"v"}}}`))

	if _, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if want := "/v1/secret/data/teams/a/channels/b"; stub.last(t).path != want {
		t.Errorf("read path = %q, want %q", stub.last(t).path, want)
	}
}

// X-Bao-Token returns 403 on OpenBao; X-Vault-Token is the header that
// works on both it and Vault. Verified against both servers.
func TestOpenBaoSendsTheTokenFromTheLogin(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{"data":{"data":{"k":"v"}}}`))

	if _, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got := stub.last(t).token; got != "tok-1" {
		t.Errorf("X-Vault-Token = %q, want the token the login returned", got)
	}
}

// 404 carries an empty error list, so only the status code separates
// "absent" from "denied". Both fail closed, but only one is worth retrying,
// and only one should page an operator.
func TestOpenBaoMapsStatusCodesToSentinels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"absent", http.StatusNotFound, `{"errors":[]}`, ErrNotFound},
		{"denied", http.StatusForbidden, `{"errors":["permission denied"]}`, ErrUnavailable},
		{"sealed", http.StatusServiceUnavailable, `{"errors":["Vault is sealed"]}`, ErrUnavailable},
		{"broken", http.StatusInternalServerError, `{"errors":["internal error"]}`, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := newStub(t, answering(tc.status, tc.body))

			got, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "k")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if got != "" {
				t.Errorf("value = %q, want empty alongside the error", got)
			}
		})
	}
}

// A key absent from a document that exists is still absent. Returning ""
// with no error here is the failure this whole package exists to prevent:
// an empty signing secret verifies nothing and looks like it worked.
func TestOpenBaoMissingKeyIsNotFound(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{"data":{"data":{"other":"v"}}}`))

	if _, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A login that fails must not surface as a missing secret — that would send
// an operator looking for a row when the credential is what is wrong.
func TestOpenBaoLoginFailureIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":["invalid role or secret ID"]}`))
		}))
	t.Cleanup(srv.Close)

	store := NewOpenBao(srv.URL, "role", "wrong", "secret", srv.Client())
	if _, err := store.Get(t.Context(), "teams/a/channels/b", "k"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// An unreachable OpenBao is unavailable, not empty.
func TestOpenBaoUnreachableIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := srv.Client()
	addr := srv.URL
	srv.Close()

	store := NewOpenBao(addr, "role", "secret-id", "secret", client)
	if _, err := store.Get(t.Context(), "teams/a/channels/b", "k"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// One webhook must not become three round trips. The lease is 60s in the
// stub, so a second read inside the same test has to reuse the token.
func TestOpenBaoLogsInOncePerLease(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{"data":{"data":{"k":"v"}}}`))
	store := newTestBao(t, stub)

	for range 3 {
		if _, err := store.Get(t.Context(), "teams/a/channels/b", "k"); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}

	if _, logins := stub.snapshot(); logins != 1 {
		t.Errorf("logins = %d, want 1 — the token is not being reused", logins)
	}
}

// Ping is a separate call so readiness can say "the secret store is sealed"
// rather than "something is wrong". Sealed is 503; not initialised is 501.
func TestOpenBaoPingMapsHealth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"unsealed", http.StatusOK, false},
		{"not initialised", http.StatusNotImplemented, true},
		{"sealed", http.StatusServiceUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := newStub(t, answering(tc.status, `{}`))

			err := asProber(t, newTestBao(t, stub)).Ping(t.Context())
			if tc.wantErr && !errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Ping: %v", err)
			}
		})
	}
}

// sys/health is unauthenticated. Logging in to answer a readiness probe
// would make a probe fail for a reason that has nothing to do with health.
func TestOpenBaoPingDoesNotLogIn(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{}`))

	if err := asProber(t, newTestBao(t, stub)).Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if _, logins := stub.snapshot(); logins != 0 {
		t.Errorf("logins = %d, want 0 — Ping must not need a token", logins)
	}
}

func TestOpenBaoPutWrapsTheValueForKVv2(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{}`))

	if err := asWriter(t, newTestBao(t, stub)).Put(t.Context(), "teams/a/channels/b", "signing_secret", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got := stub.last(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if want := "/v1/secret/data/teams/a/channels/b"; got.path != want {
		t.Errorf("write path = %q, want %q", got.path, want)
	}

	// KV v2 rejects a bare object: the fields must sit under "data", and a
	// write that skips the wrapper stores the wrong shape rather than failing.
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("unmarshal the request body %q: %v", got.body, err)
	}
	if body.Data["signing_secret"] != "s3cr3t" {
		t.Errorf("body = %q, want the value under a data wrapper", got.body)
	}
}

// DELETE on the data path soft-deletes the newest version and leaves it
// recoverable with undelete — the credential is still live. Only the
// metadata path destroys it. The two are one word apart.
func TestOpenBaoDeleteDestroysTheMetadata(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusNoContent, ``))

	if err := asWriter(t, newTestBao(t, stub)).Delete(t.Context(), "teams/a/channels/b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got := stub.last(t)
	if got.method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", got.method)
	}
	if want := "/v1/secret/metadata/teams/a/channels/b"; got.path != want {
		t.Errorf("delete path = %q, want %q — the data path only soft-deletes", got.path, want)
	}
}

// A write that OpenBao refused must not return nil. Silently losing a
// channel's credential would surface much later as a webhook that cannot
// be verified.
func TestOpenBaoWriteFailuresAreErrors(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusForbidden, `{"errors":["permission denied"]}`))
	writer := asWriter(t, newTestBao(t, stub))

	if err := writer.Put(t.Context(), "teams/a/channels/b", "k", "v"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Put err = %v, want ErrUnavailable", err)
	}
	if err := writer.Delete(t.Context(), "teams/a/channels/b"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Delete err = %v, want ErrUnavailable", err)
	}
}

// A trailing slash on the address would produce "//v1/..." — most servers
// forgive it, so it survives to production and then breaks behind a proxy
// that does not.
func TestOpenBaoToleratesATrailingSlashOnTheAddress(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `{"data":{"data":{"k":"v"}}}`))

	store := NewOpenBao(stub.URL+"/", "role", "secret-id", "secret", stub.Client())
	if _, err := store.Get(t.Context(), "teams/a/channels/b", "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if want := "/v1/secret/data/teams/a/channels/b"; stub.last(t).path != want {
		t.Errorf("read path = %q, want %q", stub.last(t).path, want)
	}
}

// Nothing outside this package can construct the type, so the constructor's
// return value is the whole contract. If it stops satisfying one of these,
// the wiring in cmd/brain fails at a type assertion at startup rather than
// here.
func TestNewOpenBaoSatisfiesEveryInterface(t *testing.T) {
	t.Parallel()

	store := NewOpenBao("http://127.0.0.1:8200", "r", "s", "secret", nil)

	if _, ok := store.(Writer); !ok {
		t.Errorf("%T does not implement Writer", store)
	}
	if _, ok := store.(Prober); !ok {
		t.Errorf("%T does not implement Prober", store)
	}
}

// Static is deliberately not a Prober: a development brain with no OpenBao
// has nothing to reach, and readiness skips it rather than reporting a
// dependency that does not exist.
func TestStaticIsNotAProber(t *testing.T) {
	t.Parallel()

	var store Store = Static{}
	if _, ok := store.(Prober); ok {
		t.Error("Static implements Prober — readiness would probe an in-memory map")
	}
}

// A proxy or a captive portal in front of OpenBao answers 200 with HTML.
// Decoding that must be unavailable, not an empty secret.
func TestOpenBaoNonJSONBodyIsUnavailable(t *testing.T) {
	t.Parallel()

	stub := newStub(t, answering(http.StatusOK, `<html>gateway timeout</html>`))

	if _, err := newTestBao(t, stub).Get(t.Context(), "teams/a/channels/b", "k"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// A 200 with no token in it would otherwise be cached as an empty token and
// then sent as an empty header on every read, which OpenBao answers 403 to —
// a login failure reported as a permission problem for the whole lease.
func TestOpenBaoLoginWithoutATokenIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"auth":{"lease_duration":60}}`))
		}))
	t.Cleanup(srv.Close)

	store := NewOpenBao(srv.URL, "role", "secret-id", "secret", srv.Client())
	if _, err := store.Get(t.Context(), "teams/a/channels/b", "k"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}
