package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/rooms"
)

// role is which part of the sandbox this process is; the others come with
// the slices that build them.
type role string

const (
	roleShim  role = "shim"
	roleServe role = "serve"
)

type command struct {
	role role

	// shim
	listen string
	token  string
	argv   []string

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
}

const usage = `usage: sandbox shim [--listen ADDR] -- COMMAND [ARGS...]
       sandbox serve [--listen ADDR]

  serve  the self-hosted sandbox provider: rooms on a rootless Docker daemon.
         Environment: SANDBOXD_TOKEN, SANDBOXD_PUBLIC_URL, SANDBOXD_DOCKER_SOCKET
         (required); SANDBOXD_CONTAINER (default $HOSTNAME), SANDBOXD_MEMORY_BUDGET,
         SANDBOXD_ROOM_MEMORY, SANDBOXD_ROOM_PIDS, SANDBOXD_ROOM_CPUS (optional)

  shim   serve an MCP server that speaks stdio over Streamable HTTP, at /mcp;
         SHIM_TOKEN is the bearer token every caller must send`

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
	c.serve = s
	return c, nil
}
