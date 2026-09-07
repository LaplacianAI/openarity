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
	if !strings.Contains(err.Error(), "write policies") {
		t.Errorf("MintAppRole() = %q, want it to say what the token is missing", err)
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
