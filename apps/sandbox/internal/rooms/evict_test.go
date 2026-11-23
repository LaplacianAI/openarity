package rooms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
)

const (
	// testDisk is what each workspace holds, and may hold, in these tests.
	testDisk = 100
	// testWait bounds every call: a start that never returns fails the test
	// instead of hanging it.
	testWait = 30 * time.Second
)

var epoch = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func within(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	t.Cleanup(cancel)
	return ctx
}

// roomID is a valid room id that sorts by n.
func roomID(n int) string { return fmt.Sprintf("%024x", n) }

// evictService is a service whose disk budget is budget bytes, and whose
// workspaces are testDisk bytes each; memory is no limit.
func evictService(budget int64) (*Service, *fakeEngine) {
	l := DefaultLimits()
	l.MemoryBudget = 1 << 40
	l.WorkspaceBytes = testDisk
	l.DiskBudget = budget
	s, e, _ := service(l)
	e.createSize = testDisk
	return s, e
}

type state int

const (
	roomRunning state = iota
	roomStopped
	roomNeverRan    // a container made and never started: no stop time
	roomNoContainer // a volume and no container: a start under way
	roomResumed     // running again, with the stop time of the run before
)

// seed puts a workspace room on the daemon. stoppedAt is only read for a
// stopped or a resumed one.
func seed(e *fakeEngine, id string, bytes int64, st state, stoppedAt time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.volumes == nil {
		e.volumes = map[string]int64{}
	}
	if e.stopped == nil {
		e.stopped = map[string]bool{}
	}
	if e.finished == nil {
		e.finished = map[string]time.Time{}
	}
	e.volumes[volumeName(id)] = bytes
	e.networks[networkName(id)] = &docker.Network{
		Name:       networkName(id),
		Labels:     map[string]string{roomLabel: id, specLabel: `{"image":"x","workspace":true}`},
		Internal:   true,
		Containers: map[string]docker.Member{},
	}
	name := containerName(id)
	if st == roomNoContainer {
		return
	}
	e.containers[name] = docker.Container{HostConfig: docker.HostConfig{NetworkMode: networkName(id)}}
	e.networks[networkName(id)].Containers[name+"-id"] = docker.Member{Name: name, IPv4: "10.9.0.3"}
	switch st {
	case roomStopped:
		e.stopped[name], e.finished[name] = true, stoppedAt
		delete(e.networks[networkName(id)].Containers, name+"-id")
	case roomNeverRan:
		e.stopped[name] = true
		delete(e.networks[networkName(id)].Containers, name+"-id")
	case roomResumed:
		// The daemon keeps FinishedAt of the last run while the next one runs.
		e.finished[name] = stoppedAt
	case roomRunning, roomNoContainer:
		// A running room has no stop time; roomNoContainer returned above.
	}
}

// begin makes a workspace room and starts it.
func begin(t *testing.T, s *Service) (string, error) {
	t.Helper()
	ctx := within(t)
	id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	return id, s.Start(ctx, id, []string{"sandbox", "workspace"})
}

// whole reports whether the room is still there: teardown removes its
// volume and its network together, so either one missing is a delete.
func whole(e *fakeEngine, id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, vol := e.volumes[volumeName(id)]
	_, net := e.networks[networkName(id)]
	return vol && net
}

// gone reports whether nothing of the room is left.
func gone(e *fakeEngine, id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, vol := e.volumes[volumeName(id)]
	_, ctr := e.containers[containerName(id)]
	_, net := e.networks[networkName(id)]
	return !vol && !ctr && !net
}

func deleted(e *fakeEngine) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, v := range e.volumeGone {
		ids = append(ids, strings.TrimPrefix(v, networkPrefix))
	}
	return ids
}

