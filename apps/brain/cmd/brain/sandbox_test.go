package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/daytona"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/e2b"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxd"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The commit the image build names is the commit the room image is tagged
// with; anything that is not a commit is not trusted to name an image.
// Tests that set the package variable run one at a time.
func TestTheRevisionTheImageBuildNamesIsUsed(t *testing.T) {
	old := revision
	t.Cleanup(func() { revision = old })

	revision = "0123456789abcdef0123456789abcdef01234567"
	if got := buildRevision(); got != revision {
		t.Errorf("buildRevision = %q, want the named commit", got)
	}

	for _, bad := range []string{"v1.2.3", "main", "abc", "0123456789ABCDEF", "$(id)", "a b c d e f g h"} {
		revision = bad
		// What is left is Go's own stamp, which a test binary does not carry
		// in a form that is a commit, or a real one: never what was named.
		if got := buildRevision(); got == bad {
			t.Errorf("%q was accepted as a commit", bad)
		}
	}
}

func TestTheRoomImageIsChosenFromTheCommitAndTheOverride(t *testing.T) {
	old := revision
	t.Cleanup(func() { revision = old })
	revision = "0123456789abcdef0123456789abcdef01234567"

	images := roomImages(&config.Config{})
	if got, want := images.For(sandbox.Sandboxd, ""), "ghcr.io/laplacianai/openarity-sandbox:sha-0123456789abcdef0123456789abcdef01234567"; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}

	images = roomImages(&config.Config{SandboxImage: "registry.example/room:7"})
	if got := images.For(sandbox.Daytona, ""); got != "image:registry.example/room:7" {
		t.Errorf("image = %q, want the operator's", got)
	}
}

// Each provider is opened with the team's key, and sandboxd with its address.
func TestEachSandboxProviderIsOpenedWithTheTeamsKey(t *testing.T) {
	t.Parallel()

	connect := newSandboxConnect(http.DefaultClient)

	e, err := connect(sandbox.E2B, "", "key")
	if _, ok := e.(*e2b.Client); !ok || err != nil {
		t.Errorf("e2b: %T, %v", e, err)
	}
	d, err := connect(sandbox.Daytona, "", "key")
	if _, ok := d.(*daytona.Client); !ok || err != nil {
		t.Errorf("daytona: %T, %v", d, err)
	}
	s, err := connect(sandbox.Sandboxd, "https://sbx.example", "key")
	if _, ok := s.(*sandboxd.Client); !ok || err != nil {
		t.Errorf("sandboxd: %T, %v", s, err)
	}

	if _, err := connect(sandbox.Sandboxd, "", "key"); err == nil {
		t.Error("sandboxd with no address was opened")
	}
	if _, err := connect(sandbox.None, "", "key"); err == nil {
		t.Error("no provider was opened")
	}
}

// A secret store that cannot write has nowhere to keep a room's token, so
// there is no keeper and command servers are refused as with no sandbox; one
// that can has a keeper, and the discoverer runs command servers.
func TestCommandServersRunOnlyWhereTheSecretStoreWrites(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	dbStore := &store.Store{}

	if newKeeper(cfg, quietLogger(), dbStore, readOnly{}) != nil {
		t.Error("a keeper was built over a store that cannot write")
	}
	if newKeeper(cfg, quietLogger(), nil, static.New()) != nil {
		t.Error("a keeper was built with no database")
	}
	if newKeeper(cfg, quietLogger(), dbStore, static.New()) == nil {
		t.Error("no keeper over a store that writes")
	}

	row := db.McpServer{Name: "mail", Command: []string{"npx", "mail"}, Env: []byte(`{}`)}
	refusing := newDiscoverer(cfg, readOnly{}, nil)
	runCommandsInRooms(refusing, cfg, quietLogger(), dbStore, readOnly{})
	if _, _, err := refusing.Reach(t.Context(), row); !errors.Is(err, discovery.ErrCommandServer) {
		t.Errorf("Reach = %v, want ErrCommandServer without a keeper", err)
	}
}

// Workspaces share the command servers' keeper: where there is none, there
// are none, and where there is one, the worker's builder is given them.
func TestWorkspacesShareTheKeeperOrThereAreNone(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	if ws := newWorkspaces(runCommandsInRooms(newDiscoverer(cfg, readOnly{}, nil), cfg, quietLogger(), &store.Store{}, readOnly{}), cfg); ws != nil {
		t.Errorf("workspaces %+v with no keeper", ws)
	}
	k := runCommandsInRooms(newDiscoverer(cfg, static.New(), nil), cfg, quietLogger(), &store.Store{}, static.New())
	ws := newWorkspaces(k, cfg)
	if ws == nil || ws.Rooms == nil || ws.Client == nil {
		t.Fatalf("workspaces = %+v over a keeper", ws)
	}
}

type readOnly struct{}

func (readOnly) Get(_ context.Context, _, _ string) (string, error) { return "", secrets.ErrNotFound }

// The sweep deletes rooms, ahead of the keys they were made with.
func TestTheSweepDeletesRoomsBeforeKeys(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ObjectsBackend: config.ObjectsBackendMemory, SandboxIdle: time.Minute}
	effects, err := newEffects(t.Context(), cfg, quietLogger(), &store.Store{}, static.New())
	if err != nil {
		t.Fatalf("newEffects: %v", err)
	}

	var names []string
	for _, e := range effects {
		names = append(names, e.Name())
	}
	if len(names) != 3 || names[0] != "rooms" || names[1] != "secrets" || names[2] != "objects" {
		t.Errorf("effects = %v, want rooms, secrets, objects", names)
	}
}
