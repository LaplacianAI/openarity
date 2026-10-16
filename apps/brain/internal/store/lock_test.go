package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
)

// lockStore is a connection pool and nothing else: an advisory lock needs no
// schema. Two of them are two workers, each with a pool of its own.
func lockStore(t *testing.T) *Store {
	t.Helper()

	s, err := New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// Advisory locks are per database, not per schema, so every test takes a key
// of its own and none can collide with another running against this server.
func freshKey() int64 { return rand.Int64N(1 << 62) }

func mustHold(t *testing.T, s *Store, key int64) {
	t.Helper()

	held, err := s.WithAdvisoryLock(t.Context(), key, func(context.Context) error { return nil })
	if err != nil || !held {
		t.Fatalf("the lock could not be taken: held %v, err %v", held, err)
	}
}

// While one worker holds the lock another is turned away, without an error
// and without running anything: that is "skip this tick", not a failure.
func TestASecondWorkerIsTurnedAwayWhileTheFirstHoldsTheLock(t *testing.T) {
	first, second := lockStore(t), lockStore(t)
	key := freshKey()

	ran := false
	held, err := first.WithAdvisoryLock(t.Context(), key, func(context.Context) error {
		got, err := second.WithAdvisoryLock(t.Context(), key, func(context.Context) error {
			ran = true
			return nil
		})
		if err != nil || got {
			t.Errorf("the second worker: held %v, err %v; want turned away quietly", got, err)
		}
		return nil
	})
	if err != nil || !held {
		t.Fatalf("the first worker: held %v, err %v", held, err)
	}
	if ran {
		t.Error("the second worker's function ran while the first held the lock")
	}

	// And once the first is done, the second gets it.
	mustHold(t, second, key)
}

// The same pool is turned away too: the lock belongs to one connection, and
// a second call from this process is on another.
func TestTheLockIsNotReentrantAcrossCalls(t *testing.T) {
	s := lockStore(t)
	key := freshKey()

	_, err := s.WithAdvisoryLock(t.Context(), key, func(context.Context) error {
		got, err := s.WithAdvisoryLock(t.Context(), key, func(context.Context) error { return nil })
		if err != nil || got {
			t.Errorf("a second call from the same store: held %v, err %v", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDifferentKeysDoNotBlockEachOther(t *testing.T) {
	s := lockStore(t)

	_, err := s.WithAdvisoryLock(t.Context(), freshKey(), func(context.Context) error {
		mustHold(t, s, freshKey())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// However the function ends, the lock is given back: a drain that failed
// must not stop every drain after it.
func TestTheLockIsReleasedHoweverTheFunctionEnds(t *testing.T) {
	boom := errors.New("the drain failed")

	for name, run := range map[string]func(*Store, int64){
		"it returns an error": func(s *Store, key int64) {
			held, err := s.WithAdvisoryLock(t.Context(), key, func(context.Context) error { return boom })
			if !held || !errors.Is(err, boom) {
				t.Errorf("held %v, err %v; want the function's own error", held, err)
			}
		},
		"it panics": func(s *Store, key int64) {
			defer func() { _ = recover() }()
			_, _ = s.WithAdvisoryLock(t.Context(), key, func(context.Context) error { panic("the drain panicked") })
		},
		"its context is cancelled while it runs": func(s *Store, key int64) {
			ctx, cancel := context.WithCancel(t.Context())
			held, err := s.WithAdvisoryLock(ctx, key, func(ctx context.Context) error {
				cancel()
				return ctx.Err()
			})
			if !held || !errors.Is(err, context.Canceled) {
				t.Errorf("held %v, err %v", held, err)
			}
			// A cancelled drain still releases cleanly: the release runs on a
			// context of its own, or shutting a worker down would report a
			// failed release every time and cost a connection.
			if err != nil && strings.Contains(err.Error(), "failed to release") {
				t.Errorf("a cancelled function could not release its lock: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, other := lockStore(t), lockStore(t)
			key := freshKey()
			run(s, key)
			mustHold(t, other, key)
		})
	}
}

func TestACancelledContextTakesNoLockAndRunsNothing(t *testing.T) {
	s := lockStore(t)
	key := freshKey()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ran := false
	held, err := s.WithAdvisoryLock(ctx, key, func(context.Context) error { ran = true; return nil })
	if held || err == nil || ran {
		t.Errorf("held %v, err %v, ran %v; want an error and nothing done", held, err, ran)
	}
	mustHold(t, lockStore(t), key)
}

// The pinned connection goes back to the pool each time. More calls than the
// pool has connections would hang if it did not.
func TestTheConnectionIsReturnedToThePool(t *testing.T) {
	s := lockStore(t)
	key := freshKey()

	for range maxConns * 3 {
		mustHold(t, s, key)
	}
	// Including when the lock was not available.
	blocker := lockStore(t)
	_, err := blocker.WithAdvisoryLock(t.Context(), key, func(context.Context) error {
		for range maxConns * 3 {
			if held, err := s.WithAdvisoryLock(t.Context(), key, func(context.Context) error { return nil }); held || err != nil {
				t.Fatalf("held %v, err %v", held, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// If the lock cannot be released — here the connection holding it is killed
// from outside, as a failover would — the caller is told, the connection is
// not handed back still holding it, and the next drain can take the lock.
func TestALockThatCannotBeReleasedIsReportedAndNotLeftHeld(t *testing.T) {
	s, other := lockStore(t), lockStore(t)
	key := freshKey()

	held, err := s.WithAdvisoryLock(t.Context(), key, func(ctx context.Context) error {
		var killed bool
		err := other.pool.QueryRow(ctx, `
			SELECT pg_terminate_backend(pid) FROM pg_locks
			WHERE locktype = 'advisory' AND granted
			  AND ((classid::bigint << 32) | objid::bigint) = $1`, key).Scan(&killed)
		if err != nil || !killed {
			t.Fatalf("could not kill the connection holding the lock: %v %v", killed, err)
		}
		return nil
	})
	if !held || err == nil || !strings.Contains(err.Error(), "failed to release the advisory lock") {
		t.Errorf("held %v, err %v; want the failed release reported", held, err)
	}

	// The lock died with its session, and the pool still works.
	mustHold(t, other, key)
	for range maxConns + 2 {
		mustHold(t, s, freshKey())
	}
}

// The function's own error is not lost when the release fails as well.
func TestAFailedReleaseKeepsTheFunctionsError(t *testing.T) {
	s, other := lockStore(t), lockStore(t)
	key := freshKey()
	boom := errors.New("the drain failed")

	_, err := s.WithAdvisoryLock(t.Context(), key, func(ctx context.Context) error {
		var killed bool
		if err := other.pool.QueryRow(ctx, `
			SELECT pg_terminate_backend(pid) FROM pg_locks
			WHERE locktype = 'advisory' AND granted
			  AND ((classid::bigint << 32) | objid::bigint) = $1`, key).Scan(&killed); err != nil {
			t.Fatal(err)
		}
		return boom
	})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "failed to release") {
		t.Errorf("err = %v, want both the function's error and the failed release", err)
	}
}
