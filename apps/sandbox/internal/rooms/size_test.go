package rooms

import (
	"errors"
	"strings"
	"testing"
)

func TestAWorkspaceIsMadeAtItsTeamsSize(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.WorkspaceMemory, l.WorkspaceCPUs, l.WorkspaceBytes = 8<<30, 4_000_000_000, 4<<30
	sp := Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 4096, CPUs: 2, DiskMB: 1024}}
	h := Harden("abc", sp, []string{"sandbox", "workspace"}, "10.0.0.2", l).HostConfig
	if h.Memory != 4<<30 || h.MemorySwap != 4<<30 || h.NanoCPUs != 2_000_000_000 {
		t.Fatalf("memory %d swap %d cpus %d", h.Memory, h.MemorySwap, h.NanoCPUs)
	}
	if m, _, d := l.workspaceSize(sp); m != 4<<30 || d != 1<<30 {
		t.Fatalf("size %d %d", m, d)
	}
}

func TestASizePastTheOperatorsIsRefusedBeforeAnythingIsMade(t *testing.T) {
	t.Parallel()
	l := DefaultLimits() // 1.5 GiB, one CPU, 512 MiB of disk per workspace
	for name, c := range map[string]struct {
		sp   Spec
		says string
	}{
		"memory":         {Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 2048, CPUs: 1, DiskMB: 256}}, "2048 MiB of memory"},
		"cpus":           {Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 1024, CPUs: 2, DiskMB: 256}}, "2 CPUs"},
		"disk":           {Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 1024, CPUs: 1, DiskMB: 1024}}, "1024 MiB of disk"},
		"zero":           {Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 1024, CPUs: 0, DiskMB: 256}}, "more than zero"},
		"below the home": {Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 256, CPUs: 1, DiskMB: 256}}, "less than the room's home"},
		"a server":       {Spec{Image: "x", Size: &Size{MemoryMB: 512, CPUs: 1, DiskMB: 256}}, "only a workspace"},
	} {
		s, e, _ := service(l)
		_, err := s.Create(t.Context(), c.sp)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want refused saying %q", name, err, c.says)
		}
		if len(e.networks) != 0 {
			t.Errorf("%s: a network was made", name)
		}
	}
}

// The budget counts each workspace at its own size: two small ones fit where
// one at the operator's size would leave no room.
func TestTheBudgetCountsAWorkspaceAtItsOwnSize(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = l.WorkspaceMemory // 1.5 GiB
	s, _, _ := service(l)
	small := Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 768, CPUs: 1, DiskMB: 256}}
	for range 2 {
		if _, err := s.Create(t.Context(), small); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(t.Context(), small); !errors.Is(err, ErrBusy) {
		t.Fatalf("a third: %v, want ErrBusy", err)
	}
}

func TestAWorkspaceIsHeldToItsOwnDisk(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = l.WorkspaceMemory
	s, e, _ := service(l)
	id, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true, Size: &Size{MemoryMB: 1024, CPUs: 1, DiskMB: 256}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), id, []string{"sandbox", "workspace"}); err != nil {
		t.Fatal(err)
	}
	// Under the operator's 512 MiB, over the room's own 256.
	e.volumes[volumeName(id)] = 300 << 20
	s.measure(t.Context())
	if !e.stopped[containerName(id)] {
		t.Fatal("a workspace over its own disk, under the operator's, was left running")
	}
	if err := s.Resume(t.Context(), id); !errors.Is(err, ErrOverDisk) || !strings.Contains(err.Error(), "of 256") {
		t.Fatalf("resume: %v", err)
	}
}
