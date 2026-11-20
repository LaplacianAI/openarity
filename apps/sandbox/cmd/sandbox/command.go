package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/rooms"
)

// role is which part of the sandbox this process is; the others come with
// the slices that build them.
type role string

const (
	roleShim      role = "shim"
	roleServe     role = "serve"
	roleWorkspace role = "workspace"
)

type command struct {
	role role

	// shim and workspace
	listen string
	token  string
	argv   []string
	root   string // workspace

	// serve
	serve serveSettings
}

// serveSettings are sandboxd's, all from the environment but the listen
// address.
type serveSettings struct {
	token     string
	publicURL string
	socket    string
	self      string
	limits    rooms.Limits
	images    []string // repository prefixes a room's image may be under
	pull      []string // images to pull in the background at start
}

// defaultImages is the room image Openarity publishes.
const defaultImages = "ghcr.io/laplacianai/openarity-sandbox"

const usage = `usage: sandbox shim [--listen ADDR] -- COMMAND [ARGS...]
       sandbox serve [--listen ADDR]
       sandbox workspace [--listen ADDR] [--root DIR]

  serve  the self-hosted sandbox provider: rooms on a rootless Docker daemon.
         Environment: SANDBOXD_TOKEN, SANDBOXD_PUBLIC_URL, SANDBOXD_DOCKER_SOCKET
         (required); SANDBOXD_CONTAINER (default $HOSTNAME), SANDBOXD_MEMORY_BUDGET,
         SANDBOXD_ROOM_MEMORY, SANDBOXD_ROOM_HOME, SANDBOXD_ROOM_PIDS, SANDBOXD_ROOM_CPUS (optional)
         SANDBOXD_IMAGES: the image prefixes a room may run, comma-separated
         (default ghcr.io/laplacianai/openarity-sandbox); SANDBOXD_PULL: images
         to pull in the background at start, each one allowed (optional);
         sandboxd pulls an allowed image it does not have

  shim   serve an MCP server that speaks stdio over Streamable HTTP, at /mcp;
         SHIM_TOKEN is the bearer token every caller must send

  workspace  serve an agent's Bash, Read, Write, Edit, Glob and Grep in this
         room, rooted at --root (default /workspace); WORKSPACE_TOKEN is the
         bearer token every caller must send, and no command sees it`

// parse reads the arguments and the environment it is given; it starts
// nothing.
func parse(args []string, env func(string) string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New(usage)
	}
	switch role(args[0]) {
	case roleShim:
		return parseShim(args[1:], env)
	case roleServe:
		return parseServe(args[1:], env)
	case roleWorkspace:
		return parseWorkspace(args[1:], env)
	default:
		return command{}, fmt.Errorf("unknown role %q\n%s", args[0], usage)
	}
}

func parseShim(args []string, env func(string) string) (command, error) {
	c := command{role: roleShim}
	flags := flag.NewFlagSet("shim", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&c.listen, "listen", ":8080", "the address to serve on")
	if err := flags.Parse(args); err != nil {
		return command{}, fmt.Errorf("%w\n%s", err, usage)
	}
	c.argv = flags.Args()
	if len(c.argv) == 0 {
		return command{}, fmt.Errorf("the shim needs the server's command after --\n%s", usage)
	}
	c.token = env("SHIM_TOKEN")
	if c.token == "" {
		return command{}, errors.New("SHIM_TOKEN is required: every caller must send it")
	}
	return c, nil
}

// WorkspaceTokenEnv is where the workspace reads its bearer token from.
const WorkspaceTokenEnv = "WORKSPACE_TOKEN"

