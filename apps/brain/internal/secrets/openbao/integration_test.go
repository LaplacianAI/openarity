package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// The stub in openbao_test.go encodes what this package believes OpenBao
// does. These tests are what check that belief. They skip unless a real
// server is reachable:
//
//	docker run -d --name openbao -p 8200:8200 \
//	  -e BAO_DEV_ROOT_TOKEN_ID=dev-root openbao/openbao:latest server -dev
//	export BRAIN_TEST_SECRETS_ADDR=http://127.0.0.1:8200
//	export BRAIN_TEST_SECRETS_TOKEN=dev-root

// liveClient is what every request to a real OpenBao goes through, and its
// transport is its own on purpose.
//
// httptest.Server.Close() calls CloseIdleConnections on http.DefaultTransport
// — "assume most users of httptest.Server will be using the standard
// transport, so help them out", in net/http's own words. The stub tests in
// this package create and close httptest servers while these tests are talking
// to a live server, and both were on the default transport. A connection
// checked out of the pool in that window fails with "http: CloseIdleConnections
// called", which net/http deliberately does not retry, and which reads as the
// live server having gone away.
//
// It surfaced once in CI as TestDeniedPathIsUnavailableAgainstRealOpenBao
// failing on a request that had nothing to do with what it was testing.
var liveClient = &http.Client{
	Timeout:   baoTimeout,
	Transport: &http.Transport{},
}

// Pointing liveClient back at the default transport would restore the flake
// and break nothing that runs in the normal case — the failure needs an
// httptest server to close in exactly the wrong microsecond. This is the only
// thing that would notice, and it needs no server of any kind.
func TestLiveClientDoesNotShareTheDefaultTransport(t *testing.T) {
	t.Parallel()

	switch liveClient.Transport {
	case nil:
		t.Error("liveClient has no transport of its own, so it uses " +
			"http.DefaultTransport — which httptest.Server.Close closes " +
			"idle connections on, mid-request")
	case http.DefaultTransport:
		t.Error("liveClient shares http.DefaultTransport with the stub tests, " +
			"so httptest.Server.Close will close its idle connections")
	}
}

func liveBao(t *testing.T) (addr, root string) {
	t.Helper()

	addr = os.Getenv("BRAIN_TEST_SECRETS_ADDR")
	root = os.Getenv("BRAIN_TEST_SECRETS_TOKEN")
	if addr == "" || root == "" {
		t.Skip("BRAIN_TEST_SECRETS_ADDR or BRAIN_TEST_SECRETS_TOKEN is not set")
	}
	return addr, root
}

// admin talks to a live OpenBao with the root token, to set up what the
// store under test then uses without any privilege of its own.
type admin struct {
	t     *testing.T
	addr  string
	token string
}

func (a admin) do(method, path string, payload any) map[string]any {
	a.t.Helper()

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			a.t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, a.addr+path, body)
	if err != nil {
		a.t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("X-Vault-Token", a.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := liveClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusInternalServerError {
		a.t.Fatalf("%s %s returned %d: %s", method, path, resp.StatusCode, raw)
	}

	var decoded struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &decoded)
	return decoded.Data
}

// grantSecretMount opens the whole KV mount. It is deliberately more
// permissive than the policy the brain deploys with, because the tests below
// are about the client — login, round trip, renewal, a denied mount — and a
// tight policy would make them fail for reasons that have nothing to do with
// what they check.
//
// It used to say it was what the brain's own policy looks like. That was true
// once, stopped being true when the deployed policy gained its write
// capability, and read as a verified claim the whole time. Whether the shipped
// policy actually serves the brain is now asked directly, against the file, in
// policy_integration_test.go — not inferred from this constant.
const grantSecretMount = `path "secret/*" ` +
	`{ capabilities = ["create","read","update","delete","list"] }`

// Enabling an auth method twice is a 400, and two tests doing it at once
// makes the dev server's in-memory storage fail the whole transaction.
var enableAppRole sync.Once

// appRole sets up an AppRole with the given policy and hands back its
// credentials. Doing it here rather than in a fixture is the point: the
// login path is the half a stub cannot prove.
//
// Names are per-test because these run in parallel and OpenBao's inmem
// backend fails a concurrent write to the same key with a 500, not a retry.
func appRole(t *testing.T, addr, root, policy string) (roleID, secretID string) {
	t.Helper()
	return appRoleWith(t, addr, root, policy, map[string]any{"token_ttl": "60s"})
}

func appRoleWith(
	t *testing.T, addr, root, policy string, role map[string]any,
) (roleID, secretID string) {
	t.Helper()

	a := admin{t: t, addr: addr, token: root}
	enableAppRole.Do(func() {
		a.do(http.MethodPost, "/v1/sys/auth/approle", map[string]string{"type": "approle"})
	})

	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))

	a.do(http.MethodPut, "/v1/sys/policies/acl/"+name,
		map[string]string{"policy": policy})

	role["token_policies"] = name
	a.do(http.MethodPost, "/v1/auth/approle/role/"+name, role)

	roleData := a.do(http.MethodGet, "/v1/auth/approle/role/"+name+"/role-id", nil)
	secretData := a.do(http.MethodPost, "/v1/auth/approle/role/"+name+"/secret-id", nil)

	roleID, _ = roleData["role_id"].(string)
	secretID, _ = secretData["secret_id"].(string)
	if roleID == "" || secretID == "" {
		t.Fatalf("AppRole setup produced role_id=%q secret_id=%q", roleID, secretID)
	}
	return roleID, secretID
}

