package teamsettings

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const sbx = `"provider":"sandboxd","url":"https://sbx.example","api_key":"k"`

func TestANeverSavedTeamKeepsAStoppedWorkspaceADay(t *testing.T) {
	got := decode(t, newEnv().get(t))
	if got.Sandbox.KeepHours != 24 || got.Sandbox.IdleMinutes != nil || got.Sandbox.WorkspaceSize != nil {
		t.Fatalf("sandbox = %+v", got.Sandbox)
	}
}

func TestTheStopTimesAndSizeAreSavedAndShown(t *testing.T) {
	e := newEnv()
	got := decode(t, e.save(t, withSandbox(`{`+sbx+`,"keep_hours":72,"idle_minutes":10,"workspace_size":{"memory_mb":8192,"cpus":4,"disk_mb":20480}}`)))
	p := e.store.upserts[0]
	if *p.SandboxKeepHours != 72 || *p.SandboxIdleMinutes != 10 || *p.SandboxWorkspaceMemoryMb != 8192 ||
		*p.SandboxWorkspaceCpus != 4 || *p.SandboxWorkspaceDiskMb != 20480 {
		t.Fatalf("saved %+v", p)
	}
	z := got.Sandbox.WorkspaceSize
	if got.Sandbox.KeepHours != 72 || *got.Sandbox.IdleMinutes != 10 || z == nil || z.MemoryMB != 8192 || z.CPUs != 4 || z.DiskMB != 20480 {
		t.Fatalf("shown %+v", got.Sandbox)
	}
}

// What a save leaves out stays as it was; null sets it back to the
// provider's, or the deployment's.
func TestLeftOutKeepsAndNullResets(t *testing.T) {
	e := newEnv()
	e.save(t, withSandbox(`{`+sbx+`,"keep_hours":72,"idle_minutes":10,"workspace_size":{"memory_mb":8192,"cpus":4,"disk_mb":20480}}`))

	got := decode(t, e.save(t, withSandbox(`{"provider":"sandboxd","url":"https://sbx.example"}`)))
	if got.Sandbox.KeepHours != 72 || got.Sandbox.IdleMinutes == nil || got.Sandbox.WorkspaceSize == nil {
		t.Fatalf("a save that left them out changed them: %+v", got.Sandbox)
	}
	got = decode(t, e.save(t, withSandbox("")))
	if got.Sandbox.KeepHours != 72 || got.Sandbox.WorkspaceSize == nil {
		t.Fatalf("a save without the sandbox changed them: %+v", got.Sandbox)
	}

	got = decode(t, e.save(t, withSandbox(`{"provider":"sandboxd","url":"https://sbx.example","idle_minutes":null,"workspace_size":null}`)))
	if got.Sandbox.IdleMinutes != nil || got.Sandbox.WorkspaceSize != nil || got.Sandbox.KeepHours != 72 {
		t.Fatalf("null did not reset: %+v", got.Sandbox)
	}
}

func TestABadStopTimeOrSizeIsRefusedAndNothingIsSaved(t *testing.T) {
	for name, c := range map[string]struct{ body, says string }{
		"keep 0":          {`"keep_hours":0`, "keep_hours"},
		"keep a month":    {`"keep_hours":721`, "keep_hours"},
		"idle 0":          {`"idle_minutes":0`, "idle_minutes"},
		"idle two days":   {`"idle_minutes":2881`, "idle_minutes"},
		"memory too low":  {`"workspace_size":{"memory_mb":128,"cpus":1,"disk_mb":512}`, "memory_mb"},
		"no cpu":          {`"workspace_size":{"memory_mb":1024,"cpus":0,"disk_mb":512}`, "cpus"},
		"too much disk":   {`"workspace_size":{"memory_mb":1024,"cpus":1,"disk_mb":300000}`, "disk_mb"},
		"half a size":     {`"workspace_size":{"memory_mb":1024}`, "cpus"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			rec := e.save(t, withSandbox(`{`+sbx+`,`+c.body+`}`))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.says) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(e.store.upserts) != 0 || len(e.secrets.puts) != 0 {
				t.Fatal("a refused save wrote something")
			}
		})
	}
}

// E2B's size is its template's until a template is built per size.
func TestASizeForE2BIsRefusedForNow(t *testing.T) {
	e := newEnv()
	rec := e.save(t, withSandbox(`{"provider":"e2b","api_key":"k","workspace_size":{"memory_mb":4096,"cpus":2,"disk_mb":4096}}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "e2b") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	_ = uuid.Nil
}
