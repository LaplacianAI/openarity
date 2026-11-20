// Package workroom is a session's workspace room: where an agent's Bash,
// Read, Write, Edit, Glob and Grep run. It is the keeper's second caller, and
// all it adds is what a workspace needs of a room: the workspace role
// (apps/sandbox) as its command, the public internet as its network, and a
// workspace's size. Making, finding and replacing the room is the keeper's,
// so it runs on any provider that runs a command MCP server, or refuses with
// sandbox.ErrRefused when the provider cannot keep a public room off private
// addresses.
//
// One room per session, shared by every run on it, delegates included: a
// file one agent writes is there for the next.
package workroom

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
)

const (
	// OwnerKind is what a workspace room is owned as; the database names it
	// too, in sandbox_rooms_owner_kind_known.
	OwnerKind = "session"

	// Port is where the workspace listens in the room.
	Port = 8081
	// TokenEnv is the variable the workspace reads its bearer token from.
	TokenEnv = "WORKSPACE_TOKEN"
	// Dir is the workspace's directory in the room, where commands run and
	// relative paths start.
	Dir = "/workspace"

	healthPath = "/healthz"
)

// Ensurer is the keeper, as this package uses it.
type Ensurer interface {
	Ensure(ctx context.Context, o keeper.Owner, team uuid.UUID, w keeper.Want) (sandbox.Endpoint, error)
}

// Rooms are sessions' workspaces in their team's sandbox.
type Rooms struct{ keeper Ensurer }

func New(k Ensurer) *Rooms { return &Rooms{keeper: k} }

// Ensure is the endpoint of the session's workspace, starting its room if
// there is none.
func (r *Rooms) Ensure(ctx context.Context, team, session uuid.UUID) (sandbox.Endpoint, error) {
	return r.keeper.Ensure(ctx, keeper.Owner{Kind: OwnerKind, ID: session}, team, Want())
}

// Want is what every workspace is made from. Nothing in it is a session's
// own, so every session's room is the same room until it is used.
func Want() keeper.Want {
	return keeper.Want{
		Argv:       []string{"sandbox", "workspace", "--listen", fmt.Sprintf(":%d", Port), "--root", Dir},
		Network:    sandbox.NetworkPublic,
		Workspace:  true,
		Port:       Port,
		HealthPath: healthPath,
		TokenEnv:   TokenEnv,
	}
}
