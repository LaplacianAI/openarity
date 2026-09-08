package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Minting the AppRole the brain authenticates with, so a person choosing an
// external secret store does not have to run `make bao-approle` and paste two
// values back.
//
// It costs an admin token, which is a strictly more dangerous credential than
// the two it replaces — it can do anything to that server. So it is used for
// the six calls below and never stored: only the AppRole reaches the
// credentials file, and the token exists for as long as setup runs.
//
// This also writes into somebody else's infrastructure: it enables an auth
// method and creates a policy and a role. That is why it is an option a person
// chooses rather than something setup does when it can.

// AppRole is what the brain logs in with, and all that is kept.
type AppRole struct {
	ID     string
	Secret string
}

// The role and policy are named for what uses them, so an operator reading
// their own Vault later can tell what put them there.
const vaultRole = "openarity-brain"

// The capabilities the brain needs and nothing more. The reasoning for each is
// in deployment/openbao/policy-brain.hcl, which is the canonical copy; this is
// the same set with the mount templated, because the mount is a choice here
// and fixed there.
//
// Kept deliberately short of the original's commentary: a policy written into
// somebody's Vault is read in their UI, and an essay there is noise.
const vaultPolicy = `# Written by the Openarity installer. The capabilities the brain needs.
path "%[1]s/data/teams/+/channels/+" {
  capabilities = ["read", "create", "update"]
}
path "%[1]s/metadata/teams/+/channels/+" {
  capabilities = ["delete"]
}
path "%[1]s/data/teams/+/attachments" {
  capabilities = ["read", "create", "update"]
}
path "%[1]s/metadata/teams/+/attachments" {
  capabilities = ["delete"]
}
path "auth/token/renew-self" {
  capabilities = ["update"]
}
`

type vault struct {
	client *http.Client
	addr   string
	token  string
}

// MintAppRole creates the mount, the auth method, the policy and the role, and
// returns the credentials the brain will use.
//
// Every step tolerates already existing, because an install run twice against
// one server must not fail on the second — and because an operator may well
// have enabled the KV mount years ago.
func MintAppRole(ctx context.Context, client *http.Client, addr, token, mount string) (AppRole, error) {
	if strings.TrimSpace(token) == "" {
		// Both ways out, because either is reasonable: somebody whose ops team
		// handed them an AppRole and no admin token is not stuck, and somebody
		// who owns the server does not have to go and mint one by hand.
		return AppRole{}, fmt.Errorf(
			"stack: no AppRole and no admin token — set %s and %s, or give a token that may administer %s and one will be created",
			"OPENARITY_SECRETS_APPROLE_ID", "OPENARITY_SECRETS_APPROLE_SECRET", addr)
	}
	if mount == "" {
		mount = "secret"
	}

	// Trimmed because it is pasted. A token carrying a trailing newline is
	// rejected as if it were somebody else's, and the answer says nothing
	// about whitespace.
	v := &vault{client: client, addr: strings.TrimRight(addr, "/"), token: strings.TrimSpace(token)}

	// A token the server has never seen and one that is merely too narrow are
	// both 403 "permission denied" — measured, not assumed. Asking what this
	// token is separates them, so a typo stops being reported as "go and find
	// a root token" to somebody who already has one.
	if err := v.identify(ctx); err != nil {
		return AppRole{}, err
	}

	// KV v2, because the brain writes with check-and-set, which v1 does not
	// have.
	if err := v.put(ctx, "/v1/sys/mounts/"+mount, map[string]any{
		"type": "kv", "options": map[string]string{"version": "2"},
	}, "enable the KV v2 mount at "+mount+"/"); err != nil {
		return AppRole{}, err
	}

	if err := v.put(ctx, "/v1/sys/auth/approle", map[string]any{"type": "approle"},
		"enable the approle auth method"); err != nil {
		return AppRole{}, err
	}

	if err := v.put(ctx, "/v1/sys/policies/acl/"+vaultRole, map[string]any{
		"policy": renderPolicy(mount),
	}, "write the "+vaultRole+" policy"); err != nil {
		return AppRole{}, err
	}

	if err := v.put(ctx, "/v1/auth/approle/role/"+vaultRole, map[string]any{
		"token_policies": vaultRole,
		"token_ttl":      "1h",
		"token_max_ttl":  "4h",
	}, "create the "+vaultRole+" role"); err != nil {
		return AppRole{}, err
	}

	id, err := v.field(ctx, http.MethodGet, "/v1/auth/approle/role/"+vaultRole+"/role-id", nil, "role_id")
	if err != nil {
		return AppRole{}, err
	}
	secret, err := v.field(ctx, http.MethodPost, "/v1/auth/approle/role/"+vaultRole+"/secret-id", map[string]any{}, "secret_id")
	if err != nil {
		return AppRole{}, err
	}
	return AppRole{ID: id, Secret: secret}, nil
}

