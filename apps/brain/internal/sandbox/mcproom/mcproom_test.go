package mcproom

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type recordingKeeper struct {
	owner keeper.Owner
	team  uuid.UUID
	want  keeper.Want
	ep    sandbox.Endpoint
	err   error
}

func (r *recordingKeeper) Ensure(_ context.Context, o keeper.Owner, team uuid.UUID, w keeper.Want) (sandbox.Endpoint, error) {
	r.owner, r.team, r.want = o, team, w
	return r.ep, r.err
}

func server(command ...string) db.McpServer {
	return db.McpServer{ID: uuid.New(), TeamID: uuid.New(), Name: "x", Command: command, Env: []byte(`{}`)}
}

// The shim is what makes a command a URL: the room runs it with the server's
// command after the two dashes, on the port the brain dials.
func TestTheRoomRunsTheShimAroundTheCommand(t *testing.T) {
	t.Parallel()

	w, err := Want(server("uvx", "mail-mcp", "--read-only"))
	if err != nil {
		t.Fatal(err)
	}

	wantArgv := []string{"sandbox", "shim", "--listen", ":8080", "--", "uvx", "mail-mcp", "--read-only"}
	if !reflect.DeepEqual(w.Argv, wantArgv) {
		t.Errorf("argv = %v, want %v", w.Argv, wantArgv)
	}
	if w.Port != sandbox.ShimPort || w.Path != "/mcp" || w.HealthPath != "/healthz" || w.TokenEnv != "SHIM_TOKEN" {
		t.Errorf("want = %+v", w)
	}
	if w.Image != "" {
		t.Errorf("image = %q, want the brain's own choice", w.Image)
	}
}

// npx is told not to ask, check, audit or announce, because nobody is there
// to answer; uvx wants none of it.
func TestNpxIsToldNotToAskAnything(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"npx", "/usr/local/bin/npx"} {
		w, err := Want(server(command, "-y", "pkg"))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"npm_config_update_notifier": "false",
			"npm_config_fund":            "false",
			"npm_config_audit":           "false",
			"npm_config_yes":             "true",
		}
		if !reflect.DeepEqual(w.Env, want) {
			t.Errorf("%s: env = %v, want %v", command, w.Env, want)
		}
	}

	w, err := Want(server("uvx", "pkg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Env) != 0 {
		t.Errorf("uvx: env = %v, want none", w.Env)
	}
}

// The server's settings are its environment and its egress is its allow
// list; the npm settings are set after, so a server cannot unset them.
func TestTheServersSettingsAndEgressAreTheRoomsOwn(t *testing.T) {
	t.Parallel()

	s := server("npx", "pkg")
	s.Vars = []byte(`{"IMAP_HOST":"imap.example","npm_config_yes":"false"}`)
	s.Egress = []byte(`[{"host":"imap.example","port":993}]`)

	w, err := Want(s)
	if err != nil {
		t.Fatal(err)
	}

	if w.Env["IMAP_HOST"] != "imap.example" {
		t.Errorf("env = %v, want the server's setting", w.Env)
	}
	if w.Env["npm_config_yes"] != "true" {
		t.Errorf("npm_config_yes = %q, want the brain's", w.Env["npm_config_yes"])
	}
	if want := []sandbox.HostPort{{Host: "imap.example", Port: 993}}; !reflect.DeepEqual(w.Egress, want) {
		t.Errorf("egress = %v, want %v", w.Egress, want)
	}
}

