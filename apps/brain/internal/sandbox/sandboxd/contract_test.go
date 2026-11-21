package sandboxd_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/contract"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxd"
)

// TestSandboxdKeepsTheWorkspaceContract runs the contract against a live
// sandboxd. It needs SANDBOXD_TEST_URL, SANDBOXD_TEST_TOKEN and
// SANDBOXD_TEST_IMAGE, the room image sandboxd allows.
func TestSandboxdKeepsTheWorkspaceContract(t *testing.T) {
	url, token, image := os.Getenv("SANDBOXD_TEST_URL"), os.Getenv("SANDBOXD_TEST_TOKEN"), os.Getenv("SANDBOXD_TEST_IMAGE")
	if url == "" || token == "" || image == "" {
		t.Skip("SANDBOXD_TEST_URL, SANDBOXD_TEST_TOKEN and SANDBOXD_TEST_IMAGE are not set")
	}
	hc := &http.Client{Timeout: time.Minute}
	contract.Workspace(t, sandboxd.New(url, token, hc), image, hc)
}