// identify asks the server what the token it was given is, which costs one
// round trip and is the only way to tell "I have never seen this token" from
// "this token may not do that". Both are 403 here.
func (v *vault) identify(ctx context.Context) error {
	res, err := v.do(ctx, http.MethodGet, "/v1/auth/token/lookup-self", nil)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 300 {
		return nil
	}

	said, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode == http.StatusForbidden {
		return fmt.Errorf(
			"stack: %s does not recognise that token — check it was copied whole and has not expired (a wrong token and no token get the same answer)",
			v.addr)
	}
	return v.refuse("look up the token it was given", res.StatusCode, said)
}

// put writes, and treats "it is already there" as success.
func (v *vault) put(ctx context.Context, path string, body map[string]any, what string) error {
	res, err := v.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 300 {
		return nil
	}

	said, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))

	// Enabling a mount or an auth method that exists is a 400 naming the path.
	// An install run twice, or a server an operator set up years ago, must not
	// fail here.
	if res.StatusCode == http.StatusBadRequest && strings.Contains(string(said), "already in use") {
		return nil
	}
	return v.refuse(what, res.StatusCode, said)
}

func (v *vault) field(ctx context.Context, method, path string, body map[string]any, name string) (string, error) {
	res, err := v.do(ctx, method, path, body)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()

	said, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		return "", v.refuse("read "+name+" from "+path, res.StatusCode, said)
	}

	var answer struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(said, &answer); err != nil {
		return "", fmt.Errorf("stack: %s answered something that is not JSON: %w", path, err)
	}

	value, _ := answer.Data[name].(string)
	if value == "" {
		return "", fmt.Errorf("stack: %s returned no %s", path, name)
	}
	return value, nil
}

func (v *vault) do(ctx context.Context, method, path string, body map[string]any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, v.addr+path, payload)
	if err != nil {
		return nil, fmt.Errorf("stack: %s is not an address: %w", v.addr, err)
	}
	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")

	res, err := v.client.Do(req)
	if err != nil {
		return nil, unreachable(v.addr, err)
	}
	return res, nil
}

// Reachable answers whether there is a secret store at that address, without
// needing a credential: sys/health is unauthenticated.
//
// Asked before anything is downloaded, because the alternative is finding out
// four minutes later. An address that is merely wrong — the default 8200 when
// the server publishes 28200, which is what a compose file does — is the most
// likely mistake and the cheapest to catch.
func Reachable(ctx context.Context, client *http.Client, addr string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(addr, "/")+"/v1/sys/health", nil)
	if err != nil {
		return fmt.Errorf("stack: %s is not an address: %w", addr, err)
	}

	res, err := client.Do(req)
	if err != nil {
		return unreachable(addr, err)
	}
	defer func() { _ = res.Body.Close() }()

	// Any answer at all proves something is there. Sealed, standby and
	// uninitialised all have their own status here and are the operator's
	// business, not setup's.
	return nil
}

// unreachable says what a connection error means, rather than passing on Go's
// rendering of it. "Post "http://…/v1/sys/mounts/secret": dial tcp: connect:
// connection refused" is accurate and tells a person nothing about what to do.
func unreachable(addr string, err error) error {
	return fmt.Errorf(
		"stack: nothing answered at %s — check the address and that the server is running (a compose file often publishes it on a different port): %w",
		addr, err)
}

// refuse says what was refused, in words, without repeating the token back.
//
// The path alone does not read as a sentence — "the token may not
// /v1/sys/mounts/secret" was the first attempt — and does not tell somebody
// which token to reach for instead.
func (v *vault) refuse(what string, status int, said []byte) error {
	if status == http.StatusForbidden {
		return fmt.Errorf(
			"stack: that token may not %s — minting needs one that can enable mounts and auth methods and write policies, which usually means a root token",
			what)
	}
	return fmt.Errorf("stack: could not %s: %s answered %d: %s",
		what, v.addr, status, strings.TrimSpace(string(said)))
}

// renderPolicy is the policy with the mount filled in.
func renderPolicy(mount string) string {
	return fmt.Sprintf(vaultPolicy, mount)
}