// Want is pure: a server's env references are not settings, so they are
// not in its Env; Ensure resolves them into Secrets.
func TestSecretReferencesAreNotSettings(t *testing.T) {
	t.Parallel()

	s := server("uvx", "pkg")
	s.Env = []byte(`{"PASSWORD":"teams/t/mcp/x#password"}`)

	w, err := Want(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Env["PASSWORD"]; ok {
		t.Errorf("env = %v, holds a reference's variable", w.Env)
	}
}

// fakeSecrets answers path#key from a map, and counts what it was asked.
type fakeSecrets map[string]string

func (f fakeSecrets) Get(_ context.Context, path, key string) (string, error) {
	v, ok := f[path+"#"+key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

// The room is given what the server's references name, as secrets, so the
// command can read its password and the keeper never stores it as a setting.
func TestEnsureGivesTheRoomTheServersSecrets(t *testing.T) {
	t.Parallel()

	k := &recordingKeeper{}
	s := server("npx", "mail-mcp")
	s.Env = []byte(`{"EMAIL_PASS":"teams/t/mcp/gmail#app_password"}`)

	if _, err := New(k, fakeSecrets{"teams/t/mcp/gmail#app_password": "hunter2"}).Ensure(t.Context(), s.TeamID, s); err != nil {
		t.Fatal(err)
	}
	if k.want.Secrets["EMAIL_PASS"] != "hunter2" {
		t.Errorf("secrets = %v, want EMAIL_PASS resolved", k.want.Secrets)
	}
	if _, ok := k.want.Env["EMAIL_PASS"]; ok {
		t.Error("the secret is in Env, which the spec hash holds in the clear")
	}
}

// A reference that names nothing, or names an empty value, stops the start
// before the keeper is asked, and the error names the variable, not a value.
func TestEnsureRefusesASecretItCannotRead(t *testing.T) {
	t.Parallel()

	for name, env := range map[string]string{
		"missing": `{"EMAIL_PASS":"teams/t/mcp/gmail#app_password"}`,
		"empty":   `{"EMAIL_PASS":"teams/t/mcp/gmail#empty"}`,
		"not ref": `{"EMAIL_PASS":"hunter2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			k := &recordingKeeper{}
			s := server("npx", "mail-mcp")
			s.Env = []byte(env)

			_, err := New(k, fakeSecrets{"teams/t/mcp/gmail#empty": ""}).Ensure(t.Context(), s.TeamID, s)
			if err == nil || !strings.Contains(err.Error(), "EMAIL_PASS") || strings.Contains(err.Error(), "hunter2") {
				t.Errorf("err = %v, want one naming EMAIL_PASS and no value", err)
			}
			if k.owner != (keeper.Owner{}) {
				t.Error("the keeper was asked")
			}
		})
	}
}

func TestAServerWithNoCommandHasNoRoom(t *testing.T) {
	t.Parallel()

	if _, err := Want(server()); !errors.Is(err, ErrNotACommand) {
		t.Errorf("err = %v, want ErrNotACommand", err)
	}
}

func TestASettingThatIsNotJSONIsRefused(t *testing.T) {
	t.Parallel()

	s := server("uvx", "pkg")
	s.Vars = []byte(`[1]`)
	if _, err := Want(s); err == nil {
		t.Error("vars that are not an object were accepted")
	}
	s = server("uvx", "pkg")
	s.Egress = []byte(`{}`)
	if _, err := Want(s); err == nil {
		t.Error("egress that is not a list was accepted")
	}
}

// Ensure owns the room by the server, for its team, and passes the keeper's
// endpoint and errors on unchanged.
func TestEnsureAsksTheKeeperForTheServersRoom(t *testing.T) {
	t.Parallel()

	k := &recordingKeeper{ep: sandbox.Endpoint{URL: "https://room.example/mcp"}}
	s := server("uvx", "pkg")

	ep, err := New(k, fakeSecrets{}).Ensure(t.Context(), s.TeamID, s)
	if err != nil {
		t.Fatal(err)
	}

	if ep.URL != "https://room.example/mcp" {
		t.Errorf("endpoint = %+v", ep)
	}
	if k.owner != (keeper.Owner{Kind: OwnerKind, ID: s.ID}) || k.team != s.TeamID {
		t.Errorf("owner = %+v team = %s, want the server's", k.owner, k.team)
	}

	k.err = sandbox.ErrNoProvider
	if _, err := New(k, fakeSecrets{}).Ensure(t.Context(), s.TeamID, s); !errors.Is(err, sandbox.ErrNoProvider) {
		t.Errorf("err = %v, want the keeper's", err)
	}
}

func TestEnsureRefusesAServerWithNoCommandBeforeAskingTheKeeper(t *testing.T) {
	t.Parallel()

	k := &recordingKeeper{}
	s := server()
	if _, err := New(k, fakeSecrets{}).Ensure(t.Context(), s.TeamID, s); !errors.Is(err, ErrNotACommand) {
		t.Errorf("err = %v, want ErrNotACommand", err)
	}
	if k.owner != (keeper.Owner{}) {
		t.Error("the keeper was asked")
	}
}
