package memory_test

import (
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/memory"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// The reference implementation: the store every other one has to agree with,
// so it runs the suite before any driver exists to disagree.
func TestTheMemoryStoreKeepsTheContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) agent.Store { return memory.New() })
}

// Two stores are two conversations. Nothing is shared through a package-level
// map, which a test that only ever opens one store would never notice.
func TestTwoStoresDoNotShareAnything(t *testing.T) {
	first, second := memory.New(), memory.New()

	if err := first.AddSteer(t.Context(), "s1", "for the first store"); err != nil {
		t.Fatalf("AddSteer() = %v", err)
	}
	if err := first.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	steers, err := second.TakeSteers(t.Context(), "s1")
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	msgs, err := second.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(steers) != 0 || len(msgs) != 0 {
		t.Errorf("a second store sees %v and %d messages from the first", steers, len(msgs))
	}
}
