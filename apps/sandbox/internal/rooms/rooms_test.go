package rooms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/guard"
)

const selfID = "selfcontainerid"

// fakeEngine is a daemon in memory: networks with members, and containers.
type fakeEngine struct {
	mu         sync.Mutex
	networks   map[string]*docker.Network
	containers map[string]docker.Container
	nextIP     int
	removed    []string

	absent   map[string]bool // images the daemon lacks, until pulled
	pulled   []string
	pullWait chan struct{} // when set, a pull blocks until it is closed
	pullErr  error
	calls    int // every call of the engine, for the tests that expect none

	stopped map[string]bool  // containers stopped, which have left their network
	volumes map[string]int64 // bytes each volume holds
	starts  int
}

func (f *fakeEngine) HasImage(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	return !f.absent[ref], nil
}

func (f *fakeEngine) PullImage(ctx context.Context, ref string) error {
	f.mu.Lock()
	f.calls++
	f.pulled = append(f.pulled, ref)
	wait, err := f.pullWait, f.pullErr
	f.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.calls++
	delete(f.absent, ref)
	f.mu.Unlock()
	return nil
}

func newEngine() *fakeEngine {
	return &fakeEngine{networks: map[string]*docker.Network{}, containers: map[string]docker.Container{}, nextIP: 2}
}

func (f *fakeEngine) CreateNetwork(_ context.Context, name string, labels map[string]string) error {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	f.networks[name] = &docker.Network{Name: name, Labels: labels, Internal: true, Containers: map[string]docker.Member{}}
	return nil
}

func (f *fakeEngine) InspectNetwork(_ context.Context, name string) (docker.Network, error) {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	n, ok := f.networks[name]
	if !ok {
		return docker.Network{}, &docker.Error{Status: 404}
	}
	cp := *n
	cp.Containers = map[string]docker.Member{}
	for k, v := range n.Containers {
		cp.Containers[k] = v
	}
	return cp, nil
}

func (f *fakeEngine) ListNetworks(_ context.Context, label string) ([]docker.Network, error) {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	var out []docker.Network
	for _, n := range f.networks {
		if _, ok := n.Labels[label]; ok {
			out = append(out, *n)
		}
	}
	return out, nil
}

func (f *fakeEngine) ConnectNetwork(_ context.Context, network, container string) error {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	n := f.networks[network]
	n.Containers[container] = docker.Member{Name: "self", IPv4: "10.9.0.2"}
	return nil
}

func (f *fakeEngine) DisconnectNetwork(_ context.Context, network, container string) error {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	if n := f.networks[network]; n != nil {
		delete(n.Containers, container)
	}
	return nil
}

func (f *fakeEngine) RemoveNetwork(_ context.Context, name string) error {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	if _, ok := f.networks[name]; !ok {
		return &docker.Error{Status: 404}
	}
	delete(f.networks, name)
	return nil
}

func (f *fakeEngine) CreateContainer(_ context.Context, name string, c docker.Container) (string, error) {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	f.containers[name] = c
	if n := f.networks[c.HostConfig.NetworkMode]; n != nil {
		n.Containers[name+"-id"] = docker.Member{Name: name, IPv4: "10.9.0.3"}
	}
	return name + "-id", nil
}

func (f *fakeEngine) StartContainer(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.starts++
	if c, ok := f.containers[name]; ok && f.stopped[name] {
		delete(f.stopped, name)
		if n := f.networks[c.HostConfig.NetworkMode]; n != nil {
			n.Containers[name+"-id"] = docker.Member{Name: name, IPv4: "10.9.0.4"}
		}
	}
	return nil
}

func (f *fakeEngine) StopContainer(_ context.Context, name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	c, ok := f.containers[name]
	if !ok {
		return &docker.Error{Status: 404}
	}
	if f.stopped == nil {
		f.stopped = map[string]bool{}
	}
	f.stopped[name] = true
	if n := f.networks[c.HostConfig.NetworkMode]; n != nil {
		delete(n.Containers, name+"-id")
	}
	return nil
}