// The longest stopped goes first, and no more go than the new room needs.
func TestStartDeletesTheLongestStoppedWorkspacesAndNoMoreThanItNeeds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		budget int64
		want   []string // in the order deleted
	}{
		{"a gap of one room", 3 * testDisk, []string{roomID(3)}},
		{"a gap of two rooms", 2 * testDisk, []string{roomID(3), roomID(1)}},
		{"a gap of part of a room", 3*testDisk + 1, []string{roomID(3)}},
		{"a gap of more than a room", 2*testDisk + 1, []string{roomID(3), roomID(1)}},
		{"no gap", 4 * testDisk, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(tt.budget)
			// Stopped oldest to newest: 3, 1, 2. Their ids order the other way.
			seed(e, roomID(1), testDisk, roomStopped, epoch.Add(2*time.Hour))
			seed(e, roomID(2), testDisk, roomStopped, epoch.Add(3*time.Hour))
			seed(e, roomID(3), testDisk, roomStopped, epoch.Add(1*time.Hour))
			id, err := begin(t, s)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if got := deleted(e); !slices.Equal(got, tt.want) {
				t.Fatalf("deleted %v, want %v", got, tt.want)
			}
			for _, v := range tt.want {
				if !gone(e, v) {
					t.Errorf("room %s was deleted in part", v)
				}
			}
			if _, ok := e.containers[containerName(id)]; !ok {
				t.Error("the room was not started")
			}
			if _, ok := e.volumes[volumeName(id)]; !ok {
				t.Error("the room has no volume")
			}
		})
	}
}

func TestStartNeverDeletesARunningWorkspace(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want only the stopped room", got)
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a running workspace was touched")
	}
}

// A volume with no container is a start under way, or not sandboxd's own.
func TestStartLeavesAVolumeWithNoContainer(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomNoContainer, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want only the stopped room", got)
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a room with no container was touched")
	}
}

// A container that never ran has no stop time to order it by.
func TestStartLeavesAContainerThatNeverRan(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomNeverRan, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want only the stopped room", got)
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a container that never ran was touched")
	}
}

func TestTheRoomBeingStartedIsNotACandidate(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	self, other := roomID(1), roomID(2)
	// The oldest stopped room is the one asking for a volume.
	seed(e, self, testDisk, roomStopped, epoch)
	seed(e, other, testDisk, roomStopped, epoch.Add(time.Hour))
	if err := s.workspaceVolume(within(t), self, Spec{Image: "x", Workspace: true}); err != nil {
		t.Fatalf("workspaceVolume: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{other}) {
		t.Fatalf("deleted %v, want %v", got, other)
	}
	if !whole(e, self) {
		t.Fatal("the room being started was deleted")
	}
}

func TestStartIsBusyAndDeletesNothingWhenStoppedRoomsCannotFreeEnough(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		budget int64
		seed   func(*fakeEngine)
	}{
		{"running rooms fill the budget", 2 * testDisk, func(e *fakeEngine) {
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), testDisk, roomRunning, time.Time{})
		}},
		{"stopped rooms free less than the gap", 3 * testDisk, func(e *fakeEngine) {
			seed(e, roomID(1), 5*testDisk/2, roomRunning, time.Time{})
			seed(e, roomID(2), testDisk/2, roomStopped, epoch)
			seed(e, roomID(3), testDisk/4, roomStopped, epoch)
		}},
		{"the rest cannot be deleted", 2 * testDisk, func(e *fakeEngine) {
			seed(e, roomID(1), 2*testDisk, roomNoContainer, time.Time{})
			seed(e, roomID(2), testDisk, roomNeverRan, time.Time{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(tt.budget)
			tt.seed(e)
			before := len(e.volumes)
			id, err := begin(t, s)
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("Start = %v, want ErrBusy", err)
			}
			// The operator's numbers stay in the log.
			if strings.ContainsAny(err.Error(), "0123456789") {
				t.Errorf("the error carries a number: %q", err)
			}
			if got := deleted(e); len(got) != 0 || len(e.removed) != 0 {
				t.Fatalf("deleted %v, removed %v: want nothing", got, e.removed)
			}
			if len(e.volumes) != before {
				t.Fatalf("volumes %d → %d", before, len(e.volumes))
			}
			if _, ok := e.containers[containerName(id)]; ok {
				t.Error("a container was made for a refused room")
			}
		})
	}
}

