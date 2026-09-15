package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

	// nodeBin is what goes on PATH, and it is the directory the archive puts
	// node, npm and npx in — beside each other, and in different places on
	// Windows. Asserted against the layout rather than against nodeBin's own
	// answer, which would agree with itself whatever it said.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(node, "node.exe") {
			t.Errorf("nodeExe() = %q, want node.exe", node)
		}
		if got, want := nodeBin(root), filepath.Join(root, "node"); got != want {
			t.Errorf("nodeBin() = %q, want %q", got, want)
		}
		return
	}
	if !strings.HasSuffix(node, filepath.Join("bin", "node")) {
		t.Errorf("nodeExe() = %q, want bin/node", node)
	}
	if got, want := nodeBin(root), filepath.Join(root, "node", "bin"); got != want {
		t.Errorf("nodeBin() = %q, want %q", got, want)
	}
	if filepath.Dir(node) != nodeBin(root) {
		t.Errorf("nodeBin() = %q but node is in %q; PATH would not find it",
			nodeBin(root), filepath.Dir(node))
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

// `npm install` of a package with a native addon runs
// `sh -c node scripts/build-from-source.js`. We drive npm as
// `node npm-cli.js`, which needs nothing on PATH, so this stayed invisible
// until a dependency wanted node itself — and then it was
// "sh: node: command not found", minutes into the install, on a machine that
// was never meant to have a node of its own.
func TestNodeIsOnThePathItGivesItsChildren(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "gateway")

	var path string
	for _, entry := range nodeEnv(root) {
		if key, value, _ := strings.Cut(entry, "="); key == "PATH" {
			path = value
		}
	}
	if path == "" {
		t.Fatal("nodeEnv() sets no PATH")
	}

	first, rest, _ := strings.Cut(path, string(os.PathListSeparator))
	if first != nodeBin(root) {
		t.Errorf("PATH starts %q, want the node we downloaded at %q", first, nodeBin(root))
	}
	// And the machine's own PATH is still behind it, because a package script
	// reaching for sh, git or a compiler has to find one.
	if rest != os.Getenv("PATH") {
		t.Errorf("PATH after the node directory = %q, want the machine's own", rest)
	}
}

// The supervised gateway gets the same treatment: OmniRoute runs under node
// and spawns node, so a PATH that was only right during the install would
// fail at start instead.
func TestTheRunningGatewayAlsoFindsNode(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "gateway")
	child := gatewayChild(
		engine.Settings{ModelBackend: "omniroute", ModelPath: root},
		map[string]string{}, 20128, filepath.Join(root, "gateway.log"))
	if child == nil {
		t.Fatal("gatewayChild() = nil for omniroute")
	}

	for _, entry := range child.Env {
		if key, value, _ := strings.Cut(entry, "="); key == "PATH" {
			if !strings.HasPrefix(value, nodeBin(root)) {
				t.Errorf("the gateway's PATH = %q, want it to start with %q", value, nodeBin(root))
			}
			return
		}
	}
	t.Error("the gateway is started with no PATH at all")
}

// "uv brings its own Python" was not true. uv takes whatever python3 is on
// PATH when it satisfies the requirement, and on macOS — which still ships
// 3.9.6 — it satisfies nothing, so `uv tool install litellm[proxy]` ended in
// "No solution found when resolving dependencies", naming a Python nobody
// asked it to use.
func TestLiteLLMGetsAPythonWeChoseRatherThanTheMachines(t *testing.T) {
	t.Parallel()

	env := map[string]string{}
	for _, entry := range uvEnv(filepath.Join(t.TempDir(), "gateway")) {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}

	if env["UV_PYTHON"] != engine.GatewayPythonVersion {
		t.Errorf("UV_PYTHON = %q, want the pinned %q", env["UV_PYTHON"], engine.GatewayPythonVersion)
	}
	// only-managed rather than the default: a machine that does have a
	// suitable python3 would otherwise install against it, and removing that
	// Python later would break an Openarity that looked fine.
	if env["UV_PYTHON_PREFERENCE"] != "only-managed" {
		t.Errorf("UV_PYTHON_PREFERENCE = %q, want only-managed", env["UV_PYTHON_PREFERENCE"])
	}
	if env["UV_PYTHON_INSTALL_DIR"] == "" {
		t.Error("UV_PYTHON_INSTALL_DIR is unset, so a downloaded Python lands outside the install")
	}
}

// The pin has to be a version LiteLLM accepts: it requires >=3.10,<3.15, and
// the failure for a version outside that is a resolver message rather than
// anything naming the pin.
func TestThePinnedPythonIsOneLiteLLMAccepts(t *testing.T) {
	t.Parallel()

	major, minor, ok := strings.Cut(engine.GatewayPythonVersion, ".")
	if !ok || major != "3" {
		t.Fatalf("GatewayPythonVersion = %q, want a 3.x version", engine.GatewayPythonVersion)
	}
	n, err := strconv.Atoi(minor)
	if err != nil {
		t.Fatalf("GatewayPythonVersion = %q: %v", engine.GatewayPythonVersion, err)
	}
	if n < 10 || n >= 15 {
		t.Errorf("GatewayPythonVersion = %q, outside LiteLLM's >=3.10,<3.15",
			engine.GatewayPythonVersion)
	}
}

// Same reason as node's: uv says "`<root>/bin` is not on your PATH" at the end
// of every install, and litellm ships three executables that may reach for
// each other.
func TestTheToolsUvInstallsAreOnThePath(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "gateway")
	for _, entry := range uvEnv(root) {
		if key, value, _ := strings.Cut(entry, "="); key == "PATH" {
			first, _, _ := strings.Cut(value, string(os.PathListSeparator))
			if first != filepath.Join(root, "bin") {
				t.Errorf("PATH starts %q, want the tools uv installed at %q",
					first, filepath.Join(root, "bin"))
			}
			return
		}
	}
	t.Error("uvEnv() sets no PATH")
}
