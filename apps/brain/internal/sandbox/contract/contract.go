// Package contract is what every sandbox provider must do for a workspace,
// as a test it runs against a real one. A provider is done when its
// integration test calls Workspace and it passes: the workspace role starts
// in a public room, works in /workspace, reaches a public host, reaches no
// private one, and keeps its files through a stop and a resume. Only through the Rooms port, so a provider added later is
// held to exactly what the others are.
package contract

import (
	"context"
	"crypto/rand"
	"errors"
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
	// The room's address and its provider's credential, asked for again
	// after a resume, as the keeper does: a provider may hand out new ones.
	connect := func(t *testing.T) (*workroom.Client, sandbox.Endpoint) {
		t.Helper()
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
		return workroom.NewClient(ep, hc), ep
	}
	ws, ep := connect(t)

	t.Run("works in /workspace", func(t *testing.T) {
		if _, err := ws.Write(ctx, "probe.txt", "probe-value\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
		out, err := ws.Exec(ctx, "pwd; cat probe.txt", 30)
		if err != nil || out.Stdout != workroom.Dir+"\nprobe-value\n" {
			t.Fatalf("Exec = %+v, %v", out, err)
		}
	})

	t.Run("keeps its token from the room", func(t *testing.T) {
		// The token guards the room from outside; a command that read it
		// could hand it to whoever wrote the prompt it is following.
		out, err := ws.Exec(ctx, "grep -l -s -a "+want.TokenEnv+"= /proc/[0-9]*/environ; env | grep -c "+want.TokenEnv+"; true", 30)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if got := strings.TrimSpace(out.Stdout); got != "0" {
			t.Errorf("a command found the token: %q", got)
		}
		var p *workroom.Problem
		if _, err := ws.Read(ctx, "/proc/self/environ", 0, 0); !errors.As(err, &p) {
			t.Errorf("Read of the workspace's own environ: %v, want it refused", err)
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

	// Last, since it stops the room: a session's workspace is stopped when
	// its last run ends, and its next call must find the files it left.
	t.Run("keeps its files through a stop and a resume", func(t *testing.T) {
		if _, err := ws.Write(ctx, "kept.txt", "kept-value\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := rooms.Stop(ctx, room); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		// Twice, as a reaper sweep and a run's end may both ask.
		if err := rooms.Stop(ctx, room); err != nil {
			t.Fatalf("Stop of a stopped room: %v", err)
		}
		// Stopped is not running: what it held is given back.
		if healthy(ctx, ep) {
			t.Fatal("the workspace still answers after Stop")
		}
		if err := rooms.Resume(ctx, room, want.Argv); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		resumed, _ := connect(t)
		out, err := resumed.Exec(ctx, "cat kept.txt", 30)
		if err != nil || out.Stdout != "kept-value\n" {
			t.Fatalf("after a resume, Exec = %+v, %v; want the file written before the stop", out, err)
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

// healthy is whether the workspace answers /healthz within a few seconds.
func healthy(ctx context.Context, ep sandbox.Endpoint) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ep.URL, "/")+"/healthz", nil)
	if err != nil {
		return false
	}
	req.Header = ep.Header.Clone()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
