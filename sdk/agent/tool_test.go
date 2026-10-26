package agent

import (
	"context"
	"testing"
)

func TestNoCallIDOutsideACall(t *testing.T) {
	t.Parallel()

	if got := ToolCallID(context.Background()); got != "" {
		t.Errorf("ToolCallID = %q outside a call, want empty", got)
	}
}
