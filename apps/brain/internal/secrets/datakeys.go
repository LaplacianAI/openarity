package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const dataKeyField = "data_key"

type DataKeys struct {
	store   Store
	creator Creator
	size    int
}

func NewDataKeys(store Store, size int) (*DataKeys, error) {
	if size <= 0 {
		return nil, fmt.Errorf("data key size is %d, want a positive number of bytes", size)
	}

	creator, ok := store.(Creator)
	if !ok {
		return nil, fmt.Errorf(
			"secret store %T cannot create a key without replacing one, so per-team "+
				"data keys cannot be generated safely", store)
	}
	return &DataKeys{store: store, creator: creator, size: size}, nil
}

func (d *DataKeys) TeamKey(ctx context.Context, teamID uuid.UUID) ([]byte, error) {
	path := TeamPath(teamID, KindAttachments)

	key, err := d.read(ctx, path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	fresh := make([]byte, d.size)
	rand.Read(fresh) //nolint:errcheck

	switch err := d.creator.Create(
		ctx, path, dataKeyField, base64.StdEncoding.EncodeToString(fresh),
	); {
	case err == nil:
		return fresh, nil
	case errors.Is(err, ErrExists):
		return d.read(ctx, path)
	default:
		return nil, err
	}
}

func (d *DataKeys) read(ctx context.Context, path string) ([]byte, error) {
	encoded, err := d.store.Get(ctx, path, dataKeyField)
	if err != nil {
		return nil, err
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not base64: %w", ErrUnavailable, path, err)
	}
	if len(key) != d.size {
		return nil, fmt.Errorf("%w: %s holds %d bytes, want %d",
			ErrUnavailable, path, len(key), d.size)
	}
	return key, nil
}
