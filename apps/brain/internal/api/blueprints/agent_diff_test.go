package blueprints

import (
	"slices"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A plan names what an apply would change on an agent: giving it a browser,
// or taking one away, is a change of its own, apart from the web.
func TestAPlanNamesABrowserGrantedOrTakenAway(t *testing.T) {
	t.Parallel()

	cur := db.Agent{Name: "clicker", Pattern: "react", MaxSteps: 5, Delegation: "none"}
	same := db.CreateAgentParams{Name: "clicker", Pattern: "react", MaxSteps: 5, Delegation: "none"}
	if got := agentDiff(cur, same); len(got) != 0 {
		t.Fatalf("agentDiff of the same agent = %v, want nothing", got)
	}

	granted := same
	granted.Browser = true
	if got := agentDiff(cur, granted); !slices.Equal(got, []string{"browser"}) {
		t.Errorf("agentDiff with a browser granted = %v, want [browser]", got)
	}

	cur.Browser = true
	if got := agentDiff(cur, same); !slices.Equal(got, []string{"browser"}) {
		t.Errorf("agentDiff with the browser taken away = %v, want [browser]", got)
	}
}
