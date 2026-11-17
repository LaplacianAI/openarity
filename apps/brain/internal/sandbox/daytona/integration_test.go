package daytona

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// TestAgainstARealDaytona runs the four calls against a live Daytona: a room
// with no egress, a web server started in it, its preview address read with
// the token, and the room deleted. It needs DAYTONA_TEST_API_URL (ending in
// /api) and DAYTONA_TEST_API_KEY, and DAYTONA_TEST_IMAGE to name a snapshot
// when the default one is not wanted.
func TestAgainstARealDaytona(t *testing.T) {
	apiURL, key := os.Getenv("DAYTONA_TEST_API_URL"), os.Getenv("DAYTONA_TEST_API_KEY")
	if apiURL == "" || key == "" {
		t.Skip("DAYTONA_TEST_API_URL and DAYTONA_TEST_API_KEY are not set")
	}
	c := New(apiURL, key, nil)
	c.StartTimeout = 10 * time.Minute
	ctx := t.Context()

	room, err := c.Create(ctx, sandbox.Spec{
		Image:  os.Getenv("DAYTONA_TEST_IMAGE"),
		Env:    map[string]string{"OA_PROBE": "probe-value"},
		Labels: map[string]string{"openarity-test": "true"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Delete(t.Context(), room); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})

	argv := []string{"python3", "-m", "http.server", "8080"}
	if err := c.Start(ctx, room, argv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ep, err := c.URL(ctx, room, 8080)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	t.Logf("preview %s, headers %v", ep.URL, headerNames(ep.Header))

	var status int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = ep.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
			if status == http.StatusOK {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if status != http.StatusOK {
		t.Fatalf("preview answered %d, want 200", status)
	}
}

func headerNames(h http.Header) []string {
	var out []string
	for k := range h {
		out = append(out, k)
	}
	return out
}
