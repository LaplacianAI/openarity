package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // to write a row this store would never write

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/sqlite"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

func TestTheSQLiteStoreKeepsTheContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) agent.Store {
		return open(t, filepath.Join(t.TempDir(), "sessions.db"))
	})
}

// A file store is only worth having if what it wrote is there when the process
// that wrote it has gone. Everything else in the contract a map could do.
func TestATranscriptOutlivesTheStoreThatWroteIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	first := open(t, path)
	saved := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "still here"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search"}}},
	}
	if err := first.Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second := open(t, path)
	back, err := second.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("reopening the file found %d messages, want 2", len(back))
	}
	if back[0].Text() != "still here" {
		t.Errorf("the first message came back as %q", back[0].Text())
	}
	if len(back[1].ToolCalls) != 1 || back[1].ToolCalls[0].Name != "search" {
		t.Errorf("the tool call did not survive the file: %+v", back[1].ToolCalls)
	}
}

// A steer left before a crash is the case this store exists for: the replica
// that took it never ran, and another one has to find it.
func TestASteerOutlivesTheStoreThatTookIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	first := open(t, path)
	if err := first.AddSteer(t.Context(), "s1", "check the lease"); err != nil {
		t.Fatalf("AddSteer() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second := open(t, path)
	got, err := second.TakeSteers(t.Context(), "s1")
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	if len(got) != 1 || got[0] != "check the lease" {
		t.Errorf("TakeSteers() = %v after reopening, want the steer left before the close", got)
	}
}

// Two stores over one file are two replicas over one disk. The steer must go
// to exactly one of them, which is the whole reason TakeSteers deletes inside
// a transaction rather than reading and then deleting.
func TestTwoStoresOverOneFileSplitTheSteers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	first, second := open(t, path), open(t, path)

	if err := first.AddSteer(t.Context(), "s1", "once"); err != nil {
		t.Fatalf("AddSteer() = %v", err)
	}

	fromFirst, err := first.TakeSteers(t.Context(), "s1")
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	fromSecond, err := second.TakeSteers(t.Context(), "s1")
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}

	if total := len(fromFirst) + len(fromSecond); total != 1 {
		t.Errorf("two stores over one file took %d copies of one steer, want 1", total)
	}
}

func TestAPathThatCannotBeOpenedIsAnError(t *testing.T) {
	// A directory where the file should be: openable as a handle, unusable as
	// a database, so this catches a store that defers its first statement.
	if _, err := sqlite.Open(t.TempDir()); err == nil {
		t.Error("a directory was accepted as a database file")
	}
}

// Returns the concrete type rather than agent.Store so a test can Close it.
// sql.DB.Close is idempotent, so a test that closes early and the cleanup
// below do not fight.
func open(t *testing.T, path string) *sqlite.Store {
	t.Helper()

	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open(%s) = %v", path, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	return store
}

// Every statement this store makes can fail, and a store that swallows a
// database error hands a run a transcript that is silently short. Closing the
// database is the cheapest way to make all four fail for real rather than
// through a mock that proves only that a mock was called.
func TestEveryCallReportsAClosedDatabase(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	t.Run("Save", func(t *testing.T) {
		err := store.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}})
		mustName(t, err, "s1")
	})
	t.Run("Messages", func(t *testing.T) {
		_, err := store.Messages(t.Context(), "s1")
		mustName(t, err, "s1")
	})
	t.Run("AddSteer", func(t *testing.T) {
		mustName(t, store.AddSteer(t.Context(), "s1", "anything"), "s1")
	})
	t.Run("TakeSteers", func(t *testing.T) {
		_, err := store.TakeSteers(t.Context(), "s1")
		mustName(t, err, "s1")
	})
}

// A ToolCall's Arguments are raw JSON the model produced, so a malformed one
// reaches the store rather than being caught earlier. Saving it must fail and
// say which message, not write half a transcript.
func TestAMessageThatCannotBeEncodedIsReported(t *testing.T) {
	store := open(t, filepath.Join(t.TempDir(), "sessions.db"))

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "fine"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{
			ID: "call_1", Name: "search", Arguments: json.RawMessage(`{not json`),
		}}},
	}

	err := store.Save(t.Context(), "s1", msgs)
	if err == nil {
		t.Fatal("a message that cannot be encoded was saved")
	}
	if !strings.Contains(err.Error(), "message 1") || !strings.Contains(err.Error(), "s1") {
		t.Errorf("the error does not say which message of which session: %v", err)
	}

	back, err := store.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 0 {
		t.Errorf("the failed save left %d messages behind; the whole transcript is written\n"+
			"in one transaction or not at all", len(back))
	}
}

// A row this store did not write — an older format, a hand-edited file, a
// half-finished migration. Reporting it beats returning a transcript with a
// silent hole in the middle.
func TestARowThatIsNotAMessageIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store := open(t, path)
	if err := store.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	corrupt(t, path)

	if _, err := store.Messages(t.Context(), "s1"); err == nil {
		t.Error("a row that is not a message came back as one")
	} else if !strings.Contains(err.Error(), "s1") {
		t.Errorf("the error does not say which session: %v", err)
	}
}

func corrupt(t *testing.T, path string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening %s directly: %v", path, err)
	}
	defer db.Close() //nolint:errcheck // the test fails on the assertion, not on this

	if _, err := db.ExecContext(t.Context(),
		`UPDATE agent_messages SET body = 'not json' WHERE session = 's1'`); err != nil {
		t.Fatalf("corrupting the row: %v", err)
	}
}

func mustName(t *testing.T, err error, session string) {
	t.Helper()

	if err == nil {
		t.Fatal("a closed database was reported as success")
	}
	if !strings.Contains(err.Error(), session) {
		t.Errorf("the error does not say which session: %v", err)
	}
}
