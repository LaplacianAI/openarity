// Package sandbox is the brain's view of a sandbox provider: somewhere that
// makes rooms, runs a command in one, and gives the address of a port in it.
// A team chooses its provider in settings; each has a client of its own in a
// package below this one, and nothing else in the brain knows which is used.
//
// A command MCP server runs in a room behind the shim (apps/sandbox), which
// serves it over Streamable HTTP, so the brain reaches it like a URL server.
package sandbox

import (
	"context"
	"errors"
	"net/http"
)

type Provider string

const (
	None     Provider = "none"
	E2B      Provider = "e2b"
	Daytona  Provider = "daytona"
	Sandboxd Provider = "sandboxd"
)

// ShimPort is where the shim listens in every room.
const ShimPort = 8080

// A HostPort is one place a room may connect to. It is stored as a command
// MCP server's egress, in this shape.
type HostPort struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// A Spec is what a room is made with. Env holds values, secrets among them:
// a client sends it to its provider and never logs it.
type Spec struct {
	// Image is the provider's own name for what a room starts from: an E2B
	// template, a Daytona image or snapshot, a sandboxd image.
	Image  string
	Env    map[string]string
	Egress []HostPort
	// Labels find a room again; a provider without labels keeps them as
	// metadata or ignores them.
	Labels map[string]string
}

// An Endpoint reaches a port in a room: its base URL, and the headers the
// provider wants on every request to it.
type Endpoint struct {
	URL    string
	Header http.Header
}

// Rooms are one provider's rooms, as one team's credentials reach them.
type Rooms interface {
	// Create makes a room and returns its id. It refuses, with ErrRefused, a
	// spec the provider cannot honour, such as an egress port it cannot
	// allow; nothing is left behind.
	Create(ctx context.Context, s Spec) (string, error)
	// Start runs argv in the room in the background: it returns once the
	// command is started, not when it ends.
	Start(ctx context.Context, room string, argv []string) error
	// URL is the address of port in the room.
	URL(ctx context.Context, room string, port int) (Endpoint, error)
	// Delete removes the room; a room already gone is not an error.
	Delete(ctx context.Context, room string) error
}

var (
	// ErrNotFound is a room the provider no longer has.
	ErrNotFound = errors.New("the sandbox has no such room")
	// ErrRefused is a spec the provider cannot honour.
	ErrRefused = errors.New("the sandbox provider cannot make that room")
)
