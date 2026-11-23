package rooms

import (
	"errors"
	"strings"
	"testing"
)

// workspace is a started workspace room, under a budget of exactly one.
func workspace(t *testing.T) (*Service, *fakeEngine, string) {
	t.Helper()
	l := DefaultLimits()
	l.MemoryBudget = l.WorkspaceMemory
	s, e, _ := service(l)
	id, err := s.Create(t.Context(), Spec{Image: "x", Network: NetworkPublic, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), id, []string{"sandbox", "workspace"}); err != nil {
		t.Fatal(err)
	}
	return s, e, id
}

func TestAStoppedRoomGivesItsMemoryBack(t *testing.T) {
	t.Parallel()
	s, _, id := workspace(t)
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second workspace while one runs: %v, want ErrBusy", err)
	}
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); err != nil {
		t.Fatalf("a second workspace once the first stopped: %v", err)
	}
}

// A room made and not yet started holds its share: its start is coming.
func TestARoomNotYetStartedHoldsItsMemory(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = l.WorkspaceMemory
	s, _, _ := service(l)
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
}

func TestAResumeStartsTheSameContainerAndItsWorkspaceStays(t *testing.T) {
	t.Parallel()
	s, e, id := workspace(t)
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	starts := e.starts
	if err := s.Resume(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if e.starts != starts+1 || e.stopped[containerName(id)] {
		t.Fatalf("starts %d → %d, stopped %v", starts, e.starts, e.stopped)
	}
	if _, ok := e.volumes[volumeName(id)]; !ok {
		t.Fatal("the workspace's volume went with the stop")
	}
	if err := s.Resume(t.Context(), id); err != nil || e.starts != starts+1 {
		t.Fatalf("a resume of a running room: %v, %d starts", err, e.starts-starts)
	}
}

func TestAResumeThatWouldPassTheBudgetIsRefused(t *testing.T) {
	t.Parallel()
	s, e, id := workspace(t)
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	starts := e.starts
	if err := s.Resume(t.Context(), id); !errors.Is(err, ErrBusy) {
		t.Fatalf("resume with %s holding the budget: %v, want ErrBusy", other, err)
	}
	if e.starts != starts {
		t.Fatal("a refused resume started the container")
	}
}

func TestAWorkspaceOverItsDiskIsStoppedAndNotResumed(t *testing.T) {
	t.Parallel()
	s, e, id := workspace(t)
	e.volumes[volumeName(id)] = s.limits.WorkspaceBytes + 1

	s.measure(t.Context())
	if !e.stopped[containerName(id)] {
		t.Fatal("the watch left a workspace over its disk running")
	}
	starts := e.starts
	if err := s.Resume(t.Context(), id); !errors.Is(err, ErrOverDisk) {
		t.Fatalf("resume over disk: %v, want ErrOverDisk", err)
	}
	if e.starts != starts {
		t.Fatal("a workspace over its disk was started")
	}
}

func TestTheWatchLeavesAWorkspaceWithinItsDisk(t *testing.T) {
	t.Parallel()
	s, e, id := workspace(t)
	e.volumes[volumeName(id)] = s.limits.WorkspaceBytes
	s.measure(t.Context())
	if e.stopped[containerName(id)] {
		t.Fatal("a workspace at its limit, not over it, was stopped")
	}
}

func TestANewWorkspacePastTheDiskBudgetIsRefused(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.DiskBudget = 2 * l.WorkspaceBytes
	s, e, _ := service(l)
	e.volumes = map[string]int64{"oa-sb-other": l.WorkspaceBytes + 1}
	id, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(t.Context(), id, []string{"sandbox", "workspace"})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "could not free enough") {
		t.Fatalf("err = %v, want ErrBusy saying deleting stopped workspaces could not free enough", err)
	}
	if _, ok := e.containers[containerName(id)]; ok {
		t.Fatal("a container was made past the disk budget")
	}
	if _, ok := e.volumes[volumeName(id)]; ok {
		t.Fatal("a volume was made past the disk budget")
	}
}

func TestAWorkspaceGoesWithItsVolume(t *testing.T) {
	t.Parallel()
	s, e, id := workspace(t)
	if err := s.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.volumes[volumeName(id)]; ok {
		t.Fatal("the workspace's volume outlived its room")
	}
}

func TestAServersRoomHasNoVolume(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id, err := s.Create(t.Context(), Spec{Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), id, []string{"sandbox", "shim"}); err != nil {
		t.Fatal(err)
	}
	if len(e.volumes) != 0 || len(e.containers[containerName(id)].HostConfig.Mounts) != 0 {
		t.Fatalf("volumes %v, mounts %v", e.volumes, e.containers[containerName(id)].HostConfig.Mounts)
	}
}

func TestAStoppedRoomIsNotStartedAsNew(t *testing.T) {
	t.Parallel()
	s, _, id := workspace(t)
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), id, []string{"sandbox", "workspace"}); !errors.Is(err, ErrStarted) {
		t.Fatalf("start of a stopped room: %v, want ErrStarted", err)
	}
}

func TestStopAndResumeOfWhatIsNotThere(t *testing.T) {
	t.Parallel()
	s, _, _ := service(DefaultLimits())
	if err := s.Stop(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop of no room: %v", err)
	}
	if err := s.Resume(t.Context(), "0123456789abcdef01234567"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume of no room: %v", err)
	}
	id, err := s.Create(t.Context(), Spec{Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(t.Context(), id); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("stop of a room never started: %v", err)
	}
	if err := s.Resume(t.Context(), id); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("resume of a room never started: %v", err)
	}
}