func liveStore(t *testing.T) secrets.Store {
	t.Helper()

	addr, root := liveBao(t)
	roleID, secretID := appRole(t, addr, root, grantSecretMount)
	return New(addr, roleID, secretID, "secret", liveClient)
}

// Every other test asserts a failure direction. Without this one, a Ping
// that always errored would still pass the suite.
func TestPingSucceedsAgainstRealOpenBao(t *testing.T) {
	t.Parallel()

	addr, _ := liveBao(t)

	if err := asProber(t, New(addr, "", "", "secret", liveClient)).Ping(t.Context()); err != nil {
		t.Fatalf("Ping against a live OpenBao: %v", err)
	}
}

// The whole lifecycle against a real server: AppRole login, the KV v2 data
// segment, the write wrapper, and the metadata delete. Each of these is a
// place the stub could be wrong in the same way the code is.
func TestRoundTripAgainstRealOpenBao(t *testing.T) {
	t.Parallel()

	addr, root := liveBao(t)
	roleID, secretID := appRole(t, addr, root, grantSecretMount)
	store := New(addr, roleID, secretID, "secret", liveClient)

	path := secrets.Path(uuid.New(), secrets.KindChannel, uuid.New())

	if err := asWriter(t, store).Put(t.Context(), path, "signing_secret", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(t.Context(), path, "signing_secret")
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Get = %q, want %q", got, "s3cr3t")
	}

	if err := asWriter(t, store).Delete(t.Context(), path); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get(t.Context(), path, "signing_secret"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want secrets.ErrNotFound", err)
	}

	// The read above cannot tell a destroyed secret from a soft-deleted one:
	// KV v2 answers 404 for both. Only asking OpenBao to bring it back can.
	// A DELETE on the data path leaves the credential live behind a 404, and
	// this is the assertion that would catch it.
	admin{t: t, addr: addr, token: root}.do(http.MethodPost,
		"/v1/secret/undelete/"+path, map[string][]int{"versions": {1}})

	if _, err := store.Get(t.Context(), path, "signing_secret"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("undelete brought the secret back: Delete only soft-deleted it")
	}
}

// A path nobody ever wrote, against a real server: 404 with an empty error
// list, which the stub asserts and only this can confirm.
func TestMissingSecretIsNotFoundAgainstRealOpenBao(t *testing.T) {
	t.Parallel()

	store := liveStore(t)
	path := secrets.Path(uuid.New(), secrets.KindChannel, uuid.New())

	if _, err := store.Get(t.Context(), path, "signing_secret"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want secrets.ErrNotFound", err)
	}
}

// A denial and an absence are one status code apart, and only the absence is
// a data problem. Reading an unwritten path on a *granted* mount returns 404,
// so the denial has to come from a mount the policy never mentions —
// cubbyhole does not work here, because a token may always read its own.
func TestDeniedPathIsUnavailableAgainstRealOpenBao(t *testing.T) {
	t.Parallel()

	addr, root := liveBao(t)

	admin{t: t, addr: addr, token: root}.do(http.MethodPost, "/v1/sys/mounts/denied",
		map[string]any{"type": "kv", "options": map[string]string{"version": "2"}})

	roleID, secretID := appRole(t, addr, root, grantSecretMount)
	store := New(addr, roleID, secretID, "denied", liveClient)

	_, err := store.Get(t.Context(), "teams/a/channels/b", "k")
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Errorf("err = %v, want secrets.ErrUnavailable", err)
	}
	if errors.Is(err, secrets.ErrNotFound) {
		t.Error("a denial was reported as a missing secret")
	}
}

// Renewal against a real server. The role allows its secret-id to be used
// exactly once, so a second login is impossible — a second read that
// succeeds can only have renewed. A two-second lease is inside renewSkew
// immediately, so nothing here sleeps.
func TestRenewsAgainstRealOpenBao(t *testing.T) {
	t.Parallel()

	addr, root := liveBao(t)
	roleID, secretID := appRoleWith(t, addr, root, grantSecretMount, map[string]any{
		"token_ttl":          "2s",
		"secret_id_num_uses": 1,
	})
	store := New(addr, roleID, secretID, "secret", liveClient)
	path := secrets.Path(uuid.New(), secrets.KindChannel, uuid.New())

	if err := asWriter(t, store).Put(t.Context(), path, "signing_secret", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(t.Context(), path, "signing_secret")
	if err != nil {
		t.Fatalf("Get on a renewed token: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Get = %q, want %q", got, "s3cr3t")
	}
}
