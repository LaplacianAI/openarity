// Package trigger is what oa schedules and oa watchers share: their
// permissions, how a command finds the one it names, and how a time, a
// length, a place, an agent and an objective are read from flags and said
// back. It is not a command.
package trigger

import (
	"context"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// The permissions schedules and watchers both take.
const (
	ReadPermission   = "schedule:read"
	WritePermission  = "schedule:write"
	ManagePermission = "schedule:manage"
)

// TargetHelp is --target's help, the same on both create commands.
const TargetHelp = "where it answers: own (the default), chat:<session>, channel:<channel>:<ref> or direct:<channel>"

// Resolver finds one schedule or watcher by name or id; with everyone it
// looks among everyone's, not only the caller's.
type Resolver func(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, ref string, everyone bool) (uuid.UUID, error)

// One is the team args[0] names and the item args[1] names in it.
func One(
	ctx context.Context, opts *cli.Options, args []string, resolve Resolver, everyone bool,
) (*client.ClientWithResponses, uuid.UUID, uuid.UUID, error) {
	api, err := opts.API(ctx)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	team, err := cli.ResolveTeam(ctx, api, args[0])
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	id, err := resolve(ctx, api, team, args[1], everyone)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	return api, team, id, nil
}
