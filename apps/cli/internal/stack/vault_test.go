package stack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A Vault that answers the six calls minting needs, and records them.
func stubVault(t *testing.T, quirks map[string]func(http.ResponseWriter)) (*httptest.Server, *[]string) {
	t.Helper()

	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)

		if r.Header.Get("X-Vault-Token") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if quirk := quirks[r.URL.Path]; quirk != nil {
			quirk(w)
			return
		}

		switch r.URL.Path {
		case "/v1/auth/approle/role/" + vaultRole + "/role-id":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"role_id": "the-role-id"}})
		case "/v1/auth/approle/role/" + vaultRole + "/secret-id":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"secret_id": "the-secret-id"}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &seen
}

func TestMintingCreatesTheRoleAndReturnsItsCredentials(t *testing.T) {
	t.Parallel()

	server, seen := stubVault(t, nil)

	role, err := MintAppRole(t.Context(), server.Client(), server.URL, "an-admin-token", "secret")
	if err != nil {
		t.Fatalf("MintAppRole() = %v", err)
	}
	if role.ID != "the-role-id" || role.Secret != "the-secret-id" {
		t.Errorf("MintAppRole() = %+v, want the values the server returned", role)
	}

	for _, want := range []string{
		"POST /v1/sys/mounts/secret",
		"POST /v1/sys/auth/approle",
		"POST /v1/sys/policies/acl/" + vaultRole,
		"POST /v1/auth/approle/role/" + vaultRole,
		"GET /v1/auth/approle/role/" + vaultRole + "/role-id",
		"POST /v1/auth/approle/role/" + vaultRole + "/secret-id",
	} {
		if !contains(*seen, want) {
			t.Errorf("%q was never called; the server saw %v", want, *seen)
		}
	}
}

// An install run twice, or a server whose KV mount an operator enabled years
// ago, must not fail on "path is already in use".
func TestMintingToleratesWhatIsAlreadyThere(t *testing.T) {
	t.Parallel()

	alreadyThere := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":["path is already in use at secret/"]}`))
	}

	server, _ := stubVault(t, map[string]func(http.ResponseWriter){
		"/v1/sys/mounts/secret": alreadyThere,
		"/v1/sys/auth/approle":  alreadyThere,
	})

	if _, err := MintAppRole(t.Context(), server.Client(), server.URL, "an-admin-token", "secret"); err != nil {
		t.Errorf("MintAppRole() = %v, want a second run to succeed", err)
	}
}

// A token that cannot write policies is the most likely mistake, and "403" on
// its own does not say what to do about it.
func TestATokenThatMayNotAdministerSaysSo(t *testing.T) {
	t.Parallel()

	server, _ := stubVault(t, map[string]func(http.ResponseWriter){
		"/v1/sys/policies/acl/" + vaultRole: func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
		},
	})

	_, err := MintAppRole(t.Context(), server.Client(), server.URL, "a-narrow-token", "secret")
	if err == nil {
		t.Fatal("MintAppRole() with a token that may not write policies = nil, want an error")
	}
	// The path alone does not read as a sentence and does not say which token
	// to reach for. "the token may not /v1/sys/mounts/secret" was the first
	// attempt, and it reached somebody that way.
	for _, want := range []string{"write the openarity-brain policy", "root token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("MintAppRole() = %q, want it to mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "/v1/sys/") {
		t.Errorf("MintAppRole() = %q, want the operation in words rather than a path", err)
	}
}

// Each step says which one it was, so a token that can do some of this and not
// the rest points at the thing it cannot do.
func TestEachStepSaysWhichOneItWas(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{
		"/v1/sys/mounts/secret":              "enable the KV v2 mount at secret/",
		"/v1/sys/auth/approle":               "enable the approle auth method",
		"/v1/auth/approle/role/" + vaultRole: "create the " + vaultRole + " role",
	} {
		server, _ := stubVault(t, map[string]func(http.ResponseWriter){
			path: func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
		})

		_, err := MintAppRole(t.Context(), server.Client(), server.URL, "a-narrow-token", "secret")
		if err == nil {
			t.Errorf("MintAppRole() refused at %s = nil, want an error", path)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refused at %s: got %q, want it to say %q", path, err, want)
		}
	}
}

