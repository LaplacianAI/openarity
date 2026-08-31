package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// closedStore is a Store whose pool is shut, so every call through it fails
// without needing a broken database. It is the only way to reach the error
// branches in migrateUp and migrateDown: a healthy database makes them succeed
// and a missing one stops run long before it gets here.
func closedStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()

	return s
}

// parse only ever produces serve or migrate, so this branch is unreachable
// through the command line — today. It exists so that adding a commandName
// without a case here fails loudly instead of silently doing nothing, and this
// test is what proves it fails loudly.
func TestExecuteRejectsAnUnknownCommand(t *testing.T) {
	t.Parallel()

	// A name that is not a command, and must stay that way. This used to say
	// "worker", which stopped being unknown the moment the role existed — and
	// the test then dispatched into the worker with a nil store and panicked
	// rather than failing with anything readable.
	err := execute(t.Context(), &config.Config{}, quietLogger(), nil, command{name: "nonsense"})
	if err == nil {
		t.Fatal("execute accepted an unknown command")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("execute error %q does not name the command", err)
	}
}

// Same argument one level down: parseMigrate only produces up or down.
func TestMigrateRejectsAnUnknownDirection(t *testing.T) {
	t.Parallel()

	err := migrate(t.Context(), quietLogger(), nil, direction("sideways"))
	if err == nil {
		t.Fatal("migrate accepted an unknown direction")
	}
	if !strings.Contains(err.Error(), "sideways") {
		t.Errorf("migrate error %q does not name the direction", err)
	}
}

// A failing migration must surface as an error and must not log a count, or a
// deploy Job reports success on a schema it never changed.
func TestMigrateUpReportsFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	err := migrate(t.Context(), logger, closedStore(t), directionUp)
	if err == nil {
		t.Fatal("migrate up succeeded against a closed pool")
	}
	if strings.Contains(buf.String(), "Applied migrations") {
		t.Errorf("migrate up logged success while failing: %q", buf.String())
	}
}

func TestMigrateDownReportsFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	err := migrate(t.Context(), logger, closedStore(t), directionDown)
	if err == nil {
		t.Fatal("migrate down succeeded against a closed pool")
	}
	if strings.Contains(buf.String(), "Rolled back") {
		t.Errorf("migrate down logged success while failing: %q", buf.String())
	}
}

// config.Validate only checks the scheme and host, so a DSN can be valid
// configuration and still be unparseable by pgx. run has to fail on that
// rather than carry a half-built pool forward.
func TestRunRejectsADSNConfigAccepts(t *testing.T) {
	stubEnv(t)
	t.Setenv("OPENARITY_POSTGRES_DSN", "postgres://user@127.0.0.1:5432/openarity?sslmode=bogus")

	var buf bytes.Buffer
	err := run(t.Context(), &buf, nil)
	if err == nil {
		t.Fatal("run accepted a DSN pgx cannot parse")
	}
	if !strings.Contains(err.Error(), "sslmode") {
		t.Errorf("run error %q does not explain what was wrong with the DSN", err)
	}
}
