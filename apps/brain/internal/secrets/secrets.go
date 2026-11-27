package secrets

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrNotFound    = errors.New("secret not found")
	ErrUnavailable = errors.New("secret store unavailable")
	ErrExists      = errors.New("secret already exists")
)

type Kind string

const (
	KindChannel        Kind = "channels"
	KindAttachments    Kind = "attachments"
	KindMCP            Kind = "mcp"
	KindSearch         Kind = "search"
	KindMCPOAuth       Kind = "mcp-oauth"
	KindMCPOAuthClient Kind = "mcp-oauth-client"
	KindSandbox        Kind = "sandbox"
	KindBrowser        Kind = "browser"
	KindWatcher        Kind = "watchers"
)

var AllKinds = []Kind{
	KindChannel,
	KindAttachments,
	KindMCP,
	KindSearch,
	KindMCPOAuth,
	KindMCPOAuthClient,
	KindSandbox,
	KindBrowser,
	KindWatcher,
}

type Store interface {
	Get(ctx context.Context, path, key string) (string, error)
}

type Writer interface {
	// Put writes one key. A path is one secret, written whole: in OpenBao a
	// write replaces every other key at the path, so a path with several
	// keys is written with PutAll, never with one Put per key.
	Put(ctx context.Context, path, key, value string) error
	// PutAll writes several keys as one secret: afterwards the path holds
	// exactly these keys, all of them or, on failure, none of them new.
	PutAll(ctx context.Context, path string, values map[string]string) error
	Delete(ctx context.Context, path string) error
}

type Creator interface {
	Create(ctx context.Context, path, key, value string) error
}

type Prober interface {
	Ping(ctx context.Context) error
}

func Path(teamID uuid.UUID, kind Kind, id uuid.UUID) string {
	return TeamPath(teamID, kind) + "/" + id.String()
}

func TeamPath(teamID uuid.UUID, kind Kind) string {
	return "teams/" + teamID.String() + "/" + string(kind)
}

// RoomPath is where a sandbox room's own bearer token is kept, under its
// row's id: teams/<team>/sandbox/rooms/<id>. It is beneath the sandbox key's
// kind but is not a Kind, which is a single segment, and an id there is never
// a key's.
func RoomPath(teamID, roomID uuid.UUID) string {
	return TeamPath(teamID, KindSandbox) + "/rooms/" + roomID.String()
}
