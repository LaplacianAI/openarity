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
	// Network is what the room may reach: NetworkListed, its Egress and
	// nothing else; or NetworkPublic, every public address and no private,
	// loopback or link-local one, with Egress empty. A provider that cannot
	// keep a public room off private addresses refuses it with ErrRefused,
	// rather than make a room that reaches them.
	Network Network
	// Workspace is a room an agent works in, which needs a writable
	// /workspace and room to build and run things. The room image has the
	// directory; a provider that sizes rooms per request sizes this one for
	// work, and one whose template sets the size leaves it to the template.
	Workspace bool
	// Size is the team's own size for a workspace room; nil is the
	// provider's. A provider refuses, with ErrRefused, a size past what it
	// allows; one whose template sets the size is given a template built at
	// it.
	Size *Size
	// Labels find a room again; a provider without labels keeps them as
	// metadata or ignores them.
	Labels map[string]string
}

// Size is a workspace room's memory, CPUs and disk.
type Size struct {
	MemoryMB int `json:"memory_mb"`
	CPUs     int `json:"cpus"`
	DiskMB   int `json:"disk_mb"`
}

// A Network is what a room may reach.
type Network string

const (
	// NetworkListed is the zero value: the room's Egress, and nothing else.
	NetworkListed Network = ""
	// NetworkPublic is the public internet and nothing private.
	NetworkPublic Network = "public"
)

// PrivateNetworks are the ranges a public room must never reach, for a
// provider that takes them as a deny list: private, carrier-grade NAT,
// loopback, link-local (cloud metadata among it), and their IPv6 kin.
var PrivateNetworks = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"::1/128", "fc00::/7", "fe80::/10",
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
	// Stop stops the room, keeping its files: a stopped room holds none of
	// the provider's memory. One already stopped is not an error; one gone
	// is ErrNotFound.
	Stop(ctx context.Context, room string) error
	// Resume starts a stopped room again, its files as they were, with argv
	// running in it as Start runs it; a provider whose rooms keep their
	// process across a stop ignores argv. One that runs is not an error, one
	// gone is ErrNotFound, and one the provider has no room for now is
	// ErrFull.
	Resume(ctx context.Context, room string, argv []string) error
}

var (
	// ErrNotFound is a room the provider no longer has.
	ErrNotFound = errors.New("the sandbox has no such room")
	// ErrRefused is a spec the provider cannot honour.
	ErrRefused = errors.New("the sandbox provider cannot make that room")
	// ErrNoProvider is a team that has chosen no sandbox: nothing can be run
	// for it until it chooses one in Settings.
	ErrNoProvider = errors.New("the team has no sandbox: choose one in Settings to run command MCP servers")
	// ErrNotReady is a room that was made and did not answer in time. The
	// room is deleted, and the next call starts another.
	ErrNotReady = errors.New("the sandbox room did not start in time")
	// ErrReplaced is a workspace room that could not be used as it was (it
	// was lost, did not come back from a stop, or was made at another size)
	// and was replaced by a fresh one, without its files. The call that found
	// it did not run, so the model hears its earlier files are gone; the next
	// call uses the fresh room.
	ErrReplaced = errors.New("the workspace could not be brought back as it was, so a fresh one was " +
		"started; what was in it before is not there. Nothing ran: call again")
	// ErrFull is a room the provider has no room for now: every share of its
	// memory or disk is in use. A later call may find one.
	ErrFull = errors.New("the team's sandbox is full: every room's share is in use. Nothing ran; " +
		"try again once another conversation's workspace is idle")
	// ErrPreparing is a room that cannot be made yet, because what it starts
	// from is still being built: an E2B team's template, the first time and
	// after an upgrade. Nothing was made; a later call can be.
	ErrPreparing = errors.New("the team's sandbox is still being prepared, which takes a few minutes " +
		"the first time and after an upgrade; nothing ran, so try again in a few minutes")
)

// A Build is one build of an E2B team's room template, as E2B names it.
type Build struct {
	TemplateID string `json:"templateID"`
	BuildID    string `json:"buildID"`
}

// BuildState is where a template's build is.
type BuildState string

const (
	BuildRunning BuildState = "building"
	BuildReady   BuildState = "ready"
	BuildFailed  BuildState = "failed"
)
