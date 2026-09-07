package stack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// Installing a model gateway, which neither of the two worth running makes
// easy: LiteLLM publishes no binary at all and OmniRoute publishes a desktop
// application. So a runtime is fetched and the gateway installed into it, the
// same way Postgres is fetched rather than assumed.
//
// Everything lands under Settings.ModelPath — the runtime, the packages, the
// caches — so removing that directory removes the gateway and nothing else.

// installGateway provisions the runtime and the gateway itself.
func installGateway(ctx context.Context, d *engine.Downloader, platform engine.Platform, p engine.Plan) error {
	if err := os.MkdirAll(p.Settings.ModelPath, 0o700); err != nil {
		return err
	}

	switch p.Settings.ModelBackend {
	case "litellm":
		return installLiteLLM(ctx, d, platform, p)
	case "omniroute":
		return installOmniRoute(ctx, d, platform, p)
	default:
		return fmt.Errorf("stack: no gateway to install for %q", p.Settings.ModelBackend)
	}
}

// uv is one static binary that provisions its own Python, so there is no
// system interpreter to find, match or apologise for. Everything it downloads
// is kept inside the install rather than in the person's home directory.
func installLiteLLM(ctx context.Context, d *engine.Downloader, platform engine.Platform, p engine.Plan) error {
	url, err := platform.UvURL(engine.UvVersion)
	if err != nil {
		return err
	}

	root := p.Settings.ModelPath
	if err := d.Unpack(ctx, engine.Archive{
		URL: url, ChecksumURL: url + ".sha256",
		Dest: filepath.Join(root, "uv"), Strip: 1,
	}); err != nil {
		return err
	}

	cmd := command(ctx, filepath.Join(root, "uv", "uv"+exeSuffix()),
		"tool", "install", "litellm[proxy]=="+engine.LiteLLMVersion)
	cmd.Env = uvEnv(root)
	return capture(cmd, "uv tool install litellm")
}

// OmniRoute is a Node application published to npm. Node is fetched the same
// way uv is, and npm is driven through node rather than through the bin/npm
// shim, which is a symlink on Unix and a shell script on Windows.
func installOmniRoute(ctx context.Context, d *engine.Downloader, platform engine.Platform, p engine.Plan) error {
	url, err := platform.NodeURL(engine.NodeVersion)
	if err != nil {
		return err
	}
	name, err := platform.NodeArchiveName(engine.NodeVersion)
	if err != nil {
		return err
	}

	root := p.Settings.ModelPath
	if err := d.Unpack(ctx, engine.Archive{
		URL:          url,
		ChecksumURL:  platform.NodeChecksumURL(engine.NodeVersion),
		ChecksumName: name,
		Dest:         filepath.Join(root, "node"),
		Strip:        1,
	}); err != nil {
		return err
	}

	into := filepath.Join(root, "omniroute")
	if err := os.MkdirAll(into, 0o700); err != nil {
		return err
	}

	cmd := command(ctx, nodeExe(root), npmCLI(root),
		"install", "omniroute@"+engine.OmniRouteVersion,
		"--prefix", into, "--no-audit", "--no-fund")
	cmd.Env = nodeEnv(root)
	return capture(cmd, "npm install omniroute")
}

// uvEnv keeps uv's caches, its interpreters and its tools inside the install.
// Without these it writes to the person's home directory, and uninstalling
// Openarity would leave a Python and a gigabyte of packages behind.
func uvEnv(root string) []string {
	return append(baseEnv(), []string{
		"UV_CACHE_DIR=" + filepath.Join(root, "cache"),
		"UV_PYTHON_INSTALL_DIR=" + filepath.Join(root, "python"),
		"UV_TOOL_DIR=" + filepath.Join(root, "tools"),
		"UV_TOOL_BIN_DIR=" + filepath.Join(root, "bin"),
		"HOME=" + root,
		"USERPROFILE=" + root,
	}...)
}

func nodeEnv(root string) []string {
	return append(baseEnv(), []string{
		"npm_config_cache=" + filepath.Join(root, "npm-cache"),
		"npm_config_update_notifier=false",
		"HOME=" + root,
		"USERPROFILE=" + root,
	}...)
}

// baseEnv is the floor a child needs to run at all: a PATH for anything it
// shells out to, and SystemRoot on Windows, where an empty block stops a
// process being created. Deliberately not the parent's whole environment —
// an OPENARITY_* variable in the shell that ran setup must not reach these.
func baseEnv() []string {
	out := []string{"PATH=" + os.Getenv("PATH")}
	if runtime.GOOS == "windows" {
		out = append(out,
			"SystemRoot="+os.Getenv("SystemRoot"),
			"TEMP="+os.Getenv("TEMP"),
			"TMP="+os.Getenv("TMP"))
	}
	return out
}

func nodeExe(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "node", "node.exe")
	}
	return filepath.Join(root, "node", "bin", "node")
}

// npm is a JavaScript file, and where it sits differs: beside node on Windows,
// under lib/ everywhere else.
func npmCLI(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "node", "node_modules", "npm", "bin", "npm-cli.js")
	}
	return filepath.Join(root, "node", "lib", "node_modules", "npm", "bin", "npm-cli.js")
}

// gatewayChild is the supervised process, when this install runs one.
//
// Both are held to loopback deliberately. OmniRoute binds every interface by
// default — measured answering on a LAN address — and a personal install
// promises that nothing but this machine can reach it. Next.js takes its bind
// address from HOSTNAME; LiteLLM takes a --host flag.
func gatewayChild(settings engine.Settings, port int, log string) *engine.Child {
	root := settings.ModelPath

	switch settings.ModelBackend {
	case "litellm":
		return &engine.Child{
			Name: "gateway", Log: log,
			Path: filepath.Join(root, "bin", "litellm"+exeSuffix()),
			Args: []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port)},
			Env:  uvEnv(root),
		}

	case "omniroute":
		return &engine.Child{
			Name: "gateway", Log: log,
			Path: nodeExe(root),
			Args: []string{
				filepath.Join(root, "omniroute", "node_modules", "omniroute", "bin", "omniroute.mjs"),
				"--port", strconv.Itoa(port),
				// A window opening on a server, or during an unattended
				// install, is not a feature.
				"--no-open",
			},
			Env: append(nodeEnv(root),
				// The variable OmniRoute documents, and the only one that
				// works. HOSTNAME is what Next.js reads and it was ignored
				// here — the server came up on 0.0.0.0 and said so, warning
				// that /v1/* was "reachable by ANY device that can route to
				// this host, and requests are billed to your configured
				// providers". A personal install promises otherwise.
				"OMNIROUTE_SERVER_HOST=127.0.0.1",
				"OMNIROUTE_HOME="+filepath.Join(root, "omniroute-data"),
			),
		}

	default:
		return nil
	}
}

// gatewayReady is the path that answers without credentials.
//
// Neither /v1/models nor / will do for OmniRoute: the first is 401 before a
// token is minted and the second is a 307 to the dashboard. /api/health is
// 200 from the moment it is serving, which is the question being asked.
func gatewayReady(backend string) string {
	switch backend {
	case "litellm":
		return "/health/liveliness"
	case "omniroute":
		return "/api/health"
	default:
		return "/"
	}
}
