package objects

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("object not found")

// A Store reads objects: whole, bounded by the backend, or streamed. A
// streamed object is the caller's to close.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// A Writer writes objects. PutStream writes one from r as r gives it,
// without holding it whole, and leaves it whole or not at all: a reader that
// fails leaves no object.
type Writer interface {
	Put(ctx context.Context, key string, body []byte) error
	PutStream(ctx context.Context, key string, r io.Reader) error
	Delete(ctx context.Context, key string) error
}

func TeamPrefix(teamID uuid.UUID) string {
	return "teams/" + teamID.String() + "/"
}

func InTeam(key string, teamID uuid.UUID) bool {
	if strings.Contains(key, "..") {
		return false
	}
	prefix := TeamPrefix(teamID)
	return len(key) > len(prefix) && strings.HasPrefix(key, prefix)
}
