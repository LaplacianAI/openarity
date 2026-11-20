package keeper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// Every room made before Network and Workspace existed has a hash of a Want
// without them. Left unset they must add nothing to what is hashed, or every
// command server's room would be replaced on the deploy that added them.
func TestAWantWithoutANetworkOrAWorkspaceHashesAsBefore(t *testing.T) {
	t.Parallel()
	w := Want{Argv: []string{"sandbox", "shim"}, Egress: []sandbox.HostPort{{Host: "imap.example.com", Port: 993}}, Port: 8080}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Network", "Workspace"} {
		if strings.Contains(string(b), field) {
			t.Errorf("%s is in the hash of a Want that leaves it unset: %s", field, b)
		}
	}
}
