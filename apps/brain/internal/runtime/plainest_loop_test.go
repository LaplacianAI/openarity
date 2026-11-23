package runtime

import (
	"slices"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
)

// The one loop an efficiency change may move an agent to is a loop the brain
// runs, so a rename in the SDK fails here rather than refusing every move.
func TestThePlainestLoopIsOneTheBrainRuns(t *testing.T) {
	if !slices.Contains(Loops(), proposals.PlainestLoop) {
		t.Errorf("Loops() = %v, want it to include %q", Loops(), proposals.PlainestLoop)
	}
}
