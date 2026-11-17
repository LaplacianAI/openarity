package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// role is which part of the sandbox this process is; the others come with
// the slices that build them.
type role string

const roleShim role = "shim"

type command struct {
	role role

	// shim
	listen string
	token  string
	argv   []string
}

const usage = `usage: sandbox shim [--listen ADDR] -- COMMAND [ARGS...]

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