func (f *fakeEngine) ContainerRunning(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if _, ok := f.containers[name]; !ok {
		return false, &docker.Error{Status: 404}
	}
	return !f.stopped[name], nil
}

func (f *fakeEngine) CreateVolume(_ context.Context, name string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.volumes == nil {
		f.volumes = map[string]int64{}
	}
	f.volumes[name] = 0
	return nil
}

func (f *fakeEngine) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if _, ok := f.volumes[name]; !ok {
		return &docker.Error{Status: 404}
	}
	delete(f.volumes, name)
	return nil
}

func (f *fakeEngine) VolumeSizes(context.Context, string) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := make(map[string]int64, len(f.volumes))
	for k, v := range f.volumes {
		out[k] = v
	}
	return out, nil
}

func (f *fakeEngine) RemoveContainer(_ context.Context, name string) error {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	c, ok := f.containers[name]
	if !ok {
		return &docker.Error{Status: 404}
	}
	delete(f.containers, name)
	if n := f.networks[c.HostConfig.NetworkMode]; n != nil {
		delete(n.Containers, name+"-id")
	}
	return nil
}

func (f *fakeEngine) ContainerID(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return selfID, nil
}

type fakeEgress struct {
	mu     sync.Mutex
	opened map[string][]guard.Host
	public map[string]bool
	closed []string
}

func (e *fakeEgress) Open(id string, _ netip.Addr, h []guard.Host, public bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.opened == nil {
		e.opened, e.public = map[string][]guard.Host{}, map[string]bool{}
	}
	e.opened[id], e.public[id] = h, public
	return nil
}

func (e *fakeEgress) Close(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = append(e.closed, id)
}

func service(l Limits) (*Service, *fakeEngine, *fakeEgress) {
	e, g := newEngine(), &fakeEgress{}
	return New(e, g, slog.New(slog.NewTextHandler(io.Discard, nil)), l, "sandboxd", []byte("key"), []string{"room", "x"}), e, g
}

func TestCreateMakesAnInternalNetworkJoinsSandboxdAndOpensTheGuard(t *testing.T) {
	t.Parallel()
	s, e, g := service(DefaultLimits())
	id, err := s.Create(t.Context(), Spec{Image: "room:1", Egress: []guard.Host{{Name: "imap.example.com", Port: 993}}})
	if err != nil || !ValidID(id) {
		t.Fatalf("Create = %q, %v", id, err)
	}
	n := e.networks[networkName(id)]
	if n == nil || !n.Internal {
		t.Fatalf("network = %+v", n)
	}
	if _, joined := n.Containers[selfID]; !joined {
		t.Fatal("sandboxd did not join the room's network")
	}
	if len(g.opened[id]) != 1 {
		t.Fatalf("guard = %v", g.opened)
	}
}

func TestCreateRefusesWhatItCannotHonourAndMakesNothing(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	bad := []Spec{
		{},
		{Image: "a b"},
		{Image: "x", Env: map[string]string{"A=B": "c"}},
		{Image: "x", Egress: []guard.Host{{Name: "a.example.com", Port: 25}, {Name: "b.example.com", Port: 25}}},
		{Image: "x", Network: "host"},
		{Image: "x", Network: NetworkPublic, Egress: []guard.Host{{Name: "a.example.com", Port: 443}}},
	}
	for _, sp := range bad {
		if _, err := s.Create(t.Context(), sp); !errors.Is(err, ErrRefused) {
			t.Errorf("Create(%+v) = %v, want ErrRefused", sp, err)
		}
	}
	if len(e.networks) != 0 {
		t.Fatalf("a refused spec left networks behind: %v", e.networks)
	}
}

