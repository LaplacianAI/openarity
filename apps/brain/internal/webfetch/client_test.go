package webfetch

import (
	"net/netip"
	"testing"
	"time"
)

// A page that answers slowly costs one tool call fifteen seconds, never a run.
func TestTheClientGivesUpAfter15Seconds(t *testing.T) {
	t.Parallel()

	if c := NewClient(func(netip.Addr) bool { return true }); c.Timeout != 15*time.Second {
		t.Errorf("timeout = %s, want 15s", c.Timeout)
	}
}
