package mongodb_test

import (
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/mongodb"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// AGENT_TEST_MONGO_URI rather than an address written down here: CI and a
// laptop disagree about where mongod is, and a test that assumes one fails on
// the other for a reason that has nothing to do with the code.
const uriVar = "AGENT_TEST_MONGO_URI"

func TestTheMongoStoreKeepsTheContract(t *testing.T) {
	db := connect(t)

	storetest.Run(t, func(t *testing.T) agent.Store {
		// The contract reuses session ids across subtests, so each one starts
		// from empty.
		clear(t, db)
		return mongodb.New(db)
	})
}

// A standalone mongod has no transactions, so this store keeps a session's
// whole transcript in one document and replaces it in one write. Single
// document atomicity is guaranteed on every deployment, which is what lets a
// failed save leave the previous transcript intact rather than a hole where
// one used to be.
func TestAFailedSaveLeavesTheLastGoodTranscript(t *testing.T) {
	db := connect(t)
	clear(t, db)
	store := mongodb.New(db)

	good := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "the good one"}},
	}}
	if err := store.Save(t.Context(), "s1", good); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	bad := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "fine"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{
			ID: "call_1", Name: "search", Arguments: []byte(`{not json`),
		}}},
	}
	err := store.Save(t.Context(), "s1", bad)
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
	if len(back) != 1 || back[0].Text() != "the good one" {
		t.Errorf("a failed save destroyed the transcript that was already there: %+v", back)
	}
}

// A model's tool arguments are raw JSON it produced. Letting the database
// parse them is how a duplicate key disappears without a word, so the store
// keeps the encoded bytes rather than a BSON document.
func TestToolArgumentsComeBackByteForByte(t *testing.T) {
	db := connect(t)
	clear(t, db)
	store := mongodb.New(db)

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
		t.Errorf("the arguments came back as\n  %s\nwant\n  %s\nBSON decides field order\n"+
			"and numeric types for you, which is why the bytes are stored as bytes", got, args)
	}
}

// Two stores over one database are two replicas. findOneAndDelete is atomic
// per document, which is what makes a steer go to exactly one of them without
// a transaction to lean on.
func TestTwoStoresOverOneDatabaseSplitTheSteers(t *testing.T) {
	db := connect(t)
	clear(t, db)

	first, second := mongodb.New(db), mongodb.New(db)
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
	db := connect(t)
	clear(t, db)

	saved := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "why"}}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search"}}},
	}
	if err := mongodb.New(db).Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	back, err := mongodb.New(db).Messages(t.Context(), "s1")
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

// Every call can fail, and a store that swallows the error hands a run a
// transcript that is silently short.
func TestEveryCallReportsADisconnectedClient(t *testing.T) {
	uri := os.Getenv(uriVar)
	if uri == "" {
		t.Skipf("set %s to run the Mongo store against a real mongod", uriVar)
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := client.Disconnect(t.Context()); err != nil {
		t.Fatalf("Disconnect() = %v", err)
	}

	store := mongodb.New(client.Database("agent_test"))
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

func connect(t *testing.T) *mongo.Database {
	t.Helper()

	uri := os.Getenv(uriVar)
	if uri == "" {
		t.Skipf("set %s to run the Mongo store against a real mongod", uriVar)
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(t.Context()); err != nil {
			t.Errorf("Disconnect() = %v", err)
		}
	})
	return client.Database("agent_test")
}

func clear(t *testing.T, db *mongo.Database) {
	t.Helper()

	for _, name := range []string{"agent_sessions", "agent_steers"} {
		if err := db.Collection(name).Drop(t.Context()); err != nil {
			t.Fatalf("clearing %s: %v", name, err)
		}
	}
}

func mustName(t *testing.T, err error, session string) {
	t.Helper()

	if err == nil {
		t.Fatal("a disconnected client was reported as success")
	}
	if !strings.Contains(err.Error(), session) {
		t.Errorf("the error does not say which session: %v", err)
	}
}
