package teamsettings

import (
	"net/http"
	"strings"
	"testing"
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
		"keep 0":         {`"keep_hours":0`, "keep_hours"},
		"keep a month":   {`"keep_hours":721`, "keep_hours"},
		"idle 0":         {`"idle_minutes":0`, "idle_minutes"},
		"idle two days":  {`"idle_minutes":2881`, "idle_minutes"},
		"memory too low": {`"workspace_size":{"memory_mb":128,"cpus":1,"disk_mb":512}`, "memory_mb"},
		"no cpu":         {`"workspace_size":{"memory_mb":1024,"cpus":0,"disk_mb":512}`, "cpus"},
		"too much disk":  {`"workspace_size":{"memory_mb":1024,"cpus":1,"disk_mb":300000}`, "disk_mb"},
		"half a size":    {`"workspace_size":{"memory_mb":1024}`, "cpus"},
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
}

// With no sandbox the times are still checked: a bad one is the caller's
// 400, never the table's CHECK as a 500.
func TestBadStopTimesAreRefusedWithNoSandboxToo(t *testing.T) {
	for _, body := range []string{
		`{"provider":"none","keep_hours":0}`,
		`{"provider":"none","idle_minutes":0}`,
		`{"provider":"none","workspace_size":{"memory_mb":1024,"cpus":99,"disk_mb":512}}`,
	} {
		e := newEnv()
		if rec := e.save(t, withSandbox(body)); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := newEnv().save(t, withSandbox(`{"provider":"none"}`)); rec.Code != http.StatusOK {
		t.Fatalf("none with no times: %d %s", rec.Code, rec.Body)
	}
}

// A team moving to E2B goes back to the provider's size, whatever sent it:
// the CLI and a raw PUT, not only the dashboard.
func TestMovingToE2BClearsTheSize(t *testing.T) {
	e := newEnv()
	e.save(t, withSandbox(`{`+sbx+`,"workspace_size":{"memory_mb":8192,"cpus":4,"disk_mb":20480}}`))
	got := decode(t, e.save(t, withSandbox(`{"provider":"e2b","api_key":"k"}`)))
	if got.Sandbox.WorkspaceSize != nil {
		t.Fatalf("size = %+v after moving to e2b", got.Sandbox.WorkspaceSize)
	}
	p := e.store.upserts[len(e.store.upserts)-1]
	if p.SandboxWorkspaceMemoryMb != nil || p.SandboxWorkspaceCpus != nil || p.SandboxWorkspaceDiskMb != nil {
		t.Fatalf("saved a size for e2b: %+v", p)
	}
}

// keep_hours cannot be unset: null is read as left out, and keeps it.
func TestANullKeepHoursKeepsIt(t *testing.T) {
	e := newEnv()
	e.save(t, withSandbox(`{`+sbx+`,"keep_hours":72}`))
	rec := e.save(t, withSandbox(`{"provider":"sandboxd","url":"https://sbx.example","keep_hours":null}`))
	if got := decode(t, rec); rec.Code != http.StatusOK || got.Sandbox.KeepHours != 72 {
		t.Fatalf("%d, keep_hours %d", rec.Code, got.Sandbox.KeepHours)
	}
}
