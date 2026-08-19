package secrets

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrNotFound    = errors.New("secret not found")
	ErrUnavailable = errors.New("secret store unavailable")
)

type Kind string

const (
	KindChannel Kind = "channels"
)

var AllKinds = []Kind{
	KindChannel,
}

type Store interface {
	Get(ctx context.Context, path, key string) (string, error)
}

type Writer interface {
	Put(ctx context.Context, path, key, value string) error
	Delete(ctx context.Context, path string) error
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
