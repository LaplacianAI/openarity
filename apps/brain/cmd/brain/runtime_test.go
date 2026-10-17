package main

import (
	"context"
	"fmt"
	"hash/crc32"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func TestTheRunsRoutesAreMounted(t *testing.T) {
	t.Parallel()

	routes := servedRoutes(t, config.EnvironmentProduction)
	for _, want := range []string{
		"POST /teams/{id}/runs",
		"GET /teams/{id}/runs/{runID}",
		"GET /teams/{id}/runs/{runID}/events",
		"POST /teams/{id}/runs/{runID}/cancel",
	} {
		if !slices.Contains(routes, want) {
			t.Errorf("%s is not served", want)
		}
	}
}

// freshDatabase is a database of the test's own. DBOS keeps its tables in a
// schema called dbos whatever the search_path says, so a test about who
// creates them cannot use a schema of its own the way schemaDSN does: it
// would be looking at the tables every other test in the database made.
func freshDatabase(t *testing.T) string {
	t.Helper()

	base := liveDSN(t)
	name := fmt.Sprintf("brain_cmd_%08x", crc32.ChecksumIEEE([]byte(t.Name())))
	drop := "DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"

	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(t.Context())) }()

	if _, err := admin.Exec(t.Context(), drop); err != nil {
		t.Fatalf("clear database: %v", err)
	}
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()

		if _, err := conn.Exec(ctx, drop); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func hasDBOSTables(t *testing.T, dsn string) bool {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()

	var exists bool
	if err := conn.QueryRow(t.Context(),
		"SELECT to_regclass('dbos.workflow_status') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("look for DBOS's tables: %v", err)
	}
	return exists
}

// runtimeEnv is a fresh database, not yet migrated, with nothing in the
// environment that would make the worker reach past it.
func runtimeEnv(t *testing.T) (dsn, apiBind string) {
	t.Helper()

	dsn, apiBind = freshDatabase(t), freeAddr(t)
	baseServeEnv(t, dsn, apiBind)
	t.Setenv("OPENARITY_ENVIRONMENT", "development")
	t.Setenv("OPENARITY_SECRETS_BACKEND", "static")
	t.Setenv("OPENARITY_OBJECTS_BACKEND", "memory")
	t.Setenv("OPENARITY_EMBEDDING_MODEL", "")
	return dsn, apiBind
}

func stopped(t *testing.T, role string, done <-chan error, log *syncBuffer) {
	t.Helper()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("%s after a clean shutdown = %v\n%s", role, err, log.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never returned after the context was cancelled", role)
	}
}

// DBOS migrates its own tables and takes no lock to do it, so two processes
// starting together on a database it has never seen both run the CREATEs and
// one dies on a duplicate type. The migrate Job runs before anything else, so
// it creates them, and serve and the workers only ever find them current.
func TestMigrateUpCreatesTheDurableRuntimesTables(t *testing.T) {
	dsn, _ := runtimeEnv(t)
	if hasDBOSTables(t, dsn) {
		t.Fatal("the fresh database already has DBOS's tables, so this test proves nothing")
	}

	mustMigrate(t)

	if !hasDBOSTables(t, dsn) {
		t.Error("migrate up finished and DBOS's tables are not there")
	}
}

// A deploy rolls the API and the workers at once, after the migrate Job.
// Neither may fail because the other started first.
func TestServeAndAWorkerStartingTogetherDoNotFightOverDBOSsTables(t *testing.T) {
	_, apiBind := runtimeEnv(t)
	mustMigrate(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var serveLog, workerLog syncBuffer
	served, worked := make(chan error, 1), make(chan error, 1)
	go func() { served <- run(ctx, &serveLog, nil) }()
	go func() { worked <- run(ctx, &workerLog, []string{"worker"}) }()

	waitForHealthz(t, apiBind)

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(workerLog.String(), "Worker running") {
		select {
		case err := <-worked:
			t.Fatalf("the worker stopped while starting beside serve: %v\n%s", err, workerLog.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker never said it was running:\n%s", workerLog.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	stopped(t, "serve", served, &serveLog)
	stopped(t, "worker", worked, &workerLog)
}
