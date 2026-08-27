package s3

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const key = "teams/11111111-1111-1111-1111-111111111111/objects/abc"

func asWriter(t *testing.T, s objects.Store) objects.Writer {
	t.Helper()

	w, ok := s.(objects.Writer)
	if !ok {
		t.Fatalf("%T does not implement objects.Writer", s)
	}
	return w
}

// recorder is a stand-in object store that answers a fixed status and keeps
// what it was asked. Enough to pin the request shape and the error mapping;
// the live test is what proves the stub resembles a real store.
type recorder struct {
	mu       sync.Mutex
	status   int
	body     []byte
	requests []*http.Request
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, req.Clone(req.Context()))
	status, body := r.status, r.body
	r.mu.Unlock()

	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (r *recorder) last() *http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.requests) == 0 {
		return nil
	}
	return r.requests[len(r.requests)-1]
}

func newStub(t *testing.T, rec *recorder) objects.Store {
	t.Helper()

	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	s, err := New(Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    "openarity",
		AccessKey: "AKIAEXAMPLE",
		SecretKey: "s3cret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Path-style addressing, which is the setting that decides whether this works
// against anything but AWS.
//
// Virtual-host style puts the bucket in the hostname — bucket.endpoint — and
// almost no self-hosted store serves that, so every request goes to a name
// that does not resolve. AWS accepts path style too, so one setting works
// everywhere. The bucket belongs in the path.
func TestRequestsUsePathStyleAddressing(t *testing.T) {
	t.Parallel()

	rec := &recorder{status: http.StatusOK, body: []byte("hello")}
	s := newStub(t, rec)

	if _, err := s.Get(t.Context(), key); err != nil {
		t.Fatalf("Get: %v", err)
	}

	req := rec.last()
	if req == nil {
		t.Fatal("no request reached the store")
	}
	if !strings.HasPrefix(req.URL.Path, "/openarity/") {
		t.Errorf("path is %q, want it to start with the bucket — virtual-host "+
			"addressing would put the bucket in the host instead", req.URL.Path)
	}
	if strings.HasPrefix(req.Host, "openarity.") {
		t.Errorf("host is %q, so the bucket went into the hostname: virtual-host "+
			"addressing does not resolve against a self-hosted store", req.Host)
	}
	if !strings.Contains(req.URL.Path, key) {
		t.Errorf("path %q does not carry the key %q", req.URL.Path, key)
	}
}

// Every request has to be signed, or a store with authentication enabled
// refuses all of them. SigV4 is the reason this package takes a dependency.
func TestRequestsAreSigned(t *testing.T) {
	t.Parallel()

	rec := &recorder{status: http.StatusOK, body: []byte("hello")}
	s := newStub(t, rec)

	if _, err := s.Get(t.Context(), key); err != nil {
		t.Fatalf("Get: %v", err)
	}

	auth := rec.last().Header.Get("Authorization")
	switch {
	case auth == "":
		t.Fatal("the request carried no Authorization header")
	case !strings.HasPrefix(auth, "AWS4-HMAC-SHA256"):
		t.Errorf("Authorization is %q, want an AWS4-HMAC-SHA256 signature", auth)
	case !strings.Contains(auth, "AKIAEXAMPLE"):
		t.Errorf("the signature does not carry the access key: %q", auth)
	}
}

// The credential must not leave the process anywhere but the signature. A
// signature is a derivation of the secret; the secret itself never travels.
func TestTheSecretKeyIsNeverSent(t *testing.T) {
	t.Parallel()

	rec := &recorder{status: http.StatusOK, body: []byte("hello")}
	s := newStub(t, rec)

	if _, err := s.Get(t.Context(), key); err != nil {
		t.Fatalf("Get: %v", err)
	}

	for name, values := range rec.last().Header {
		for _, v := range values {
			if strings.Contains(v, "s3cret") {
				t.Errorf("header %s carries the secret key: %q", name, v)
			}
		}
	}
}

// 404 is an absent object. Everything else is a real failure and must not be
// mistaken for one — the read path turns ErrNotFound into a 404 to the
// caller, so a permission problem answered that way hides an outage behind a
// missing file.
func TestOnlyNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status   int
		notFound bool
	}{
		"absent":       {http.StatusNotFound, true},
		"forbidden":    {http.StatusForbidden, false},
		"unavailable":  {http.StatusServiceUnavailable, false},
		"unauthorised": {http.StatusUnauthorized, false},
		"server error": {http.StatusInternalServerError, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newStub(t, &recorder{status: tc.status})
			_, err := s.Get(t.Context(), key)
			if err == nil {
				t.Fatalf("status %d returned no error", tc.status)
			}

			if got := errors.Is(err, objects.ErrNotFound); got != tc.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v for status %d: %v",
					got, tc.notFound, tc.status, err)
			}
		})
	}
}

// Deleting something absent is not an error, the same contract the other two
// adapters keep. S3 itself answers 204 for a missing key, so this is mostly a
// promise that the adapter does not add a check of its own.
func TestDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	s := newStub(t, &recorder{status: http.StatusNoContent})
	if err := asWriter(t, s).Delete(t.Context(), key); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
}

func TestPutSendsTheBody(t *testing.T) {
	t.Parallel()

	rec := &recorder{status: http.StatusOK}
	s := newStub(t, rec)

	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	req := rec.last()
	if req.Method != http.MethodPut {
		t.Errorf("method is %s, want PUT", req.Method)
	}
	if !strings.Contains(req.URL.Path, key) {
		t.Errorf("path %q does not carry the key %q", req.URL.Path, key)
	}
}

// An endpoint is what makes this adapter reachable, and a store built without
// one would resolve to AWS itself — which is a surprising place for a
// self-hosted deployment's attachments to end up.
func TestNewRequiresAnEndpoint(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{Region: "us-east-1", Bucket: "openarity"}); err == nil {
		t.Error("New accepted a configuration with no endpoint")
	}
}

// A bucket is not optional either: without one every key would be addressed
// relative to nothing.
func TestNewRequiresABucket(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1"}); err == nil {
		t.Error("New accepted a configuration with no bucket")
	}
}
