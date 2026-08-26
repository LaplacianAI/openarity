package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The policy that ships, tested as it ships.
//
// Every other test in this package builds its AppRole from grantSecretMount,
// which opens the whole KV mount. That is the right fixture for testing the
// client — a login, a round trip, a renewal — and the wrong one for answering
// "may the brain do this in production", because it answers yes to everything.
//
// The two drifted apart and nobody noticed: registering a channel writes its
// signing secret, the mount-wide fixture allowed the write, and the deployed
// policy was read-only. It surfaced as a 403 in staging. These tests read the
// real file so that the next time the two disagree, the disagreement is a
// failing test rather than a deployment.

// shippedPolicy is deployment/openbao/policy-brain.hcl, read from disk rather
// than copied here. A copy would be one more thing to keep in step, which is
// the failure being fixed.
func shippedPolicy(t *testing.T) string {
	t.Helper()

	// From apps/brain/internal/secrets to the repository root.
	path := filepath.Join("..", "..", "..", "..", "deployment", "openbao", "policy-brain.hcl")

	hcl, err := os.ReadFile(path) //nolint:gosec // a fixed path inside the repository
	if err != nil {
		t.Fatalf("reading the policy the brain deploys with: %v", err)
	}
	return string(hcl)
}

// brainStore hands back a store carrying exactly the privileges production
// grants it, and the AppRole token behind it. Both halves are needed: the
// interfaces cannot express a list or a soft delete, and those are two of the
// things the policy must refuse.
//
// Reading and writing are separate interfaces here — Store is read-only and
// Writer adds Put and Delete — so this asserts its way to both rather than
// returning one and having each caller repeat it.
func brainStore(t *testing.T) (reader Store, writer Writer, token, addr string) {
	t.Helper()

	addr, root := liveBao(t)
	roleID, secretID := appRole(t, addr, root, shippedPolicy(t))

	reader = NewOpenBao(addr, roleID, secretID, "secret", liveClient)
	writer, ok := reader.(Writer)
	if !ok {
		t.Fatalf("the OpenBao store no longer implements Writer, so registering "+
			"a channel cannot be tested through it: %T", reader)
	}
	return reader, writer, login(t, addr, roleID, secretID), addr
}

// login performs the same AppRole login the store performs, so the token
// carries the brain's identity and not a convenient approximation of it. The
// store keeps its own token private, which is correct, and leaves this as the
// way to ask what that identity may reach.
func login(t *testing.T, addr, roleID, secretID string) string {
	t.Helper()

	body, err := json.Marshal(map[string]string{"role_id": roleID, "secret_id": secretID})
	if err != nil {
		t.Fatalf("marshal login: %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		addr+"/v1/auth/approle/login", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build login: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := liveClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode login (%d): %s", resp.StatusCode, raw)
	}
	if decoded.Auth.ClientToken == "" {
		t.Fatalf("login returned %d with no token: %s", resp.StatusCode, raw)
	}
	return decoded.Auth.ClientToken
}

// statusAs sends one request as the given token and reports the status.
// Deliberately not admin.do, which fails the test on an error status and
// decodes the body away — here a refusal is the expected answer.
func statusAs(t *testing.T, addr, token, method, path string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, addr+path, http.NoBody)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("X-Vault-Token", token)

	resp, err := liveClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