// teardown logs what it cannot remove and goes on, so the budget is checked
// again after.
func TestStartIsBusyWhenADeletedRoomDidNotFreeItsVolume(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	e.stuck = map[string]bool{volumeName(roomID(2)): true}
	id, err := begin(t, s)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Start = %v, want ErrBusy", err)
	}
	if _, ok := e.volumes[volumeName(id)]; ok {
		t.Error("a volume was made past the budget")
	}
	if _, ok := e.containers[containerName(id)]; ok {
		t.Error("a container was made past the budget")
	}
	if _, ok := e.containers[containerName(roomID(2))]; ok {
		t.Error("the room that was planned was not deleted")
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a running workspace was touched")
	}
}

func TestStartDeletesNothingWhenTheDaemonCannotBeAsked(t *testing.T) {
	t.Parallel()
	boom := errors.New("the daemon is down")
	tests := []struct {
		name string
		fail func(*fakeEngine)
	}{
		{"measuring the volumes", func(e *fakeEngine) { e.sizesErr = boom }},
		{"inspecting a container", func(e *fakeEngine) { e.stateErr = boom }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(2 * testDisk)
			seed(e, roomID(1), testDisk, roomStopped, epoch)
			seed(e, roomID(2), testDisk, roomStopped, epoch.Add(time.Hour))
			ctx := within(t)
			id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
			if err != nil {
				t.Fatal(err)
			}
			tt.fail(e)
			err = s.workspaceVolume(ctx, id, Spec{Image: "x", Workspace: true})
			if !errors.Is(err, boom) {
				t.Fatalf("workspaceVolume = %v, want the daemon's error", err)
			}
			if got := deleted(e); len(got) != 0 || len(e.removed) != 0 {
				t.Fatalf("deleted %v, removed %v: want nothing", got, e.removed)
			}
		})
	}
}

// Rooms stopped at the same moment are chosen by id, however the daemon
// lists them.
func TestEqualStopTimesAreBrokenByRoomID(t *testing.T) {
	t.Parallel()
	s, e := evictService(5 * testDisk)
	for _, n := range []int{5, 3, 1, 4, 2} {
		seed(e, roomID(n), testDisk, roomStopped, epoch)
	}
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(1)}) {
		t.Fatalf("deleted %v, want %v", got, roomID(1))
	}
}

// With a fake whose new volume is full size the moment it is made (the
// daemon's measures about nothing until it is written to), starts that race
// see each other's volumes: the room the budget has goes to two of them, and
// no room is deleted for two. This is only as strong as that fake; the lock
// itself is pinned by TestTheLockIsHeldFromTheMeasureToTheVolume.
func TestConcurrentStartsWithFullSizeVolumesTakeTheRoomOnceEach(t *testing.T) {
	t.Parallel()
	const starts = 6
	s, e := evictService(3 * testDisk)
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))

	ctx := within(t)
	ids := make([]string, starts)
	for i := range ids {
		id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	var wg sync.WaitGroup
	errs := make([]error, starts)
	gate := make(chan struct{})
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			errs[i] = s.Start(ctx, id, []string{"sandbox", "workspace"})
		}()
	}
	close(gate)
	wg.Wait()

	// Room for the running one and two more: the two stopped went for them.
	var ok, busy int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrBusy):
			busy++
		default:
			t.Errorf("Start: %v", err)
		}
	}
	if ok != 2 || busy != starts-2 {
		t.Fatalf("%d started and %d busy, want 2 and %d", ok, busy, starts-2)
	}
	if e.peak > 3*testDisk {
		t.Fatalf("the volumes held %d bytes together, past the budget of %d", e.peak, 3*testDisk)
	}
	got := deleted(e)
	slices.Sort(got)
	if want := []string{roomID(2), roomID(3)}; !slices.Equal(got, want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, name := range e.removed {
		if seen[name] {
			t.Errorf("%s was removed twice: two starts chose the same room", name)
		}
		seen[name] = true
	}
}

