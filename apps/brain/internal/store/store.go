package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Store struct {
	pool *pgxpool.Pool
	*db.Queries
	// firstUserBootstrap is a policy, not a fact about the database, so it is
	// set once at construction and never written again — Resolve reads it on
	// every authenticated request from every goroutine.
	//
	// It is one boolean rather than the two settings it comes from because the
	// store cannot see configuration: whether SUPER_ADMINS already names
	// somebody is a question only the caller can answer, and combining it here
	// would mean the store knowing what an environment variable is.
	firstUserBootstrap bool
}

type Option func(*Store)

// WithFirstUserBootstrap allows Resolve to grant super admin to a user
// arriving at an install that has none. The caller decides: this should be
// true only when the operator asked for it *and* no super admin was named in
// configuration, because a deployment that names its admins already has one.
func WithFirstUserBootstrap(enabled bool) Option {
	return func(s *Store) { s.firstUserBootstrap = enabled }
}

const (
	maxConns              = 10
	minIdleConns          = 2
	maxConnLifetime       = 30 * time.Minute
	maxConnLifetimeJitter = 5 * time.Minute
	maxConnIdleTime       = 5 * time.Minute
	connectTimeout        = 5 * time.Second
)

func applyPoolDefaults(cfg *pgxpool.Config) {
	cfg.MaxConns = maxConns
	cfg.MinIdleConns = minIdleConns
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnLifetimeJitter = maxConnLifetimeJitter
	cfg.MaxConnIdleTime = maxConnIdleTime
	cfg.ConnConfig.ConnectTimeout = connectTimeout
}

func New(ctx context.Context, dsn string, opts ...Option) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	applyPoolDefaults(cfg)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	s := &Store{pool: pool, Queries: db.New(pool)}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

func (s *Store) InTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(s.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Close() { s.pool.Close() }
