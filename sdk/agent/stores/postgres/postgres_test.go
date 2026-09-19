package postgres_test

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// AGENT_TEST_POSTGRES_DSN rather than an address written down here: CI and a
// laptop disagree about where Postgres is, and a test that assumes one fails
// on the other for a reason that has nothing to do with the code.
const dsnVar = "AGENT_TEST_POSTGRES_DSN"

func TestThePostgresStoreKeepsTheContract(t *testing.T) {
	pool := connect(t)

	storetest.Run(t, func(t *testing.T) agent.Store {
		// The contract reuses session ids across subtests, so each one starts
		// from empty.
		if _, err := pool.Exec(t.Context(), `TRUNCATE agent_messages, agent_steers`); err != nil {
			t.Fatalf("clearing the tables: %v", err)
		}
		return postgres.New(pool)
	})
}

// The brain applies this through the migration system it already has, under
// the advisory lock it already takes. If the constant stops being valid DDL
// that would fail at deploy time, so it fails here instead.
func TestTheSchemaAppliesAndCanBeAppliedAgain(t *testing.T) {
	pool := connect(t)

	for pass := range 2 {
		if _, err := pool.Exec(t.Context(), postgres.Schema); err != nil {
			t.Fatalf("applying Schema, pass %d: %v — it has to be safe to run twice, because\n"+
				"a migration that half-applied will be run again", pass+1, err)
		}
	}
}

// Two stores over one pool are two replicas over one database, which is the
// case this store exists for and the one a single-process test never reaches.
func TestTwoStoresOverOneDatabaseSplitTheSteers(t *testing.T) {
	pool := connect(t)
	clear(t, pool)

	first, second := postgres.New(pool), postgres.New(pool)
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

// A transcript written by one store and read by another is resume: the replica
// that reads it never ran the turn that produced it.
func TestAnotherStoreReadsWhatThisOneWrote(t *testing.T) {
	pool := connect(t)
	clear(t, pool)

	saved := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "why"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search"}}},
	}
	if err := postgres.New(pool).Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	back, err := postgres.New(pool).Messages(t.Context(), "s1")
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
// run a transcript that is silently short. Closing the pool makes all four fail
// for real rather than through a mock that proves a mock was called.
func TestEveryCallReportsAClosedPool(t *testing.T) {
	dsn := os.Getenv(dsnVar)
	if dsn == "" {
		t.Skipf("set %s to run the Postgres store against a real database", dsnVar)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	pool.Close()

	store := postgres.New(pool)
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

// A ToolCall's Arguments are raw JSON the model produced, so a malformed one
// reaches the store rather than being caught earlier.
func TestAMessageThatCannotBeEncodedIsReported(t *testing.T) {
	pool := connect(t)
	clear(t, pool)
	store := postgres.New(pool)

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

func connect(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv(dsnVar)
	if dsn == "" {
		t.Skipf("set %s to run the Postgres store against a real database", dsnVar)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(t.Context(), postgres.Schema); err != nil {
		t.Fatalf("applying Schema: %v", err)
	}
	return pool
}

func clear(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	if _, err := pool.Exec(t.Context(), `TRUNCATE agent_messages, agent_steers`); err != nil {
		t.Fatalf("clearing the tables: %v", err)
	}
}

func mustName(t *testing.T, err error, session string) {
	t.Helper()

	if err == nil {
		t.Fatal("a closed pool was reported as success")
	}
	if !strings.Contains(err.Error(), session) {
		t.Errorf("the error does not say which session: %v", err)
	}
}
