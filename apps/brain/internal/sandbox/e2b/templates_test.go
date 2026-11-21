package e2b

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

func TestBuildTemplateAsksForABuildThenStartsItFromTheImage(t *testing.T) {
	f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/templates":
			writeJSON(w, 202, map[string]any{"templateID": "tpl1", "buildID": "b1", "names": []string{"openarity-sandbox-abc"}})
		case "/v2/templates/tpl1/builds/b1":
			w.WriteHeader(202)
		default:
			http.NotFound(w, r)
		}
	}}
	b, err := newClient(f).BuildTemplate(t.Context(), "openarity-sandbox-abc", "ghcr.io/x/room:sha-abc")
	if err != nil {
		t.Fatal(err)
	}
	if b != (sandbox.Build{TemplateID: "tpl1", BuildID: "b1"}) {
		t.Fatalf("build = %+v", b)
	}

	if len(f.calls) != 2 {
		t.Fatalf("%d calls, want 2", len(f.calls))
	}
	var req map[string]any
	_ = json.Unmarshal(f.calls[0].Body, &req)
	if f.calls[0].Method != http.MethodPost || req["name"] != "openarity-sandbox-abc" ||
		req["cpuCount"] != float64(RoomCPUs) || req["memoryMB"] != float64(RoomMemoryMB) {
		t.Fatalf("request = %s %s", f.calls[0].Method, f.calls[0].Body)
	}
	if got := f.calls[0].Header.Get("X-API-Key"); got != apiKey {
		t.Fatalf("X-API-Key = %q", got)
	}
	var trig map[string]any
	_ = json.Unmarshal(f.calls[1].Body, &trig)
	if trig["fromImage"] != "ghcr.io/x/room:sha-abc" || trig["force"] != false {
		t.Fatalf("trigger = %s", f.calls[1].Body)
	}
	// E2B's schema wants steps present; an empty list, never null.
	if !strings.Contains(string(f.calls[1].Body), `"steps":[]`) {
		t.Fatalf("trigger has no empty steps: %s", f.calls[1].Body)
	}
}

func TestBuildTemplateKeepsTheIdsOfABuildItCouldNotStart(t *testing.T) {
	f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/templates" {
			writeJSON(w, 202, map[string]any{"templateID": "tpl1", "buildID": "b1"})
			return
		}
		writeJSON(w, 400, map[string]any{"message": "image not found " + apiKey})
	}}
	b, err := newClient(f).BuildTemplate(t.Context(), "n", "img")
	if err == nil || b.BuildID != "b1" {
		t.Fatalf("build = %+v, err = %v", b, err)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("the key leaked: %v", err)
	}
}

func TestBuildTemplateRefusedOrUnnamedIsAnError(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 403, map[string]any{"message": "no"}) },
		"no ids":  func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 202, map[string]any{}) },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{api: h}
			if _, err := newClient(f).BuildTemplate(t.Context(), "n", "img"); err == nil {
				t.Fatal("no error")
			}
			if len(f.calls) != 1 {
				t.Fatalf("%d calls: a build not asked for was started", len(f.calls))
			}
		})
	}
}

func TestBuildStatusMapsE2BsStates(t *testing.T) {
	for _, c := range []struct {
		status int
		body   map[string]any
		state  sandbox.BuildState
		reason string
	}{
		{200, map[string]any{"status": "building"}, sandbox.BuildRunning, ""},
		{200, map[string]any{"status": "waiting"}, sandbox.BuildRunning, ""},
		{200, map[string]any{"status": "ready"}, sandbox.BuildReady, ""},
		{200, map[string]any{"status": "error", "reason": map[string]any{"message": "pull failed"}}, sandbox.BuildFailed, "pull failed"},
		{200, map[string]any{"status": "error"}, sandbox.BuildFailed, "the build failed"},
		{404, map[string]any{"message": "gone"}, sandbox.BuildFailed, "E2B has no such build"},
	} {
		f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/templates/tpl1/builds/b1/status" || r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, c.status, c.body)
		}}
		state, reason, err := newClient(f).BuildStatus(t.Context(), sandbox.Build{TemplateID: "tpl1", BuildID: "b1"})
		if err != nil || state != c.state || reason != c.reason {
			t.Errorf("%v: %q %q %v, want %q %q", c.body, state, reason, err, c.state, c.reason)
		}
	}
}

func TestBuildStatusTroubleIsAnErrorNotAFailedBuild(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"down":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) },
		"unknown": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"status": "melting"}) },
	} {
		t.Run(name, func(t *testing.T) {
			state, _, err := newClient(&fake{api: h}).BuildStatus(t.Context(), sandbox.Build{TemplateID: "t", BuildID: "b"})
			if err == nil || state != "" {
				t.Fatalf("state %q, err %v", state, err)
			}
		})
	}
}
