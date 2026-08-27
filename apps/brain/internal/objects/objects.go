package objects

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("object not found")

type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

type Writer interface {
	Put(ctx context.Context, key string, body []byte) error
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