func parseWorkspace(args []string, env func(string) string) (command, error) {
	c := command{role: roleWorkspace}
	flags := flag.NewFlagSet("workspace", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&c.listen, "listen", ":8081", "the address to serve on")
	flags.StringVar(&c.root, "root", "/workspace", "the directory commands run in and paths start from")
	if err := flags.Parse(args); err != nil {
		return command{}, fmt.Errorf("%w\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return command{}, fmt.Errorf("workspace takes no arguments\n%s", usage)
	}
	if !filepath.IsAbs(c.root) {
		return command{}, fmt.Errorf("--root must be an absolute path, not %q", c.root)
	}
	c.token = env(WorkspaceTokenEnv)
	if c.token == "" {
		return command{}, errors.New(WorkspaceTokenEnv + " is required: every caller must send it")
	}
	return c, nil
}

func parseServe(args []string, env func(string) string) (command, error) {
	c := command{role: roleServe}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&c.listen, "listen", ":9090", "the address to serve on")
	if err := flags.Parse(args); err != nil {
		return command{}, fmt.Errorf("%w\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return command{}, fmt.Errorf("serve takes no arguments\n%s", usage)
	}
	s := serveSettings{
		token:     env("SANDBOXD_TOKEN"),
		publicURL: env("SANDBOXD_PUBLIC_URL"),
		socket:    env("SANDBOXD_DOCKER_SOCKET"),
		self:      env("SANDBOXD_CONTAINER"),
		limits:    rooms.DefaultLimits(),
	}
	if s.self == "" {
		s.self = env("HOSTNAME")
	}
	switch {
	case s.token == "":
		return command{}, errors.New("SANDBOXD_TOKEN is required: every request must send it")
	case s.socket == "":
		return command{}, errors.New("SANDBOXD_DOCKER_SOCKET is required: the path of a rootless Docker daemon's socket")
	case s.self == "":
		return command{}, errors.New("SANDBOXD_CONTAINER is required when HOSTNAME is not set: sandboxd's own container joins every room's network")
	}
	u, err := url.Parse(s.publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return command{}, errors.New("SANDBOXD_PUBLIC_URL is required and must be an http(s) URL: where the brain reaches sandboxd")
	}
	for _, n := range []struct {
		name string
		to   *int64
		mul  float64
	}{
		{"SANDBOXD_MEMORY_BUDGET", &s.limits.MemoryBudget, 1},
		{"SANDBOXD_ROOM_MEMORY", &s.limits.Memory, 1},
		{"SANDBOXD_ROOM_HOME", &s.limits.HomeBytes, 1},
		{"SANDBOXD_ROOM_PIDS", &s.limits.PidsLimit, 1},
		{"SANDBOXD_ROOM_CPUS", &s.limits.NanoCPUs, 1e9},
	} {
		v := env(n.name)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			return command{}, fmt.Errorf("%s must be a positive number, got %q", n.name, v)
		}
		*n.to = int64(f * n.mul)
	}
	if s.limits.Memory > s.limits.MemoryBudget {
		return command{}, errors.New("SANDBOXD_ROOM_MEMORY is more than SANDBOXD_MEMORY_BUDGET: no room could be made")
	}
	// A tmpfs is memory charged to the room, so a home larger than the room
	// would be killed for filling it rather than told it is full.
	if s.limits.HomeBytes > s.limits.Memory {
		return command{}, errors.New("SANDBOXD_ROOM_HOME is more than SANDBOXD_ROOM_MEMORY: the home directory is memory, charged to the room")
	}
	if s.images, err = parseImages(env("SANDBOXD_IMAGES")); err != nil {
		return command{}, err
	}
	if s.pull, err = parsePull(env("SANDBOXD_PULL"), s.images); err != nil {
		return command{}, err
	}
	c.serve = s
	return c, nil
}

// parseImages reads SANDBOXD_IMAGES: repository prefixes, with no tag or
// digest, because a prefix with one could never match a boundary.
func parseImages(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		v = defaultImages
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			return nil, errors.New("SANDBOXD_IMAGES has an empty entry: check for a stray comma")
		case strings.ContainsAny(p, " \t@") || strings.HasSuffix(p, "/"):
			return nil, fmt.Errorf("SANDBOXD_IMAGES entry %q is not a repository prefix: no digest, spaces or trailing slash", p)
		case strings.Contains(p[strings.LastIndex(p, "/")+1:], ":"):
			return nil, fmt.Errorf("SANDBOXD_IMAGES entry %q has a tag: list the repository, such as ghcr.io/org/room", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// parsePull reads SANDBOXD_PULL: full image references, each of which
// sandboxd would run, so a warm-up cannot pull what a create would refuse.
func parsePull(v string, allowed []string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var out []string
	for _, ref := range strings.Split(v, ",") {
		ref = strings.TrimSpace(ref)
		switch {
		case ref == "":
			return nil, errors.New("SANDBOXD_PULL has an empty entry: check for a stray comma")
		case !rooms.Allowed(ref, allowed):
			return nil, fmt.Errorf("SANDBOXD_PULL entry %q is not under SANDBOXD_IMAGES (%s)", ref, strings.Join(allowed, ", "))
		}
		out = append(out, ref)
	}
	return out, nil
}
