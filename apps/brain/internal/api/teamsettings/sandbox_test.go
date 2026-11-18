package teamsettings

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// withSandbox is a whole save whose sandbox is sb: JSON, or "" to leave it out.
func withSandbox(sb string) string {
	s := `{"model":null,"search":{"provider":"none"},`
	if sb != "" {
		s += `"sandbox":` + sb + `,`
	}
	return s + `"learning":false,"compaction_messages":100}`
}

// storedSandbox puts a saved sandbox in the fake.
func (e *env) storedSandbox(provider string, secretID *uuid.UUID, url *string) {
	e.stored("none", nil, nil)
	e.store.row.SandboxProvider, e.store.row.SandboxSecretID, e.store.row.SandboxUrl = provider, secretID, url
}

func ptr(s string) *string { return &s }

func TestATeamThatNeverSavedHasNoSandbox(t *testing.T) {
	got := decode(t, newEnv().get(t))
	if got.Sandbox.Provider != "none" || got.Sandbox.KeySet || got.Sandbox.URL != nil {
		t.Errorf("sandbox = %+v, want none", got.Sandbox)
	}
}

// The key goes to a path of its own under the sandbox kind, named by the id
// the row keeps, and never comes back out.
func TestASandboxKeyGoesToItsOwnPath(t *testing.T) {
	e := newEnv()
	rec := e.save(t, withSandbox(`{"provider":"daytona","url":"https://daytona.example","api_key":"dtn-secret"}`))
	got := decode(t, rec)

	saved := e.store.upserts[0]
	if saved.SandboxSecretID == nil || len(e.secrets.puts) != 1 {
		t.Fatalf("secret id %v, puts %+v; want one key written and named", saved.SandboxSecretID, e.secrets.puts)
	}
	p := e.secrets.puts[0]
	if want := secrets.Path(e.team, secrets.KindSandbox, *saved.SandboxSecretID); p.path != want || p.key != KeyAPI || p.value != "dtn-secret" {
		t.Errorf("put %+v, want %s %s = the key", p, want, KeyAPI)
	}
	if got.Sandbox.Provider != "daytona" || !got.Sandbox.KeySet || *got.Sandbox.URL != "https://daytona.example" {
		t.Errorf("sandbox = %+v", got.Sandbox)
	}
	if strings.Contains(rec.Body.String(), "dtn-secret") {
		t.Errorf("the response carries the key: %s", rec.Body)
	}
}

// A client that predates sandboxes sends none, and the team's stays.
func TestASaveWithoutASandboxKeepsTheStoredOne(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedSandbox("e2b", &id, nil)

	got := decode(t, e.save(t, withSandbox("")))

	if p := e.store.upserts[0]; p.SandboxProvider != "e2b" || p.SandboxSecretID == nil || *p.SandboxSecretID != id {
		t.Errorf("saved %q, %v; want e2b and the stored key kept", p.SandboxProvider, p.SandboxSecretID)
	}
	if len(e.secrets.puts) != 0 || got.Sandbox.Provider != "e2b" {
		t.Errorf("puts %+v, sandbox %+v", e.secrets.puts, got.Sandbox)
	}
}

