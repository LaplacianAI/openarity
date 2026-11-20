package e2b

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/contract"
)

// TestE2BKeepsTheWorkspaceContract runs the contract against E2B. It needs
// E2B_TEST_API_KEY, and E2B_TEST_TEMPLATE when the template is not
// openarity-sandbox.
func TestE2BKeepsTheWorkspaceContract(t *testing.T) {
	key := os.Getenv("E2B_TEST_API_KEY")
	if key == "" {
		t.Skip("E2B_TEST_API_KEY is not set")
	}
	template := os.Getenv("E2B_TEST_TEMPLATE")
	if template == "" {
		template = "openarity-sandbox"
	}
	hc := &http.Client{Timeout: time.Minute}
	contract.Workspace(t, New(key, hc), template, hc)
}