// The admin token is the whole reason this is a choice rather than a default.
// It must not travel any further than the request.
func TestTheAdminTokenIsNeverReturnedInAnError(t *testing.T) {
	t.Parallel()

	const token = "a-very-secret-admin-token"

	server, _ := stubVault(t, map[string]func(http.ResponseWriter){
		"/v1/sys/mounts/secret": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":["something went wrong"]}`))
		},
	})

	_, err := MintAppRole(t.Context(), server.Client(), server.URL, token, "secret")
	if err == nil {
		t.Fatal("MintAppRole() = nil, want an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("MintAppRole() = %q, which carries the admin token", err)
	}
}

func TestMintingNeedsAToken(t *testing.T) {
	t.Parallel()

	_, err := MintAppRole(t.Context(), http.DefaultClient, "http://127.0.0.1:8200", "  ", "secret")
	if err == nil {
		t.Fatal("MintAppRole() with no token = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "administer") {
		t.Errorf("MintAppRole() = %q, want it to say what is needed", err)
	}
}

// The policy is written into somebody else's Vault, so it grants what the
// brain needs and nothing more — no sys, no auth administration, no list.
func TestThePolicyGrantsOnlyWhatTheBrainNeeds(t *testing.T) {
	t.Parallel()

	written := renderPolicy("kv")

	for _, want := range []string{
		`path "kv/data/teams/+/channels/+"`,
		`path "kv/metadata/teams/+/channels/+"`,
		`path "kv/data/teams/+/attachments"`,
		`path "kv/metadata/teams/+/attachments"`,
		`path "auth/token/renew-self"`,
	} {
		if !strings.Contains(written, want) {
			t.Errorf("the policy is missing %s", want)
		}
	}

	for _, never := range []string{`"list"`, `path "sys/`, `path "auth/approle`, `"sudo"`, `"root"`} {
		if strings.Contains(written, never) {
			t.Errorf("the policy grants %s, which the brain never needs", never)
		}
	}
}

// The mount is a choice, so a policy that hardcoded secret/ would grant
// nothing on a server that mounted its KV elsewhere.
func TestThePolicyFollowsTheMount(t *testing.T) {
	t.Parallel()

	if strings.Contains(renderPolicy("openarity"), "secret/data") {
		t.Error("the policy names secret/ regardless of the mount that was chosen")
	}
	if !strings.Contains(renderPolicy("openarity"), `path "openarity/data/teams/+/channels/+"`) {
		t.Error("the policy does not follow the mount")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Asked before anything is downloaded. A wrong address is the most likely
// mistake and the cheapest to catch: the default is 8200 and a compose file
// commonly publishes the same server on another port, so the store is running
// and nothing is at the address anyway.
func TestReachableAcceptsAnythingThatAnswers(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusOK,                 // unsealed and serving
		http.StatusTooManyRequests,    // standby
		http.StatusServiceUnavailable, // sealed
		http.StatusNotImplemented,     // uninitialised
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		// Sealed, standby and uninitialised are the operator's business, not
		// setup's. All that is being asked here is whether anything is there.
		if err := Reachable(t.Context(), server.Client(), server.URL); err != nil {
			t.Errorf("Reachable() against a server answering %d = %v", status, err)
		}
		server.Close()
	}
}

func TestReachableSaysWhatToCheckWhenNothingAnswers(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.URL
	server.Close() // nothing is listening now

	err := Reachable(t.Context(), http.DefaultClient, addr)
	if err == nil {
		t.Fatal("Reachable() against nothing = nil, want an error")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("Reachable() = %q, want it to name the address", err)
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("Reachable() = %q, want it to suggest what is usually wrong", err)
	}
}

// It asks the one endpoint that needs no credential, so it can be asked before
// there is one.
func TestReachableNeedsNoToken(t *testing.T) {
	t.Parallel()

	var path, token string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.Path, r.Header.Get("X-Vault-Token")
	}))
	t.Cleanup(server.Close)

	if err := Reachable(t.Context(), server.Client(), server.URL); err != nil {
		t.Fatalf("Reachable() = %v", err)
	}
	if path != "/v1/sys/health" {
		t.Errorf("Reachable() asked %s, want the unauthenticated health endpoint", path)
	}
	if token != "" {
		t.Errorf("Reachable() sent a token, which it cannot have before one exists")
	}
}
