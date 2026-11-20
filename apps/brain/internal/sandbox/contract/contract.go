// Package contract is what every sandbox provider must do for a workspace,
// as a test it runs against a real one. A provider is done when its
// integration test calls Workspace and it passes: the workspace role starts
// in a public room, works in /workspace, reaches a public host, and reaches
// no private one. Only through the Rooms port, so a provider added later is
// held to exactly what the others are.
package contract

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/workroom"
)

// Private are addresses a public room must never reach: cloud metadata,
// the private ranges, and Docker's default bridge, where a host's own
// services listen.
var Private = []string{
	"http://169.254.169.254/latest/meta-data/",
	"http://10.0.0.1/",
	"http://172.17.0.1/",
	"http://192.168.0.1/",
}

// PublicURL is a host every provider must reach.
const PublicURL = "https://example.com/"

// Workspace makes a public workspace room from image at rooms, runs the
// workspace role in it, and holds it to the contract. The room is deleted
// when the test ends.
func Workspace(t *testing.T, rooms sandbox.Rooms, image string, hc *http.Client) {
	t.Helper()
	ctx := t.Context()
	token := rand.Text()
	want := workroom.Want()

	room, err := rooms.Create(ctx, sandbox.Spec{
		Image:     image,
		Env:       map[string]string{want.TokenEnv: token},
		Network:   want.Network,
		Workspace: want.Workspace,
		Labels:    map[string]string{"openarity-test": "contract"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := rooms.Delete(ctx, room); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})
	if err := rooms.Start(ctx, room, want.Argv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base, err := rooms.URL(ctx, room, want.Port)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	ep := sandbox.Endpoint{URL: base.URL, Header: base.Header.Clone()}
	if ep.Header == nil {
		ep.Header = http.Header{}
	}
	ep.Header.Set("Authorization", "Bearer "+token)
	awaitHealthy(t, hc, ep)
	ws := workroom.NewClient(ep, hc)

	t.Run("works in /workspace", func(t *testing.T) {
		if _, err := ws.Write(ctx, "probe.txt", "probe-value\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
		out, err := ws.Exec(ctx, "pwd; cat probe.txt", 30)
		if err != nil || out.Stdout != workroom.Dir+"\nprobe-value\n" {
			t.Fatalf("Exec = %+v, %v", out, err)
		}
	})

	t.Run("reaches a public host", func(t *testing.T) {
		if code := status(t, ws, "", PublicURL); code != "200" {
			t.Fatalf("%s answered %q, want 200", PublicURL, code)
		}
	})

	t.Run("reaches no private address", func(t *testing.T) {
		for _, u := range Private {
			// Through whatever proxy the room is given, and around it, as a
			// program that ignores HTTP_PROXY would. 000 is no answer; a
			// proxy's refusal is a 4xx or 5xx. Anything below 400 is
			// something private that answered.
			for _, how := range []string{"", "--noproxy '*'"} {
				if code := status(t, ws, how, u); code != "000" && code < "400" {
					t.Errorf("%s %s answered %s: a public room reached a private address", how, u, code)
				}
			}
		}
	})
}

// status is what curl in the room got from u, with the flags given: its HTTP
// status, or 000.
func status(t *testing.T, ws *workroom.Client, flags, u string) string {
	t.Helper()
	out, err := ws.Exec(t.Context(), fmt.Sprintf("curl -s -o /dev/null --max-time 10 -w '%%{http_code}' %s %q", flags, u), 30)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	return strings.TrimSpace(out.Stdout)
}

func awaitHealthy(t *testing.T, hc *http.Client, ep sandbox.Endpoint) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	last := "no answer"
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.TrimRight(ep.URL, "/")+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = ep.Header.Clone()
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = resp.Status
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the workspace never answered /healthz: %s", last)
}