// The budget is what keeps one team's rooms from starving the host.
func TestCreateIsRefusedWithErrBusyWhenTheBudgetIsFull(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = 2 * l.Memory
	s, e, _ := service(l)
	for range 2 {
		if _, err := s.Create(t.Context(), Spec{Image: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("third room: %v, want ErrBusy", err)
	}
	if len(e.networks) != 2 {
		t.Fatalf("networks = %d", len(e.networks))
	}
}

func TestDeletingARoomReturnsItsBudget(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = l.Memory
	s, _, _ := service(l)
	id, err := s.Create(t.Context(), Spec{Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

func TestHardenFixesEverythingARequestCouldWant(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	sp := Spec{
		Image:  "room:1",
		Env:    map[string]string{"B": "2", "A": "1", "HOME": "/root"},
		Labels: map[string]string{"team": "t", roomLabel: "spoof"},
		Egress: []guard.Host{{Name: "IMAP.example.com", Port: 993}, {Name: "imap.example.com", Port: 143}},
	}
	c := Harden("abc", sp, []string{"sandbox", "shim", "--", "npx"}, "10.0.0.2", l)
	h := c.HostConfig

	if c.User != "65534" {
		t.Errorf("user = %q", c.User)
	}
	if !h.ReadonlyRootfs || h.Privileged {
		t.Errorf("root or privilege: %+v", h)
	}
	if len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" {
		t.Errorf("capdrop = %v", h.CapDrop)
	}
	if len(h.SecurityOpt) != 1 || h.SecurityOpt[0] != "no-new-privileges" {
		t.Errorf("securityopt = %v", h.SecurityOpt)
	}
	for _, o := range h.SecurityOpt {
		if strings.Contains(o, "seccomp") {
			t.Errorf("seccomp must be the default: %v", h.SecurityOpt)
		}
	}
	if h.PidsLimit != 256 || h.Memory != 768<<20 || h.MemorySwap != 768<<20 || h.NanoCPUs != 500_000_000 {
		t.Errorf("limits = %+v", h)
	}
	if h.NetworkMode != "oa-sb-abc" {
		t.Errorf("network = %q", h.NetworkMode)
	}
	// /tmp stays small; the home holds an npx package and its cache.
	for p, size := range map[string]string{"/tmp": "size=67108864", "/home/sandbox": "size=536870912"} {
		if !strings.Contains(h.Tmpfs[p], size) {
			t.Errorf("tmpfs %s = %q, want %s", p, h.Tmpfs[p], size)
		}
	}
	if !strings.Contains(h.Tmpfs["/tmp"], "noexec") || !strings.Contains(h.Tmpfs["/home/sandbox"], ",exec,") {
		t.Errorf("exec: /tmp must be noexec and the home directory exec, got %v", h.Tmpfs)
	}
	if got := strings.Join(c.Env, ","); got != "HOME=/home/sandbox,A=1,B=2" {
		t.Errorf("env = %s: HOME must be fixed and the rest ordered", got)
	}
	if c.Labels[roomLabel] != "abc" || c.Labels["user.team"] != "t" || c.Labels["user."+roomLabel] != "spoof" {
		t.Errorf("labels = %v", c.Labels)
	}
	if len(h.ExtraHosts) != 1 || h.ExtraHosts[0] != "imap.example.com:10.0.0.2" {
		t.Errorf("hosts = %v", h.ExtraHosts)
	}
	if got := strings.Join(c.Entrypoint, " "); got != "sandbox shim -- npx" {
		t.Errorf("entrypoint = %s", got)
	}
}

func TestStartBuildsTheContainerOnTheRoomsNetworkOnce(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id, err := s.Create(t.Context(), Spec{Image: "room:1", Egress: []guard.Host{{Name: "imap.example.com", Port: 993}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), id, []string{"sandbox", "shim"}); err != nil {
		t.Fatal(err)
	}
	c := e.containers[containerName(id)]
	if c.Image != "room:1" || c.HostConfig.NetworkMode != networkName(id) || c.HostConfig.ExtraHosts[0] != "imap.example.com:10.9.0.2" {
		t.Fatalf("container = %+v", c)
	}
	if err := s.Start(t.Context(), id, []string{"x"}); !errors.Is(err, ErrStarted) {
		t.Fatalf("second start = %v", err)
	}
	addr, err := s.Target(t.Context(), id, 8080)
	if err != nil || addr != "10.9.0.3:8080" {
		t.Fatalf("Target = %q, %v", addr, err)
	}
}

func TestTargetOfAnUnstartedRoomIsErrNotStarted(t *testing.T) {
	t.Parallel()
	s, _, _ := service(DefaultLimits())
	id, _ := s.Create(t.Context(), Spec{Image: "x"})
	if _, err := s.Target(t.Context(), id, 8080); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnUnknownRoomIsErrNotFoundEverywhere(t *testing.T) {
	t.Parallel()
	s, _, _ := service(DefaultLimits())
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaa", "not-an-id", "../../x", ""} {
		if err := s.Start(t.Context(), id, []string{"x"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("Start(%q) = %v", id, err)
		}
		if _, err := s.Target(t.Context(), id, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("Target(%q) = %v", id, err)
		}
		if err := s.Delete(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) = %v", id, err)
		}
		if err := s.Exists(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Exists(%q) = %v", id, err)
		}
	}
}

func TestDeleteRemovesTheContainerTheNetworkAndTheListeners(t *testing.T) {
	t.Parallel()
	s, e, g := service(DefaultLimits())
	id, _ := s.Create(t.Context(), Spec{Image: "x", Egress: []guard.Host{{Name: "a.example.com", Port: 443}}})
	_ = s.Start(t.Context(), id, []string{"x"})
	if err := s.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if len(e.networks) != 0 || len(e.containers) != 0 {
		t.Fatalf("left behind: %v %v", e.networks, e.containers)
	}
	if len(g.closed) == 0 || g.closed[len(g.closed)-1] != id {
		t.Fatalf("guard closed %v", g.closed)
	}
	if err := s.Delete(t.Context(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestOnlyANetworkWithOurLabelIsARoom(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id := "0123456789abcdef01234567"
	e.networks[networkName(id)] = &docker.Network{Name: networkName(id), Labels: map[string]string{}}
	if err := s.Delete(t.Context(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a network that is not a room was deleted: %v", err)
	}
	if len(e.networks) != 1 {
		t.Fatal("it was removed")
	}
}

func TestReconcileOpensTheGuardForEveryRoomTheDaemonHolds(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id, _ := s.Create(t.Context(), Spec{Image: "x", Egress: []guard.Host{{Name: "a.example.com", Port: 443}}})

	g2 := &fakeEgress{}
	again := New(e, g2, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultLimits(), "sandboxd", []byte("key"), []string{"room"})
	delete(e.networks[networkName(id)].Containers, selfID) // sandboxd was recreated
	if err := again.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(g2.opened[id]) != 1 {
		t.Fatalf("not reopened: %v", g2.opened)
	}
	if _, ok := e.networks[networkName(id)].Containers[selfID]; !ok {
		t.Fatal("sandboxd did not rejoin the network")
	}
}

func TestTheProxyTokenIsPerRoomAndDerivedFromTheKey(t *testing.T) {
	t.Parallel()
	a, _, _ := service(DefaultLimits())
	b := New(newEngine(), &fakeEgress{}, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultLimits(), "x", []byte("other"), nil)
	if a.Token("r1") == a.Token("r2") || a.Token("r1") == b.Token("r1") || a.Token("r1") == "key" {
		t.Fatal("tokens collide or leak the key")
	}
	first := a.Token("r1")
	if first != a.Token("r1") {
		t.Fatal("not stable")
	}
}

type busyEgress struct{ fakeEgress }

func (*busyEgress) Open(string, netip.Addr, []guard.Host, bool) error {
	return fmt.Errorf("%w: port 9090", guard.ErrUnavailable)
}

// An egress port that is taken is a spec this sandboxd cannot honour, so it
// is the caller's 422, and the half-made room is removed.
func TestAnEgressPortSandboxdCannotListenOnIsRefusedAndLeavesNothing(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	s.egress = &busyEgress{}
	_, err := s.Create(t.Context(), Spec{Image: "x", Egress: []guard.Host{{Name: "a.example.com", Port: 9090}}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	if len(e.networks) != 0 {
		t.Fatal("a network was left behind")
	}
}

func TestAPublicRoomListingNoHostsStillOpensTheProxy(t *testing.T) {
	t.Parallel()
	s, _, g := service(DefaultLimits())
	id, err := s.Create(t.Context(), Spec{Image: "room:1", Network: NetworkPublic})
	if err != nil {
		t.Fatal(err)
	}
	if !g.public[id] {
		t.Fatalf("a public room was opened as %v", g.public)
	}
	server, err := s.Create(t.Context(), Spec{Image: "room:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, opened := g.opened[server]; opened {
		t.Fatal("a room with no hosts and no public network was given the guard")
	}
}

// A workspace takes more of the budget than a server, so the budget sums
// what each room is, not a count of rooms.
func TestTheBudgetCountsEachRoomAtItsOwnSize(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	l.MemoryBudget = l.WorkspaceMemory + l.Memory
	s, _, _ := service(l)
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); err != nil {
		t.Fatalf("a server beside a workspace within the budget: %v", err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("past the budget: %v, want ErrBusy", err)
	}

	// The room asked for counts at its own size too: a workspace that does
	// not fit is refused where a server would have fitted.
	l.MemoryBudget = l.Memory + l.WorkspaceMemory - 1
	s, _, _ = service(l)
	if _, err := s.Create(t.Context(), Spec{Image: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), Spec{Image: "x", Workspace: true}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a workspace past the budget: %v, want ErrBusy", err)
	}
}

func TestHardenGivesAWorkspaceItsSizeAndAPublicRoomTheProxy(t *testing.T) {
	t.Parallel()
	l := DefaultLimits()
	c := Harden("abc", Spec{Image: "room:1", Network: NetworkPublic, Workspace: true}, []string{"sandbox", "workspace"}, "10.0.0.2", l)
	h := c.HostConfig
	if h.Memory != l.WorkspaceMemory || h.MemorySwap != l.WorkspaceMemory || h.NanoCPUs != l.WorkspaceCPUs {
		t.Errorf("limits = %d %d %d", h.Memory, h.MemorySwap, h.NanoCPUs)
	}
	// /workspace is a volume of the room's own, on disk, and never a tmpfs.
	if got := h.Mounts[WorkspaceDir]; got != volumeName("abc") {
		t.Errorf("/workspace is mounted from %q, want the room's volume", got)
	}
	if _, ok := h.Tmpfs[WorkspaceDir]; ok {
		t.Error("/workspace is a tmpfs: a stopped room would lose it")
	}
	env := strings.Join(c.Env, ",")
	for _, want := range []string{"HTTPS_PROXY=http://10.0.0.2:3128", "http_proxy=http://10.0.0.2:3128", "NO_PROXY=localhost"} {
		if !strings.Contains(env, want) {
			t.Errorf("env %s lacks %s", env, want)
		}
	}
	if len(h.ExtraHosts) != 0 {
		t.Errorf("a public room's hosts = %v", h.ExtraHosts)
	}
	server := Harden("abc", Spec{Image: "room:1"}, []string{"sandbox", "shim"}, "10.0.0.2", l)
	if _, ok := server.HostConfig.Tmpfs[WorkspaceDir]; ok || strings.Contains(strings.Join(server.Env, ","), "PROXY") {
		t.Errorf("a server's room was given a workspace or the proxy: %v %v", server.HostConfig.Tmpfs, server.Env)
	}
}
