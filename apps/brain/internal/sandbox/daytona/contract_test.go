package daytona

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/contract"
)

// TestDaytonaKeepsTheWorkspaceContract runs the contract against Daytona
// cloud; a self-hosted Daytona refuses a public room, which
// TestAPublicRoomOnASelfHostedDaytonaIsRefused holds. It needs
// DAYTONA_TEST_API_KEY, and DAYTONA_TEST_IMAGE for the room image.
func TestDaytonaKeepsTheWorkspaceContract(t *testing.T) {
	key, image := os.Getenv("DAYTONA_TEST_API_KEY"), os.Getenv("DAYTONA_TEST_IMAGE")
	if key == "" || image == "" {
		t.Skip("DAYTONA_TEST_API_KEY and DAYTONA_TEST_IMAGE are not set")
	}
	c := New("", key, nil)
	c.StartTimeout = 10 * time.Minute
	contract.Workspace(t, c, image, &http.Client{Timeout: time.Minute})
}
