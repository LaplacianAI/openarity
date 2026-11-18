package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/daytona"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/e2b"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/mcproom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxd"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

// revision is the commit this binary was built from, set by the image build
// (-ldflags "-X main.revision=<commit>"), which has no .git to stamp it from.
// The room image published with this brain is tagged with it.
var revision string

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// buildRevision is the commit the brain was built from: the one the image
// build named, or the one Go stamped into a build made in a checkout, unless
// the checkout had uncommitted changes, which no published image matches. It
// is empty when neither is known, and the room image's latest is used then.
func buildRevision() string {
	if commitPattern.MatchString(revision) {
		return revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var commit, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if modified == "true" || !commitPattern.MatchString(commit) {
		return ""
	}
	return commit
}

// roomImages is the room image the brain chooses for each provider.
func roomImages(cfg *config.Config) keeper.Images {
	return keeper.Images{Override: cfg.SandboxImage, Commit: buildRevision()}
}

// newSandboxConnect is a client for a team's sandbox provider, opened with
// the team's key. client decides what the provider may be dialled at: sandboxd
// is the operator's own, on an address MCP_PRIVATE_NETWORKS allows.
func newSandboxConnect(client *http.Client) keeper.Connect {
	return func(p sandbox.Provider, url, key string) (sandbox.Rooms, error) {
		switch p {
		case sandbox.E2B:
			return e2b.New(key, client), nil
		case sandbox.Daytona:
			return daytona.New(url, key, client), nil
		case sandbox.Sandboxd:
			if url == "" {
				return nil, fmt.Errorf("sandboxd needs the address of its instance")
			}
			return sandboxd.New(url, key, client), nil
		}
		return nil, fmt.Errorf("%q is not a sandbox provider", string(p))
	}
}

// roomSecrets is a secret store that reads and writes, which the keeper needs
// for the key it reads and the tokens it keeps.
type roomSecrets struct {
	secrets.Store
	secrets.Writer
}

// runCommandsInRooms lets the discoverer run a team's command servers in its
// sandbox. When there is no keeper it leaves it refusing them.
func runCommandsInRooms(d *discovery.Discoverer, cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store) {
	if k := newKeeper(cfg, logger, dbStore, secretStore); k != nil {
		d.RunsCommandsIn(mcproom.New(k))
	}
}

// newKeeper is the keeper of a team's rooms, or nil when the secret store
// cannot write: a room's token has nowhere to be kept then, and command
// servers are refused as they are with no sandbox.
func newKeeper(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store) *keeper.Keeper {
	w, ok := secretStore.(secrets.Writer)
	if !ok || dbStore == nil {
		return nil
	}
	client := discovery.PlainClient(discovery.Policy{Allow: reachable(cfg.MCPPrivateNetworks)})
	return keeper.New(dbStore, roomSecrets{secretStore, w}, newSandboxConnect(client), client, roomImages(cfg), logger)
}
