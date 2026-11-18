package daytona

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
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
		// t.Context is already cancelled when cleanups run.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := c.Delete(ctx, room); err != nil {
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
	var lastErr error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = ep.Header.Clone()
		resp, err := noRedirect.Do(req)
		lastErr = err
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
		t.Fatalf("preview answered %d (last error %v), want 200", status, lastErr)
	}
}

func headerNames(h http.Header) []string {
	var out []string
	for k := range h {
		out = append(out, k)
	}
	return out
}

// TestEgressAgainstARealDaytona makes a room that may reach only the hosts
// named in DAYTONA_TEST_EGRESS_ALLOW ("host:port", for example
// imap.gmail.com:993), has it try those and example.com:443, and reads what
// happened through the room's own web server. It shows what a given Daytona
// does with Spec.Egress, which is what egress.go has to be right about.
func TestEgressAgainstARealDaytona(t *testing.T) {
	apiURL, key := os.Getenv("DAYTONA_TEST_API_URL"), os.Getenv("DAYTONA_TEST_API_KEY")
	allow := os.Getenv("DAYTONA_TEST_EGRESS_ALLOW")
	if apiURL == "" || key == "" || allow == "" {
		t.Skip("DAYTONA_TEST_API_URL, DAYTONA_TEST_API_KEY and DAYTONA_TEST_EGRESS_ALLOW are not all set")
	}
	var egress []sandbox.HostPort
	targets := []string{"example.com:443"}
	for _, hp := range strings.Split(allow, ",") {
		host, port, ok := strings.Cut(hp, ":")
		n, err := strconv.Atoi(port)
		if !ok || err != nil {
			t.Fatalf("DAYTONA_TEST_EGRESS_ALLOW %q is not host:port", hp)
		}
		egress = append(egress, sandbox.HostPort{Host: host, Port: n})
		targets = append(targets, hp)
	}
	c := New(apiURL, key, nil)
	c.StartTimeout = 10 * time.Minute
	ctx := t.Context()
	room, err := c.Create(ctx, sandbox.Spec{Image: os.Getenv("DAYTONA_TEST_IMAGE"), Egress: egress})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := c.Delete(ctx, room); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})

	probe := `import socket
for t in ` + "%q" + `.split():
    h, p = t.split(":")
    s = socket.socket(); s.settimeout(6)
    try:
        s.connect((socket.gethostbyname(h), int(p))); print(t, "open")
    except Exception as e:
        print(t, "closed", type(e).__name__)
    s.close()
`
	script := fmt.Sprintf(probe, strings.Join(targets, " "))
	argv := []string{"sh", "-c", "mkdir -p /tmp/www && cd /tmp/www && python3 -c '" + strings.ReplaceAll(script, "'", `"`) + "' > out.txt; exec python3 -m http.server 8080"}
	if err := c.Start(ctx, room, argv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ep, err := c.URL(ctx, room, 8080)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	var body string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/out.txt", nil)
		req.Header = ep.Header.Clone()
		if resp, err := noRedirect.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body = string(b)
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("egress allowed %s:\n%.600s", allow, body)
	if body == "" {
		t.Fatal("no answer from the room")
	}
	if !strings.Contains(body, "example.com:443 closed") {
		t.Errorf("a host that was not allowed was reachable")
	}
	for _, hp := range strings.Split(allow, ",") {
		if !strings.Contains(body, hp+" open") {
			t.Errorf("%s was allowed and not reachable", hp)
		}
	}
}

// noRedirect is a client that reports a redirect to a sign-in page as one,
// instead of following it to a 200 that proves nothing about the token.
var noRedirect = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