// A volume is about 0 bytes when it is made, so racing starts all fit, and
// what the lock still owes them is that no room is deleted twice and none is
// refused for a gap that is not there.
func TestConcurrentStartsWithEmptyNewVolumesDeleteNoRoomTwiceAndRefuseNone(t *testing.T) {
	t.Parallel()
	const starts = 6
	s, e := evictService(3 * testDisk)
	e.createSize = 0
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))

	ctx := within(t)
	ids := make([]string, starts)
	for i := range ids {
		id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	var wg sync.WaitGroup
	errs := make([]error, starts)
	gate := make(chan struct{})
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			errs[i] = s.Start(ctx, id, []string{"sandbox", "workspace"})
		}()
	}
	close(gate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Start %d: %v (an ErrBusy here is a refusal for no gap)", i, err)
		}
	}
	seen := map[string]bool{}
	for _, name := range e.removed {
		if seen[name] {
			t.Errorf("%s was removed twice: two starts chose the same room", name)
		}
		seen[name] = true
	}
	seen = map[string]bool{}
	for _, id := range deleted(e) {
		if seen[id] {
			t.Errorf("room %s was deleted twice", id)
		}
		seen[id] = true
		if id != roomID(2) && id != roomID(3) {
			t.Errorf("room %s was deleted, and it was not a stopped one", id)
		}
	}
	if !whole(e, roomID(1)) {
		t.Error("a running workspace was touched")
	}
}

// The lock is held across both measures and the deletes between them, so
// nothing else measures, evicts or creates in the middle. The start is held
// inside VolumeSizes, and the test tries the lock from outside.
func TestTheLockIsHeldFromTheMeasureToTheVolume(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		block int // which VolumeSizes call to hold
	}{
		{"in the first measure", 1},
		{"in the measure after the deletes", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(2 * testDisk)
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), testDisk, roomStopped, epoch)
			ctx := within(t)
			id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
			if err != nil {
				t.Fatal(err)
			}

			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			defer free()
			var calls atomic.Int32
			e.onSizes = func() {
				if int(calls.Add(1)) == tt.block {
					close(entered)
					<-release
				}
			}
			done := make(chan error, 1)
			go func() { done <- s.workspaceVolume(ctx, id, Spec{Image: "x", Workspace: true}) }()

			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("workspaceVolume ended before measure %d: %v", tt.block, err)
			case <-ctx.Done():
				t.Fatal("workspaceVolume never measured")
			}
			if s.mu.TryLock() {
				s.mu.Unlock()
				t.Fatal("the lock was free while a start was measuring")
			}
			if tt.block == 2 && !gone(e, roomID(2)) {
				t.Error("the second measure came before the deletes were done")
			}
			free()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("workspaceVolume: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("workspaceVolume never ended")
			}
			if !s.mu.TryLock() {
				t.Fatal("the lock was kept after the volume was made")
			}
			s.mu.Unlock()
			if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
				t.Fatalf("deleted %v, want %v", got, roomID(2))
			}
		})
	}
}

// A resumed room is running with the stop time of the run before, which can
// be the oldest there is. It is running, so it is not a candidate.
func TestStartLeavesAResumedRoomThatStoppedLongestAgo(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomResumed, epoch)
	seed(e, roomID(2), testDisk, roomStopped, epoch.Add(time.Hour))
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want only the stopped room", got)
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a resumed workspace was touched")
	}
}

// The same through the service's own Stop and Resume, whatever the fake's
// resume leaves behind.
func TestStartLeavesARoomThatWasStoppedAndResumed(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	ctx := within(t)
	old, err := begin(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(ctx, old); err != nil {
		t.Fatal(err)
	}
	// It stopped before the other one did, and it is running now.
	e.mu.Lock()
	e.finished[containerName(old)] = epoch
	e.mu.Unlock()
	running, finished, err := e.ContainerState(ctx, containerName(old))
	if err != nil || !running || finished.IsZero() {
		t.Fatalf("a resumed room is running %v with stop time %v, %v: want running with a stop time", running, finished, err)
	}
	seed(e, roomID(2), testDisk, roomStopped, epoch.Add(time.Hour))
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want only the stopped room", got)
	}
	if !whole(e, old) {
		t.Fatal("a resumed workspace was touched")
	}
}

