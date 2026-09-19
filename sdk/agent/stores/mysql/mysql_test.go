package mysql_test

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/mysql"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// AGENT_TEST_MYSQL_DSN rather than an address written down here: CI and a
// laptop disagree about where MySQL is, and a test that assumes one fails on
// the other for a reason that has nothing to do with the code.
const dsnVar = "AGENT_TEST_MYSQL_DSN"

func TestTheMySQLStoreKeepsTheContract(t *testing.T) {
	db := connect(t)

	storetest.Run(t, func(t *testing.T) agent.Store {
		// The contract reuses session ids across subtests, so each one starts
		// from empty.
		clear(t, db)
		return mysql.New(db)
	})
}

// The caller applies these through whatever migration system it has. MySQL
// will not take several statements in one Exec unless the connection asked
// for it, so Schema is a slice rather than one string — and a library that
// looped over it itself would be migrating a shared database on somebody's
// behalf, which is the thing the Postgres store refuses to do.
func TestTheSchemaAppliesAndCanBeAppliedAgain(t *testing.T) {
	db := connect(t)

	for pass := range 2 {
		for i, stmt := range mysql.Schema {
			if _, err := db.ExecContext(t.Context(), stmt); err != nil {
				t.Fatalf("applying Schema[%d], pass %d: %v — it has to be safe to run twice,\n"+
					"because a migration that half-applied will be run again", i, pass+1, err)
			}
		}
	}
}

// MySQL has no DELETE ... RETURNING, so TakeSteers is a SELECT and a DELETE in
// one transaction. That is the seam where two replicas could both take the
// same steer, and this is the test that says they do not.
func TestTwoStoresOverOneDatabaseSplitTheSteers(t *testing.T) {
	db := connect(t)
	clear(t, db)

	first, second := mysql.New(db), mysql.New(db)
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
		t.Errorf("two stores over one database took %d copies of one steer, want 1", total)
	}
}

// MySQL's JSON column reformats what it is given — it reorders keys, respaces,
// and drops a duplicate key without a word. A model's tool arguments are raw
// JSON it produced, so the column has to be LONGTEXT and this is the test that
// catches somebody "improving" it later.
func TestToolArgumentsComeBackByteForByte(t *testing.T) {
	db := connect(t)
	clear(t, db)
	store := mysql.New(db)

	const args = `{"z":1,"query":"vault","z":3}`
	saved := []agent.Message{{
		Role:      agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search", Arguments: []byte(args)}},
	}}
	if err := store.Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	back, err := store.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if got := string(back[0].ToolCalls[0].Arguments); got != args {
		t.Errorf("the arguments came back as\n  %s\nwant\n  %s\nA column that parses JSON\n"+
			"normalises it, and a duplicate key disappears without a word", got, args)
	}
}

// A transcript written by one store and read by another is resume: the replica
// that reads it never ran the turn that produced it.
func TestAnotherStoreReadsWhatThisOneWrote(t *testing.T) {
	db := connect(t)
	clear(t, db)

	saved := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "why"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search"}}},
	}
	if err := mysql.New(db).Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	back, err := mysql.New(db).Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("a second store read %d messages, want 2", len(back))
	}
	if back[0].Text() != "why" {
		t.Errorf("the first message came back as %q", back[0].Text())
	}
	if len(back[1].ToolCalls) != 1 || back[1].ToolCalls[0].Name != "search" {
		t.Errorf("the tool call did not survive: %+v", back[1].ToolCalls)
	}
}

// Every statement can fail, and a store that swallows a database error hands a
// run a transcript that is silently short.
func TestEveryCallReportsAClosedDatabase(t *testing.T) {
	dsn := os.Getenv(dsnVar)
	if dsn == "" {
		t.Skipf("set %s to run the MySQL store against a real database", dsnVar)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	store := mysql.New(db)
	t.Run("Save", func(t *testing.T) {
		mustName(t, store.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}}), "s1")
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

func TestAMessageThatCannotBeEncodedIsReported(t *testing.T) {
	db := connect(t)
	clear(t, db)
	store := mysql.New(db)

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "fine"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{
			ID: "call_1", Name: "search", Arguments: []byte(`{not json`),
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

func connect(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(dsnVar)
	if dsn == "" {
		t.Skipf("set %s to run the MySQL store against a real database", dsnVar)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})

	for i, stmt := range mysql.Schema {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("applying Schema[%d]: %v", i, err)
		}
	}
	return db
}

func clear(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, table := range []string{"agent_messages", "agent_steers"} {
		if _, err := db.ExecContext(t.Context(), "TRUNCATE TABLE "+table); err != nil { //nolint:gosec // a literal from the line above
			t.Fatalf("clearing %s: %v", table, err)
		}
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