// Registering a channel, reading its secret back and disconnecting it are the
// three things the brain does with this store, and the deployed policy has to
// allow all three. This is the test that would have caught the read-only
// policy: the write is the first thing it does.
func TestTheShippedPolicyServesTheStore(t *testing.T) {
	t.Parallel()

	reader, writer, _, _ := brainStore(t)

	// Path rather than a hand-built string, so that moving where channel
	// secrets live moves this test with it instead of leaving it green
	// against a path nothing uses.
	path := Path(uuid.New(), KindChannel, uuid.New())

	if err := writer.Put(t.Context(), path, "signing_secret", "s3cret"); err != nil {
		t.Fatalf("registering a channel: %v", err)
	}

	got, err := reader.Get(t.Context(), path, "signing_secret")
	if err != nil {
		t.Fatalf("reading the secret back: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("secret = %q, want %q", got, "s3cret")
	}

	if err := writer.Delete(t.Context(), path); err != nil {
		t.Fatalf("disconnecting a channel: %v", err)
	}

	// Deletion is on the metadata path precisely so that no version survives.
	// A soft delete would leave this readable at ?version=1.
	if _, err := reader.Get(t.Context(), path, "signing_secret"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, err = %v, want ErrNotFound", err)
	}
}

// The grants are only half the policy. Each of these is something the comments
// in policy-brain.hcl claim is refused, and a claim nothing checks is a claim
// that decays — `+` quietly becoming `*`, or `list` being added to make a
// debugging session easier, would both pass every other test in this package.
func TestTheShippedPolicyRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	_, _, token, addr := brainStore(t)
	team, channel := uuid.NewString(), uuid.NewString()

	const secret = "/v1/secret"
	for name, tc := range map[string]struct{ method, path string }{
		"a sibling kind of secret": {
			http.MethodGet, secret + "/data/teams/" + team + "/tokens/" + channel,
		},
		"below a channel, because + is not greedy": {
			http.MethodGet, secret + "/data/teams/" + team + "/channels/" + channel + "/extra",
		},
		"the team itself": {
			http.MethodGet, secret + "/data/teams/" + team,
		},
		"listing the channels of a team": {
			http.MethodGet, secret + "/metadata/teams/" + team + "/channels?list=true",
		},
		"listing the teams": {
			http.MethodGet, secret + "/metadata/teams?list=true",
		},
		"a soft delete, which leaves the version recoverable": {
			http.MethodDelete, secret + "/data/teams/" + team + "/channels/" + channel,
		},
		"the version history of a channel": {
			http.MethodGet, secret + "/metadata/teams/" + team + "/channels/" + channel,
		},
		"administering OpenBao": {
			http.MethodGet, "/v1/sys/policies/acl",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := statusAs(t, addr, token, tc.method, tc.path); got != http.StatusForbidden {
				t.Errorf("%s %s returned %d, want %d — the policy permits %s",
					tc.method, tc.path, got, http.StatusForbidden, name)
			}
		})
	}
}

// The reason the file is read rather than copied. If it moves, this says so
// instead of the tests above quietly falling back to something permissive.
func TestTheShippedPolicyIsWhereTheDeploymentExpectsIt(t *testing.T) {
	t.Parallel()

	hcl := shippedPolicy(t)
	for _, want := range []string{
		`path "secret/data/teams/+/channels/+"`,
		`path "secret/metadata/teams/+/channels/+"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("policy-brain.hcl no longer contains %s — the tests above are\n"+
				"checking a policy with a different shape than they were written for", want)
		}
	}
}

// A guard against the guard: brainStore must be building its AppRole from the
// deployed file, not from grantSecretMount. Swapping one for the other is a
// one-word edit that would turn both tests above into tests of nothing, and
// the denial test would fail loudly — but only if the mount-wide fixture is
// genuinely more permissive than the shipped policy. This pins that.
func TestTheShippedPolicyIsNarrowerThanTheTestFixture(t *testing.T) {
	t.Parallel()

	if shippedPolicy(t) == grantSecretMount {
		t.Fatal("the deployed policy and the test fixture are the same text")
	}

	addr, root := liveBao(t)
	roleID, secretID := appRole(t, addr, root, grantSecretMount)
	token := login(t, addr, roleID, secretID)

	// The fixture allows what the shipped policy refuses. If this ever fails,
	// grantSecretMount has been narrowed and the denial test above may be
	// passing for the wrong reason.
	path := "/v1/secret/metadata/teams/" + uuid.NewString() + "/channels?list=true"
	if got := statusAs(t, addr, token, http.MethodGet, path); got == http.StatusForbidden {
		t.Errorf("the mount-wide fixture refused %s, so it no longer differs\n"+
			"from the shipped policy in the way the denial test relies on", path)
	}
}
