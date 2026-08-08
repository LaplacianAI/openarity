package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// poolConfig builds a Store and hands back the config its pool ended up with.
func poolConfig(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()

	s, err := New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	return s.pool.Config()
}

// Every field must be set. A dropped line here is invisible — the pool still
// works, on pgx's defaults, which is exactly what these constants exist to
// avoid. Same failure mode gosec G112 caught on the http.Servers.
func TestApplyPoolDefaultsSetsEveryField(t *testing.T) {
	t.Parallel()

	cfg := poolConfig(t, deadDSN(t))

	if cfg.MaxConns != maxConns {
		t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, maxConns)
	}
	if cfg.MinIdleConns != minIdleConns {
		t.Errorf("MinIdleConns = %d, want %d", cfg.MinIdleConns, minIdleConns)
	}
	if cfg.MaxConnLifetime != maxConnLifetime {
		t.Errorf("MaxConnLifetime = %v, want %v", cfg.MaxConnLifetime, maxConnLifetime)
	}
	if cfg.MaxConnLifetimeJitter != maxConnLifetimeJitter {
		t.Errorf("MaxConnLifetimeJitter = %v, want %v", cfg.MaxConnLifetimeJitter, maxConnLifetimeJitter)
	}
	if cfg.MaxConnIdleTime != maxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, maxConnIdleTime)
	}
	if cfg.ConnConfig.ConnectTimeout != connectTimeout {
		t.Errorf("ConnectTimeout = %v, want %v", cfg.ConnConfig.ConnectTimeout, connectTimeout)
	}
}

// pgx defaults ConnectTimeout to zero, which means no timeout: a dial to an
// unreachable host blocks until the OS gives up, around two minutes on Linux.
// The startup Ping inherits that, so a wrong DSN hangs the pod instead of
// crash-looping it.
func TestConnectTimeoutIsNotZero(t *testing.T) {
	t.Parallel()

	if connectTimeout == 0 {
		t.Fatal("connectTimeout is zero, which pgx reads as no timeout at all")
	}
	if cfg := poolConfig(t, deadDSN(t)); cfg.ConnConfig.ConnectTimeout == 0 {
		t.Error("the pool was built with no connect timeout")
	}
}

// pgx derives MaxConns from NumCPU when the DSN says nothing, so the same
// image would open 8 connections on one node and 64 on another. Replicas
// times that has to stay under Postgres max_connections.
func TestMaxConnsDoesNotVaryWithTheMachine(t *testing.T) {
	t.Parallel()

	cfg, err := pgxpool.ParseConfig(deadDSN(t))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	applyPoolDefaults(cfg)

	if cfg.MaxConns != maxConns {
		t.Errorf("MaxConns = %d, want the fixed %d", cfg.MaxConns, maxConns)
	}
}

// One place decides the pool size. Pool settings can ride in the DSN, and
// silently honouring them would mean the value differs per environment with
// nothing in the code to grep for.
func TestDSNPoolSettingsDoNotWin(t *testing.T) {
	t.Parallel()

	dsn := deadDSN(t) + "&pool_max_conns=99&pool_max_conn_lifetime=9h"
	cfg := poolConfig(t, dsn)

	if cfg.MaxConns != maxConns {
		t.Errorf("MaxConns = %d, the DSN overrode the code default", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime != maxConnLifetime {
		t.Errorf("MaxConnLifetime = %v, the DSN overrode the code default", cfg.MaxConnLifetime)
	}
}

// A lifetime with no jitter expires every connection opened at startup at the
// same instant, so the whole pool reconnects at once — across replicas, a
// synchronised stampede.
func TestLifetimeHasJitter(t *testing.T) {
	t.Parallel()

	if maxConnLifetimeJitter <= 0 {
		t.Fatal("no jitter: every connection in the pool expires simultaneously")
	}
	if maxConnLifetimeJitter >= maxConnLifetime {
		t.Errorf("jitter %v is not smaller than the lifetime %v", maxConnLifetimeJitter, maxConnLifetime)
	}
}

// Idle connections must be released before they outlive their usefulness, and
// recycling must happen well inside anything a proxy or firewall would cut.
func TestLifetimesAreOrdered(t *testing.T) {
	t.Parallel()

	if maxConnIdleTime >= maxConnLifetime {
		t.Errorf("idle timeout %v is not shorter than the lifetime %v", maxConnIdleTime, maxConnLifetime)
	}
	if connectTimeout >= time.Minute {
		t.Errorf("connect timeout %v is too long to fail fast", connectTimeout)
	}
}

// MinIdleConns keeps connections warm without pinning them open the way
// MinConns does. Zero means the first request after an idle period pays for a
// handshake.
func TestMinIdleConnsIsWarmButNotPinned(t *testing.T) {
	t.Parallel()

	cfg := poolConfig(t, deadDSN(t))

	if cfg.MinIdleConns <= 0 {
		t.Error("no warm connections: the first request after idle pays a handshake")
	}
	if cfg.MinIdleConns > cfg.MaxConns {
		t.Errorf("MinIdleConns %d exceeds MaxConns %d", cfg.MinIdleConns, cfg.MaxConns)
	}
	if cfg.MinConns != 0 {
		t.Errorf("MinConns = %d, want 0 — use MinIdleConns, which does not pin", cfg.MinConns)
	}
}
