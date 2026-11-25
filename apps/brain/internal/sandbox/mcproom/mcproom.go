// Package mcproom runs a team's command MCP server in a sandbox room. It is
// the keeper's first caller, and all it adds is what a command server needs
// of a room: the shim around the command, which serves it over Streamable
// HTTP at /mcp, and the environment npx and uvx want. Finding, making,
// replacing and deleting the room is the keeper's.
package mcproom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/roomfile"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// OwnerKind is what a server's room is owned as; the database names it
	// too, in sandbox_rooms_owner_kind_known.
	OwnerKind = "mcp_server"

	// TokenEnv is the variable the shim reads the bearer token it demands from.
	TokenEnv = "SHIM_TOKEN"

	mcpPath    = "/mcp"
	healthPath = "/healthz"
)

// ErrNotACommand is a server with no command to run.
var ErrNotACommand = errors.New("the server has no command to run")

// Keeper is the keeper, as this package uses it: Ensure starts a server's
// room, and Find reads one that runs, starting none.
type Keeper interface {
	Ensure(ctx context.Context, o keeper.Owner, team uuid.UUID, w keeper.Want) (sandbox.Endpoint, error)
	Find(ctx context.Context, o keeper.Owner, team uuid.UUID, w keeper.Want) (sandbox.Endpoint, error)
}

// Rooms runs command servers in their team's sandbox.
type Rooms struct {
	keeper  Keeper
	secrets secrets.Store
}

// New takes the secret store a server's env references are read from.
func New(k Keeper, sec secrets.Store) *Rooms {
	return &Rooms{keeper: k, secrets: sec}
}

// Ensure is the endpoint of the server's room, its address at /mcp and the
// headers to call it with, starting the server if it is not running.
func (r *Rooms) Ensure(ctx context.Context, team uuid.UUID, s db.McpServer) (sandbox.Endpoint, error) {
	w, err := Want(s)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	if w.Secrets, err = r.resolve(ctx, s); err != nil {
		return sandbox.Endpoint{}, err
	}
	return r.keeper.Ensure(ctx, keeper.Owner{Kind: OwnerKind, ID: s.ID}, team, w)
}

// Files is where the server's room hands over a file the server saved in it:
// the shim's /file, in the room as it runs now. A room that is not running
// has none of what the server saved, so none is started: sandbox.ErrNoRoom.
func (r *Rooms) Files(ctx context.Context, team uuid.UUID, s db.McpServer) (sandbox.Endpoint, error) {
	w, err := Want(s)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	w.Path = roomfile.Route
	return r.keeper.Find(ctx, keeper.Owner{Kind: OwnerKind, ID: s.ID}, team, w)
}

// Want is what a command server needs of its room: the shim, started with the
// server's command after it, the server's own settings as its environment, and
// the places it may connect.
//
// The environment holds the server's plain settings and what npm wants, and
// nothing of the sandbox provider's: the provider's key goes to the provider,
// and the keeper adds the room's own token, which is the shim's.
func Want(s db.McpServer) (keeper.Want, error) {
	if len(s.Command) == 0 {
		return keeper.Want{}, ErrNotACommand
	}

	var vars map[string]string
	if err := unmarshalOrEmpty(s.Vars, &vars); err != nil {
		return keeper.Want{}, fmt.Errorf("reading the server's settings: %w", err)
	}
	var egress []sandbox.HostPort
	if err := unmarshalOrEmpty(s.Egress, &egress); err != nil {
		return keeper.Want{}, fmt.Errorf("reading the server's egress: %w", err)
	}

	env := make(map[string]string, len(vars)+4)
	for name, v := range vars {
		env[name] = v
	}
	if path.Base(s.Command[0]) == "npx" {
		// npx asks before installing a package, checks for a newer npm and
		// audits what it fetched, and a room has nobody to answer or read.
		// Set after the server's own, so they cannot be undone.
		env["npm_config_update_notifier"] = "false"
		env["npm_config_fund"] = "false"
		env["npm_config_audit"] = "false"
		env["npm_config_yes"] = "true"
	}

	argv := append([]string{"sandbox", "shim", "--listen", fmt.Sprintf(":%d", sandbox.ShimPort), "--"}, s.Command...)
	return keeper.Want{
		Argv: argv, Env: env, Egress: egress,
		Port: sandbox.ShimPort, Path: mcpPath, HealthPath: healthPath, TokenEnv: TokenEnv,
	}, nil
}

func unmarshalOrEmpty(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// resolve reads each of the server's env references, path#key, from the
// secret store: the values the command reads, such as a mailbox's password.
// An error names the variable and never its value.
func (r *Rooms) resolve(ctx context.Context, s db.McpServer) (map[string]string, error) {
	var refs map[string]string
	if err := unmarshalOrEmpty(s.Env, &refs); err != nil {
		return nil, fmt.Errorf("reading the server's env: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(refs))
	for _, name := range slices.Sorted(maps.Keys(refs)) {
		ref := refs[name]
		i := strings.LastIndexByte(ref, '#')
		if i < 0 {
			return nil, fmt.Errorf("env %s is not a path#key reference", name)
		}
		v, err := r.secrets.Get(ctx, ref[:i], ref[i+1:])
		if err == nil && v == "" {
			err = secrets.ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("reading env %s: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}