// The caller going away in the middle must not leave a planned room half
// deleted, and the deletes are bounded by a deadline of their own.
func TestEveryPlannedRoomIsDeletedWhenTheCallerGivesUpDuringTheFirst(t *testing.T) {
	t.Parallel()
	for _, hook := range []string{"container", "volume"} {
		t.Run("the first removal is a "+hook, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(2 * testDisk)
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), testDisk, roomStopped, epoch)
			seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))
			ctx, cancel := context.WithCancel(within(t))
			defer cancel()
			id, err := s.Create(ctx, Spec{Image: "x", Workspace: true})
			if err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			e.honourCtx, e.hookOn, e.onFirstRemove = true, hook, cancel
			e.mu.Unlock()

			if err := s.workspaceVolume(ctx, id, Spec{Image: "x", Workspace: true}); err != nil {
				t.Fatalf("workspaceVolume: %v", err)
			}
			if ctx.Err() == nil {
				t.Fatal("the caller was never cancelled: the test proves nothing")
			}
			for _, v := range []string{roomID(2), roomID(3)} {
				if !gone(e, v) {
					t.Errorf("room %s was left deleted in part", v)
				}
			}
			if _, ok := e.volumes[volumeName(id)]; !ok {
				t.Error("the new volume was not made")
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			for _, end := range e.removeCtxEnds {
				if end.IsZero() || time.Until(end) > evictTimeout {
					t.Errorf("a removal ran with deadline %v, want one within %v", end, evictTimeout)
				}
			}
		})
	}
}

// A name that is not a room's is never inspected, let alone deleted, and a
// room that holds nothing frees nothing.
func TestStartNeverDeletesWhatIsNotAnEvictableRoom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		seed    func(*fakeEngine)
		inspect bool // whether the daemon may be asked about any container
	}{
		{"an id that is not an id", func(e *fakeEngine) {
			e.volumes["oa-sb-notanid"] = testDisk
		}, false},
		{"an id of the right length that is not hex", func(e *fakeEngine) {
			e.volumes[networkPrefix+strings.Repeat("z", 24)] = testDisk
		}, false},
		{"no prefix", func(e *fakeEngine) {
			e.volumes[roomID(9)] = testDisk
			e.volumes["workspace"] = testDisk
		}, false},
		{"a stopped room with an empty volume", func(e *fakeEngine) {
			seed(e, roomID(2), 0, roomStopped, epoch)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Running rooms fill the budget; only what is under test could free it.
			s, e := evictService(testDisk)
			e.volumes = map[string]int64{}
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			tt.seed(e)
			before := len(e.volumes)
			_, err := begin(t, s)
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("Start = %v, want ErrBusy", err)
			}
			if got := deleted(e); len(got) != 0 || len(e.removed) != 0 {
				t.Fatalf("deleted %v, removed %v: want nothing", got, e.removed)
			}
			if len(e.volumes) != before {
				t.Fatalf("volumes %d → %d", before, len(e.volumes))
			}
			for _, name := range e.inspected {
				if !tt.inspect && name != containerName(roomID(1)) {
					t.Errorf("the container %q was inspected", name)
				}
			}
		})
	}
}

// A stopped room with nothing in its volume frees nothing, so deleting it is
// only a loss, even when it is the one stopped longest and the plan goes on.
func TestStartLeavesAnEmptyStoppedRoomBesideARealVictim(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), 0, roomStopped, epoch)
	seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(3)}) {
		t.Fatalf("deleted %v, want %v", got, roomID(3))
	}
	if !whole(e, roomID(2)) {
		t.Fatal("an empty stopped room was deleted")
	}
}

// An invalid name beside a real victim is passed over, and the victim goes.
func TestStartPassesOverAnInvalidIDBesideARealVictim(t *testing.T) {
	t.Parallel()
	s, e := evictService(2*testDisk + 2) // the two stray volumes count against it
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch.Add(time.Hour))
	e.volumes["oa-sb-notanid"] = 1 // would be the oldest, if it were a room
	e.volumes["workspace"] = 1
	if _, err := begin(t, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := deleted(e); !slices.Equal(got, []string{roomID(2)}) {
		t.Fatalf("deleted %v, want %v", got, roomID(2))
	}
	for _, name := range e.inspected {
		if name == "oa-sb-notanid" || name == "workspace" {
			t.Errorf("the container %q was inspected", name)
		}
	}
}