func TestASandboxKeyIsKeptOnlyForTheSameProviderAndURL(t *testing.T) {
	id := uuid.New()
	for name, tc := range map[string]struct {
		sandbox string
		keeps   bool
	}{
		"the same provider and url": {`{"provider":"sandboxd","url":"https://sbx.example"}`, true},
		"another url":               {`{"provider":"sandboxd","url":"https://other.example"}`, false},
		"another provider":          {`{"provider":"daytona","url":"https://sbx.example"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.storedSandbox("sandboxd", &id, ptr("https://sbx.example"))
			rec := e.save(t, withSandbox(tc.sandbox))
			switch {
			case tc.keeps && rec.Code != http.StatusOK:
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			case tc.keeps && (e.store.upserts[0].SandboxSecretID == nil || *e.store.upserts[0].SandboxSecretID != id):
				t.Errorf("the stored key was not kept")
			case !tc.keeps && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sandbox.api_key is required")):
				t.Errorf("status %d %q; a key saved for one place must not reach another", rec.Code, rec.Body)
			case !tc.keeps && len(e.store.upserts) != 0:
				t.Error("a refused save was written")
			}
		})
	}
}

func TestTurningTheSandboxOffDropsItsKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedSandbox("e2b", &id, nil)

	got := decode(t, e.save(t, withSandbox(`{"provider":"none"}`)))
	if p := e.store.upserts[0]; p.SandboxSecretID != nil || p.SandboxUrl != nil {
		t.Errorf("saved %+v, want nothing but none", p)
	}
	if got.Sandbox.KeySet {
		t.Error("key_set with no sandbox")
	}
}

// A refused sandbox is refused before anything is written.
func TestABadSandboxIsRefused(t *testing.T) {
	for name, tc := range map[string]struct{ sandbox, says string }{
		"an unknown provider":    {`{"provider":"modal","api_key":"k"}`, "sandbox.provider must be"},
		"e2b with a url":         {`{"provider":"e2b","url":"https://e2b.example","api_key":"k"}`, "e2b is hosted only"},
		"sandboxd without a url": {`{"provider":"sandboxd","api_key":"k"}`, "required for sandboxd"},
		"a provider with no key": {`{"provider":"e2b"}`, "sandbox.api_key is required"},
		"an empty key":           {`{"provider":"e2b","api_key":""}`, "must not be empty"},
		"none with a key":        {`{"provider":"none","api_key":"k"}`, "none takes none"},
		"a url with credentials": {`{"provider":"daytona","url":"https://u:p@d.example","api_key":"k"}`, "must not carry credentials"},
		"a private address":      {`{"provider":"sandboxd","url":"http://10.0.0.5:8090","api_key":"k"}`, "private"},
		// What a room starts from is the brain's to choose, but for E2B's template.
		"an image":                {`{"provider":"e2b","image":"attacker/miner:1","api_key":"k"}`, "invalid request body"},
		"a template for daytona":  {`{"provider":"daytona","template":"openarity-sandbox","api_key":"k"}`, "sandbox.template is for e2b"},
		"a template for none":     {`{"provider":"none","template":"openarity-sandbox"}`, "none takes none"},
		"an empty template":       {`{"provider":"e2b","template":"","api_key":"k"}`, "E2B template name"},
		"a template with a space": {`{"provider":"e2b","template":"my template","api_key":"k"}`, "E2B template name"},
		"a template with a tag":   {`{"provider":"e2b","template":"openarity-sandbox:latest","api_key":"k"}`, "E2B template name"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			rec := e.save(t, withSandbox(tc.sandbox))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("status %d %q, want 400 saying %q", rec.Code, rec.Body, tc.says)
			}
			if len(e.secrets.puts) != 0 || len(e.store.upserts) != 0 {
				t.Error("a refused sandbox wrote something")
			}
		})
	}
}

// A save that fails after both keys were written leaves neither unnamed.
func TestAFailedSaveDestroysBothKeysItWrote(t *testing.T) {
	e := newEnv()
	e.store.upsertErr = errAny

	rec := e.save(t, `{"model":null,"search":{"provider":"brave","api_key":"b"},`+
		`"sandbox":{"provider":"e2b","api_key":"s"},"learning":false,"compaction_messages":100}`)

	if rec.Code != http.StatusInternalServerError || len(e.secrets.puts) != 2 || len(e.store.recorded) != 2 {
		t.Fatalf("status %d, puts %+v, recorded %+v; want 500 and both keys recorded", rec.Code, e.secrets.puts, e.store.recorded)
	}
	for i, r := range e.store.recorded {
		if r.Path != e.secrets.puts[i].path {
			t.Errorf("recorded %s, want %s", r.Path, e.secrets.puts[i].path)
		}
	}
}

// The search key written first is not left behind when the sandbox key cannot be.
func TestASandboxKeyTheStoreRefusesForgetsTheSearchKey(t *testing.T) {
	e := newEnv()
	e.secrets.putErrAfter = 1

	rec := e.save(t, `{"model":null,"search":{"provider":"brave","api_key":"b"},`+
		`"sandbox":{"provider":"e2b","api_key":"s"},"learning":false,"compaction_messages":100}`)

	if rec.Code != http.StatusServiceUnavailable || len(e.store.upserts) != 0 {
		t.Fatalf("status %d, upserts %d; want 503 and nothing saved", rec.Code, len(e.store.upserts))
	}
	if len(e.store.recorded) != 1 || e.store.recorded[0].Path != e.secrets.puts[0].path {
		t.Errorf("recorded %+v, want the search key just written", e.store.recorded)
	}
}

// An E2B team may name its template, its own or another team's public one;
// left out, none is stored and the brain's default is used.
func TestAnE2BTemplateIsKeptAsGiven(t *testing.T) {
	for _, tmpl := range []string{"openarity-sandbox", "acme/openarity-sandbox"} {
		e := newEnv()
		got := decode(t, e.save(t, withSandbox(`{"provider":"e2b","template":"`+tmpl+`","api_key":"k"}`)))
		if p := e.store.upserts[0].SandboxTemplate; p == nil || *p != tmpl {
			t.Errorf("stored template %v, want %s", p, tmpl)
		}
		if got.Sandbox.Template == nil || *got.Sandbox.Template != tmpl {
			t.Errorf("shown template %v, want %s", got.Sandbox.Template, tmpl)
		}
	}

	e := newEnv()
	e.save(t, withSandbox(`{"provider":"e2b","api_key":"k"}`))
	if p := e.store.upserts[0].SandboxTemplate; p != nil {
		t.Errorf("stored template %q, want none", *p)
	}
}
