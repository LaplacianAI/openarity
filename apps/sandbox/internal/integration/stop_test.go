package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// workspaceRoom is a public workspace room running sleep.
func (e *env) workspaceRoom() (id, container string) {
	e.t.Helper()
	status, b := e.api("POST", "/v1/rooms", map[string]any{
		"image": roomImg, "env": map[string]string{}, "egress": []egress{},
		"labels": map[string]string{"test": "it"}, "network": "public", "workspace": true,
	})
	if status != 201 {
		e.t.Fatalf("create = %d %s", status, b)
	}
	var out struct{ ID string }
	_ = json.Unmarshal(b, &out)
	e.t.Cleanup(func() { e.api("DELETE", "/v1/rooms/"+out.ID, nil) })
	if status, b := e.api("POST", "/v1/rooms/"+out.ID+"/start", map[string]any{"argv": []string{"sleep", "3600"}}); status != 204 {
		e.t.Fatalf("start = %d %s", status, b)
	}
	return out.ID, "oa-sb-" + out.ID
}

// A stopped workspace keeps its files, and gives its memory back.
func TestAStoppedWorkspaceKeepsItsFilesAndItsBudget(t *testing.T) {
	// One workspace's worth: 1.5 GiB.
	e := setup(t, "1610612736")
	id, ct := e.workspaceRoom()

	if out, err := e.exec(ct, "sh", "-c", "echo kept > /workspace/note && id -u && stat -c %a /workspace"); err != nil {
		t.Fatalf("writing in /workspace as the room's user: %v %s", err, out)
	} else if !strings.Contains(out, "65534") {
		t.Fatalf("the room runs as %q", out)
	}

	if status, b := e.api("POST", "/v1/rooms/"+id+"/stop", nil); status != 204 {
		t.Fatalf("stop = %d %s", status, b)
	}
	if state := e.mustDocker("inspect", "-f", "{{.State.Running}}", ct); strings.TrimSpace(state) != "false" {
		t.Fatalf("running = %s after a stop", state)
	}

	// The budget holds one workspace: a second fits only because the first
	// stopped. It is made and then deleted, so the first can resume.
	status, b := e.api("POST", "/v1/rooms", map[string]any{
		"image": roomImg, "env": map[string]string{}, "egress": []egress{}, "network": "public", "workspace": true,
	})
	if status != 201 {
		t.Fatalf("a second workspace while the first is stopped = %d %s", status, b)
	}
	var other struct{ ID string }
	_ = json.Unmarshal(b, &other)
	if status, b := e.api("POST", "/v1/rooms/"+id+"/resume", nil); status != 503 {
		t.Fatalf("resume while the second holds the budget = %d %s, want 503", status, b)
	}
	if status, _ := e.api("DELETE", "/v1/rooms/"+other.ID, nil); status != 204 {
		t.Fatalf("delete the second = %d", status)
	}

	if status, b := e.api("POST", "/v1/rooms/"+id+"/resume", nil); status != 204 {
		t.Fatalf("resume = %d %s", status, b)
	}
	if out, err := e.exec(ct, "cat", "/workspace/note"); err != nil || strings.TrimSpace(out) != "kept" {
		t.Fatalf("after a resume /workspace/note = %q, %v", out, err)
	}

	if status, _ := e.api("DELETE", "/v1/rooms/"+id, nil); status != 204 {
		t.Fatalf("delete = %d", status)
	}
	if out, _ := e.docker("volume", "ls", "-q", "--filter", "name=oa-sb-"+id); strings.TrimSpace(out) != "" {
		t.Fatalf("the workspace's volume outlived its room: %s", out)
	}
}