// The plan takes the oldest first until the gap is covered, whatever size
// the rooms after are.
func TestTheOldestStoppedRoomsAreTakenUntilTheGapIsCovered(t *testing.T) {
	t.Parallel()
	const huge = 10 * testDisk
	tests := []struct {
		name   string
		budget int64
		seed   func(*fakeEngine)
		want   []string
	}{
		{"one huge room, bigger than the gap, is alone", 2 * testDisk, func(e *fakeEngine) {
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), huge, roomStopped, epoch)
		}, []string{roomID(2)}},
		{"a huge oldest room is alone, small newer ones stay", huge, func(e *fakeEngine) {
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), huge, roomStopped, epoch)
			seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))
			seed(e, roomID(4), testDisk, roomStopped, epoch.Add(2*time.Hour))
		}, []string{roomID(2)}},
		{"a huge newer room does not displace older small ones that cover the gap", 12 * testDisk, func(e *fakeEngine) {
			// Used with the new room: 100 + 100 + 100 + 1000 + 100 = 1400; the gap is 200.
			seed(e, roomID(1), testDisk, roomRunning, time.Time{})
			seed(e, roomID(2), testDisk, roomStopped, epoch)
			seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))
			seed(e, roomID(4), huge, roomStopped, epoch.Add(2*time.Hour))
		}, []string{roomID(2), roomID(3)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := evictService(tt.budget)
			tt.seed(e)
			if _, err := begin(t, s); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if got := deleted(e); !slices.Equal(got, tt.want) {
				t.Fatalf("deleted %v, want %v", got, tt.want)
			}
		})
	}
}

// Two rooms are planned, the second's volume will not go: the first is gone,
// the budget still has no room, and no volume is made for the new one.
func TestStartIsBusyWhenOnlyTheFirstOfTwoVictimsWasDeleted(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	seed(e, roomID(1), testDisk, roomRunning, time.Time{})
	seed(e, roomID(2), testDisk, roomStopped, epoch)
	seed(e, roomID(3), testDisk, roomStopped, epoch.Add(time.Hour))
	e.stuck = map[string]bool{volumeName(roomID(3)): true}
	id, err := begin(t, s)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Start = %v, want ErrBusy", err)
	}
	if !gone(e, roomID(2)) {
		t.Error("the first victim was not deleted")
	}
	if _, ok := e.volumes[volumeName(roomID(3))]; !ok {
		t.Error("the stuck volume is gone: the fake did not stick")
	}
	if _, ok := e.volumes[volumeName(id)]; ok {
		t.Error("a volume was made past the budget")
	}
	if _, ok := e.containers[containerName(id)]; ok {
		t.Error("a container was made past the budget")
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a running workspace was touched")
	}
}

// Only the first start frees disk: a resume of a room whose volume is there
// takes nothing new, and is refused, never made room for.
func TestResumeDoesNotEvict(t *testing.T) {
	t.Parallel()
	s, e := evictService(2 * testDisk)
	ctx := within(t)
	id, err := begin(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Every volume together now passes the budget, and another room is
	// stopped longer than this one.
	seed(e, roomID(1), 2*testDisk, roomStopped, epoch)
	if err := s.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := deleted(e); len(got) != 0 {
		t.Fatalf("a resume deleted %v", got)
	}

	// A resume that is refused deletes nothing either.
	if err := s.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.volumes[volumeName(id)] = 2*testDisk + 1
	e.mu.Unlock()
	if err := s.Resume(ctx, id); !errors.Is(err, ErrOverDisk) {
		t.Fatalf("Resume over its disk: %v, want ErrOverDisk", err)
	}
	if got := deleted(e); len(got) != 0 {
		t.Fatalf("a refused resume deleted %v", got)
	}
	if !whole(e, roomID(1)) {
		t.Fatal("a resume deleted another room")
	}
}
