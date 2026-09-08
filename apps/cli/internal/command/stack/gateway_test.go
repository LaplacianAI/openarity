package stack

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// The whole promise of a personal install: nothing but this machine can reach
// it. OmniRoute binds every interface by default and says so in its own log —
// "reachable by ANY device that can route to this host, and requests are
// billed to your configured providers" — and the variable that stops it is
// OMNIROUTE_SERVER_HOST. HOSTNAME, which Next.js reads, was ignored.
func TestTheGatewayIsHeldToLoopback(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ backend, want string }{
		{"omniroute", "OMNIROUTE_SERVER_HOST=127.0.0.1"},
		{"litellm", "--host"},
	} {
		child := gatewayChild(engine.Settings{ModelBackend: tc.backend, ModelPath: "/somewhere"}, nil, 20128, "/dev/null")
		if child == nil {
			t.Fatalf("gatewayChild(%s) = nil", tc.backend)
		}

		found := false
		for _, s := range append(append([]string{}, child.Env...), child.Args...) {
			if strings.Contains(s, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is started without %q, so it binds every interface", tc.backend, tc.want)
		}
		if tc.backend == "litellm" {
			if !strings.Contains(strings.Join(child.Args, " "), "--host 127.0.0.1") {
				t.Errorf("litellm args = %v, want --host 127.0.0.1", child.Args)
			}
		}
	}
}

// A readiness probe that needs credentials is a stack that never comes up.
// OmniRoute answers 401 on /v1/models and 307 on /, both measured; only
// /api/health is 200 from the moment it serves.
func TestTheReadinessProbeNeedsNoCredentials(t *testing.T) {
	t.Parallel()

	for backend, want := range map[string]string{
		"litellm":   "/health/liveliness",
		"omniroute": "/api/health",
	} {
		if got := gatewayReady(backend); got != want {
			t.Errorf("gatewayReady(%s) = %q, want %q", backend, got, want)
		}
	}
	if gatewayReady("omniroute") == "/v1/models" {
		t.Error("/v1/models answers 401 before a token is minted, so it can never report ready")
	}
}

// Everything a gateway downloads goes under the install. Without these it
// writes to the person's home directory, and removing Openarity would leave a
// Python, a Node and a gigabyte of packages behind.
func TestAGatewayWritesNothingOutsideItsOwnDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "gateway")

	for name, env := range map[string][]string{"uv": uvEnv(root), "node": nodeEnv(root)} {
		var home bool
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")

			if key == "HOME" || key == "USERPROFILE" {
				home = true
				if value != root {
					t.Errorf("%s sets %s=%s, want the install's own directory", name, key, value)
				}
				continue
			}

			// Any path it is given must be inside the install. PATH is the
			// exception: it is the machine's, and is how a child finds a
			// shell at all.
			if key == "PATH" || !strings.HasPrefix(value, "/") {
				continue
			}
			if !strings.HasPrefix(value, root) {
				t.Errorf("%s sets %s=%s, which is outside %s", name, key, value, root)
			}
		}
		if !home {
			t.Errorf("%s sets no HOME, so it writes configuration into the person's own", name)
		}
	}
}

// An environment inherited wholesale would hand a gateway every OPENARITY_
// variable in the shell that ran setup.
func TestAGatewayDoesNotInheritTheShell(t *testing.T) {
	// No t.Parallel: t.Setenv and parallel subtests are mutually exclusive.
	t.Setenv("OPENARITY_POSTGRES_DSN", "postgres://someone:hunter2@example.com/db")

	for name, env := range map[string][]string{"uv": uvEnv(t.TempDir()), "node": nodeEnv(t.TempDir())} {
		for _, entry := range env {
			if strings.HasPrefix(entry, "OPENARITY_") {
				t.Errorf("%s carries %q from the shell", name, entry)
			}
		}
	}
}

// npm is a JavaScript file rather than a program, and node is not in the same
// place on Windows as everywhere else. Running the bin/npm shim instead is a
// symlink on Unix and a shell script on Windows, neither of which exec cleanly.
func TestTheRuntimePathsMatchTheArchiveLayout(t *testing.T) {
	t.Parallel()

	// Built with filepath.Join, not written as a POSIX string: on Windows the
	// paths under test come back with backslashes and would not have this as
	// a prefix.
	root := filepath.Join(string(filepath.Separator)+"install", "gateway")

	node, npm := nodeExe(root), npmCLI(root)
	if !strings.HasPrefix(node, root) || !strings.HasPrefix(npm, root) {
		t.Errorf("node=%q npm=%q, want both inside %s", node, npm, root)
	}
	if !strings.HasSuffix(npm, "npm-cli.js") {
		t.Errorf("npmCLI() = %q, want the JavaScript entry point", npm)
	}

	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(node, "node.exe") {
			t.Errorf("nodeExe() = %q, want node.exe", node)
		}
		return
	}
	if !strings.HasSuffix(node, filepath.Join("bin", "node")) {
		t.Errorf("nodeExe() = %q, want bin/node", node)
	}
	if !strings.Contains(npm, filepath.Join("lib", "node_modules")) {
		t.Errorf("npmCLI() = %q, want it under lib/node_modules", npm)
	}
}

func TestNoGatewayIsBuiltForABackendThatRunsNothing(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"external", "", "ollama"} {
		if child := gatewayChild(engine.Settings{ModelBackend: backend}, nil, 20128, "/dev/null"); child != nil {
			t.Errorf("gatewayChild(%q) = %+v, want nothing to supervise", backend, child)
		}
	}
}

// OmniRoute's own image defaults its dashboard password to CHANGEME and logs a
// warning nobody reads. A gateway installed without being asked would come up
// with a password every reader of its documentation knows.
func TestOmniRouteIsGivenADashboardPassword(t *testing.T) {
	t.Parallel()

	child := gatewayChild(
		engine.Settings{ModelBackend: "omniroute", ModelPath: "/somewhere"},
		map[string]string{"OPENARITY_GATEWAY_PASSWORD": "a-generated-one"},
		20128, "/dev/null")

	var given string
	for _, entry := range child.Env {
		if after, ok := strings.CutPrefix(entry, "INITIAL_PASSWORD="); ok {
			given = after
		}
	}
	if given != "a-generated-one" {
		t.Errorf("INITIAL_PASSWORD=%q, want the one setup generated", given)
	}
	if given == "CHANGEME" {
		t.Error("the gateway came up with the documented default")
	}
}

// A gateway on somebody's laptop reaching the internet on a timer is a
// surprise, and neither sync affects routing.
func TestOmniRouteDoesNotPhoneHomeOnATimer(t *testing.T) {
	t.Parallel()

	child := gatewayChild(
		engine.Settings{ModelBackend: "omniroute", ModelPath: "/somewhere"}, nil, 20128, "/dev/null")

	for _, want := range []string{"ARENA_ELO_SYNC_ENABLED=false", "PRICING_SYNC_ENABLED=false"} {
		found := false
		for _, entry := range child.Env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not set, so the gateway syncs on a schedule", want)
		}
	}
}
