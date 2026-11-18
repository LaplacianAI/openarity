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
	"path"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
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

// Ensurer is the keeper, as this package uses it.
type Ensurer interface {
	Ensure(ctx context.Context, o keeper.Owner, team uuid.UUID, w keeper.Want) (sandbox.Endpoint, error)
}

// Rooms runs command servers in their team's sandbox.
type Rooms struct {
	keeper Ensurer
}

func New(k Ensurer) *Rooms {
	return &Rooms{keeper: k}
}

// Ensure is the endpoint of the server's room, its address at /mcp and the
// headers to call it with, starting the server if it is not running.
func (r *Rooms) Ensure(ctx context.Context, team uuid.UUID, s db.McpServer) (sandbox.Endpoint, error) {
	w, err := Want(s)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	return r.keeper.Ensure(ctx, keeper.Owner{Kind: OwnerKind, ID: s.ID}, team, w)
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
