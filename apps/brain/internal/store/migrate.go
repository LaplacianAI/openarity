package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const migrationLockID int64 = 1645966604

//go:embed migrations/*.sql
var migrationFS embed.FS

func (s *Store) provider() (*goose.Provider, error) {
	return s.providerFor(migrationFS, "migrations")
}

func (s *Store) providerFor(fsys fs.FS, dir string) (*goose.Provider, error) {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("failed to get migrations subfs: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(migrationLockID),
	)
	if err != nil {
		return nil, fmt.Errorf("migration locker: %w", err)
	}

	return goose.NewProvider(
		goose.DialectPostgres,
		stdlib.OpenDBFromPool(s.pool),
		sub,
		goose.WithSessionLocker(locker),
	)
}

func (s *Store) Migrate(ctx context.Context) (int, error) {
	provider, err := s.provider()
	if err != nil {
		return 0, err
	}

	applied, err := provider.Up(ctx)
	return len(applied), err
}

func (s *Store) Rollback(ctx context.Context) error {
	p, err := s.provider()
	if err != nil {
		return err
	}

	_, err = p.Down(ctx)
	return err
}
