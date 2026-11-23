// Package rooms makes, starts and removes rooms on a Docker daemon. It keeps
// no state of its own: a room is a network named oa-sb-<id> whose labels hold
// its spec, and the container that Start adds to it. Restart sandboxd and it
// finds every room again from the daemon.
package rooms

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/guard"
)

var (
	// ErrNotFound is a room the daemon does not hold.
	ErrNotFound = errors.New("no such room")
	// ErrBusy is a create refused because the memory budget or the disk
	// budget is spent.
	ErrBusy = errors.New("the sandbox is busy")
	// ErrRefused is a spec sandboxd cannot honour. The error wrapping it says why.
	ErrRefused = errors.New("the spec cannot be honoured")
	// ErrUnavailable is a room that cannot be made now: its image could not be
	// pulled. The error wrapping it names the image and says why.
	ErrUnavailable = errors.New("the sandbox cannot do that now")
	// ErrStarted is a start of a room that has already started.
	ErrStarted = errors.New("the room has already started")
	// ErrOverDisk is a workspace that holds more than its share of disk: it
	// was stopped, and is not resumed. Deleting it is what frees the disk.
	ErrOverDisk = errors.New("the workspace holds more than it may")
)

const (
	networkPrefix = "oa-sb-"
	roomLabel     = "oa.sandboxd.room"
	specLabel     = "oa.sandboxd.spec"
	// ContainerLabelPrefix marks the labels a request set, so they cannot
	// collide with sandboxd's own.
	userLabelPrefix = "user."
	// HomeDir is where HOME points: a tmpfs, because the root is read-only.
	HomeDir = "/home/sandbox"
	// WorkspaceDir is a workspace room's working directory, a volume of its
	// own, so its files outlive the room being stopped.
	WorkspaceDir = "/workspace"
	// stopGrace is how long a stopped room's process has before it is killed.
	stopGrace = 5 * time.Second
	// NetworkPublic is a room that reaches the internet, and nothing
	// private, through the guard's proxy, rather than a list of hosts.
	NetworkPublic = "public"
	// NobodyUser is the user every room runs as.
	NobodyUser = "65534"
)

// Limits are what each room gets, and what all of them together may take.
type Limits struct {
	Memory       int64 // bytes per room
	PidsLimit    int64
	NanoCPUs     int64
	MemoryBudget int64 // bytes, across rooms
	TmpBytes     int64 // the size of /tmp
	// HomeBytes is the size of the home directory, where npx and uvx keep a
	// package's download and its unpacked copy. A tmpfs is memory, charged to
	// the room's own limit, so it can never be filled past Memory.
	HomeBytes int64

	// A workspace room is an agent's, which builds and runs things, so it
	// gets more than a server's: WorkspaceMemory and WorkspaceCPUs in place
	// of Memory and NanoCPUs, and a /workspace on a volume of its own, on
	// disk, so it is kept while the room is stopped. WorkspaceBytes is what
	// one may hold and DiskBudget what all of them together may; both are
	// soft, measured every WatchEvery, since a volume on the local driver
	// has no size limit of its own without root.
	WorkspaceMemory int64
	WorkspaceCPUs   int64 // nano CPUs
	WorkspaceBytes  int64
	DiskBudget      int64
}

// WatchEvery is how often the workspaces' disk is measured.
const WatchEvery = 30 * time.Second

// DefaultLimits are 768 MiB, 256 pids, half a CPU, a 512 MiB home and a
// 64 MiB /tmp, 3 GiB in all; a workspace room has 1.5 GiB and a CPU, with a
// 512 MiB /workspace, and 8 GiB of disk across workspaces. An npx server's
// package and its cache can pass 64 MiB on their own, which a smaller home
// refused with nothing logged.
func DefaultLimits() Limits {
	return Limits{
		Memory: 768 << 20, PidsLimit: 256, NanoCPUs: 500_000_000, MemoryBudget: 3 << 30,
		TmpBytes: 64 << 20, HomeBytes: 512 << 20,
		WorkspaceMemory: 1536 << 20, WorkspaceCPUs: 1_000_000_000, WorkspaceBytes: 512 << 20,
		DiskBudget: 8 << 30,
	}
}

// Spec is what a request may say about a room: nothing else reaches the
// container.
type Spec struct {
	Image  string            `json:"image"`
	Env    map[string]string `json:"env"`
	Egress []guard.Host      `json:"egress"`
	Labels map[string]string `json:"labels"`
	// Network is "" for a room that reaches only its Egress, or
	// NetworkPublic.
	Network string `json:"network,omitempty"`
	// Workspace is a room an agent works in: more memory and CPU, and a
	// /workspace to work in.
	Workspace bool `json:"workspace,omitempty"`
	// Size is a workspace's own, in place of the operator's: no more than
	// the operator's in any part, which are the largest a workspace may be.
	Size *Size `json:"size,omitempty"`
}

// Size is what a team asked for its workspace rooms.
type Size struct {
	MemoryMB int64 `json:"memory_mb"`
	CPUs     int64 `json:"cpus"`
	DiskMB   int64 `json:"disk_mb"`
}

// workspaceSize is what a workspace room of sp gets: its own size, or the
// operator's.
func (l Limits) workspaceSize(sp Spec) (memory, nanoCPUs, disk int64) {
	if sp.Size == nil {
		return l.WorkspaceMemory, l.WorkspaceCPUs, l.WorkspaceBytes
	}
	return sp.Size.MemoryMB << 20, sp.Size.CPUs * 1_000_000_000, sp.Size.DiskMB << 20
}

// memory is what a room of this spec may use, which is what it takes of the
// budget.
func (l Limits) memory(sp Spec) int64 {
	if sp.Workspace {
		m, _, _ := l.workspaceSize(sp)
		return m
	}
	return l.Memory
}

// fitsSize refuses a size past the operator's, or one a room cannot run in.
func (l Limits) fitsSize(sp Spec) error {
	if sp.Size == nil {
		return nil
	}
	if !sp.Workspace {
		return fmt.Errorf("%w: only a workspace room is sized", ErrRefused)
	}
	m, c, d := l.workspaceSize(sp)
	switch {
	case sp.Size.MemoryMB <= 0 || sp.Size.CPUs <= 0 || sp.Size.DiskMB <= 0:
		return fmt.Errorf("%w: a size is memory, CPUs and disk, each more than zero", ErrRefused)
	case m > l.WorkspaceMemory:
		return fmt.Errorf("%w: %d MiB of memory is more than this sandbox gives a workspace, %d MiB", ErrRefused, sp.Size.MemoryMB, l.WorkspaceMemory>>20)
	case c > l.WorkspaceCPUs:
		return fmt.Errorf("%w: %d CPUs is more than this sandbox gives a workspace, %g", ErrRefused, sp.Size.CPUs, float64(l.WorkspaceCPUs)/1e9)
	case d > l.WorkspaceBytes:
		return fmt.Errorf("%w: %d MiB of disk is more than this sandbox gives a workspace, %d MiB", ErrRefused, sp.Size.DiskMB, l.WorkspaceBytes>>20)
	case l.HomeBytes > m:
		return fmt.Errorf("%w: %d MiB of memory is less than the room's home, %d MiB", ErrRefused, sp.Size.MemoryMB, l.HomeBytes>>20)
	}
	return nil
}

// Engine is the daemon.
type Engine interface {
	CreateNetwork(ctx context.Context, name string, labels map[string]string) error
	InspectNetwork(ctx context.Context, name string) (docker.Network, error)
	ListNetworks(ctx context.Context, label string) ([]docker.Network, error)
	ConnectNetwork(ctx context.Context, network, container string) error
	DisconnectNetwork(ctx context.Context, network, container string) error
	RemoveNetwork(ctx context.Context, name string) error
	CreateContainer(ctx context.Context, name string, c docker.Container) (string, error)
	StartContainer(ctx context.Context, name string) error
	StopContainer(ctx context.Context, name string, grace time.Duration) error
	ContainerRunning(ctx context.Context, name string) (bool, error)
	ContainerState(ctx context.Context, name string) (running bool, finishedAt time.Time, err error)
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeSizes(ctx context.Context, label string) (map[string]int64, error)
	RemoveContainer(ctx context.Context, name string) error
	ContainerID(ctx context.Context, nameOrID string) (string, error)
	HasImage(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string) error
}

// Egress is the guard.
type Egress interface {
	Open(id string, bind netip.Addr, hosts []guard.Host, public bool) error
	Close(id string)
}

// Service is the rooms of one daemon.
type Service struct {
	engine Engine
	egress Egress
	logger *slog.Logger
	limits Limits
	self   string   // sandboxd's own container
	key    []byte   // derives each room's proxy token
	images []string // the repository prefixes a room's image may be under

	pmu   sync.Mutex
	pulls map[string]*pull // by image, while one is under way

	mu     sync.Mutex // serialises the budget check with the create
	selfID string
}

// New returns a service. self names sandboxd's own container, which joins
// every room's network.
//
// images are the repository prefixes a room's image may be under: sandboxd
// pulls and runs nothing else, so a leaked token cannot run any image.
func New(e Engine, g Egress, logger *slog.Logger, l Limits, self string, key []byte, images []string) *Service {
	if logger == nil {
		panic("rooms: a nil logger")
	}
	return &Service{
		engine: e, egress: g, logger: logger, limits: l, self: self, key: key,
		images: images, pulls: map[string]*pull{},
	}
}

// Allowed reports whether image is under one of the repository prefixes. A
// prefix ends at a boundary of the reference: the end, a tag, a digest or a
// path segment, so "a/room" allows "a/room:1" and "a/room/sub" but not
// "a/room-evil" or "a/roomx".
func Allowed(image string, prefixes []string) bool {
	for _, p := range prefixes {
		rest, ok := strings.CutPrefix(image, p)
		if !ok {
			continue
		}
		switch {
		case rest == "", rest[0] == '@', rest[0] == '/':
			return true
		case rest[0] == ':':
			// a tag, unless a path follows: then it was a registry's port
			if !strings.Contains(rest, "/") {
				return true
			}
		}
	}
	return false
}

func (s *Service) allow(image string) error {
	if !Allowed(image, s.images) {
		return fmt.Errorf("%w: the image %q is not one this sandbox runs", ErrRefused, image)
	}
	return nil
}

// pull is one pull under way, which everyone who wants the image waits on.
type pull struct {
	done chan struct{}
	err  error
}

// pullTimeout bounds a pull that nobody's request owns.
const pullTimeout = 10 * time.Minute

// ensureImage makes sure the daemon holds image, pulling it if not. Callers
// for the same image share one pull. The pull outlives the request that
// started it: if that one gives up, the others waiting on it must not fail
// for it, and the next one finds the layers already there.
func (s *Service) ensureImage(ctx context.Context, image string) error {
	s.pmu.Lock()
	p, ok := s.pulls[image]
	if !ok {
		p = &pull{done: make(chan struct{})}
		s.pulls[image] = p
		go s.runPull(ctx, image, p)
	}
	s.pmu.Unlock()
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runPull(parent context.Context, image string, p *pull) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), pullTimeout)
	defer cancel()
	p.err = s.fetch(ctx, image)
	s.pmu.Lock()
	delete(s.pulls, image)
	s.pmu.Unlock()
	close(p.done)
}

// fetch checks again inside the shared pull: a caller that came just after
// one finished finds the image and pulls nothing.
func (s *Service) fetch(ctx context.Context, image string) error {
	have, err := s.engine.HasImage(ctx, image)
	if err != nil {
		return fmt.Errorf("%w: looking for the image %q: %w", ErrUnavailable, image, err)
	}
	if have {
		return nil
	}
	s.logger.Info("pulling an image", "image", image)
	if err := s.engine.PullImage(ctx, image); err != nil {
		return fmt.Errorf("%w: pulling the image %q failed: %w", ErrUnavailable, image, err)
	}
	return nil
}

// Warm pulls each image that is missing, one after another, and logs the
// outcome of each. It returns when the last is done or ctx ends; a failure
// is logged and goes no further, so a registry that is down at start does
// not stop sandboxd serving.
func (s *Service) Warm(ctx context.Context, images []string) {
	for _, image := range images {
		if err := s.allow(image); err != nil {
			s.logger.Error("not pulling an image", "image", image, "err", err.Error())
			continue
		}
		if err := s.ensureImage(ctx, image); err != nil {
			s.logger.Error("warming an image failed", "image", image, "err", err.Error())
			continue
		}
		s.logger.Info("image ready", "image", image)
	}
}

// Token is what the proxy accepts for a room. It is derived from sandboxd's
// token, so no room keeps one and the main token never leaves sandboxd.
func (s *Service) Token(id string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("proxy:" + id))
	return hex.EncodeToString(m.Sum(nil))
}

func networkName(id string) string   { return networkPrefix + id }
func containerName(id string) string { return networkPrefix + id }
func volumeName(id string) string    { return networkPrefix + id }

// ValidID reports whether id has the shape of one made here.
func ValidID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Validate refuses a spec before anything is made.
func Validate(sp Spec) error {
	if sp.Image == "" || strings.ContainsAny(sp.Image, " \t\n") {
		return fmt.Errorf("%w: the image is missing or malformed", ErrRefused)
	}
	for k := range sp.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("%w: %q is not an environment variable name", ErrRefused, k)
		}
	}
	for k := range sp.Labels {
		if k == "" {
			return fmt.Errorf("%w: a label has no name", ErrRefused)
		}
	}
	if err := guard.Validate(sp.Egress); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	switch sp.Network {
	case "":
	case NetworkPublic:
		if len(sp.Egress) > 0 {
			return fmt.Errorf("%w: a public room reaches every public host, so it lists none", ErrRefused)
		}
	default:
		return fmt.Errorf("%w: the network is %q or empty, not %q", ErrRefused, NetworkPublic, sp.Network)
	}
	return nil
}

// Create makes the room's network, joins sandboxd to it, and opens the guard.
func (s *Service) Create(ctx context.Context, sp Spec) (string, error) {
	if err := Validate(sp); err != nil {
		return "", err
	}
	if err := s.allow(sp.Image); err != nil {
		return "", err
	}
	if err := s.limits.fitsSize(sp); err != nil {
		return "", err
	}
	spec, err := json.Marshal(sp)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fits(ctx, "", sp); err != nil {
		return "", err
	}
	if err := s.engine.CreateNetwork(ctx, networkName(id), map[string]string{roomLabel: id, specLabel: string(spec)}); err != nil {
		return "", err
	}
	if err := s.open(ctx, id, sp); err != nil {
		s.teardown(ctx, id)
		return "", err
	}
	return id, nil
}

// fits is ErrBusy when a room of sp, added to every room holding memory now,
// would pass the budget; self is a room already counted as sp, or "". A
// stopped room holds none: only one that runs, or is made and not yet
// started, does. The caller holds s.mu.
func (s *Service) fits(ctx context.Context, self string, sp Spec) error {
	nets, err := s.engine.ListNetworks(ctx, roomLabel)
	if err != nil {
		return err
	}
	used := s.limits.memory(sp)
	for _, n := range nets {
		id := n.Labels[roomLabel]
		if id == self {
			continue
		}
		holds, err := s.holdsMemory(ctx, n, id)
		if err != nil {
			return err
		}
		if holds {
			// Every spec is written here as JSON; one unreadable would count
			// as a server's, which every room was before workspaces.
			other, _ := specOf(n)
			used += s.limits.memory(other)
		}
	}
	if used > s.limits.MemoryBudget {
		return ErrBusy
	}
	return nil
}

// holdsMemory is whether a room takes its share of the budget: its container
// runs, or there is none yet and the room is about to start one.
func (s *Service) holdsMemory(ctx context.Context, n docker.Network, id string) (bool, error) {
	for _, c := range n.Containers {
		if c.Name == containerName(id) {
			return true, nil
		}
	}
	running, err := s.engine.ContainerRunning(ctx, containerName(id))
	if docker.IsNotFound(err) {
		return true, nil
	}
	return running, err
}

// open joins sandboxd to the room's network and listens on its address there.
func (s *Service) open(ctx context.Context, id string, sp Spec) error {
	self, err := s.selfContainer(ctx)
	if err != nil {
		return err
	}
	if err := s.engine.ConnectNetwork(ctx, networkName(id), self); err != nil {
		return err
	}
	n, err := s.engine.InspectNetwork(ctx, networkName(id))
	if err != nil {
		return err
	}
	m, ok := n.Containers[self]
	if !ok {
		return errors.New("sandboxd is not on the room's network")
	}
	addr, err := netip.ParseAddr(m.IPv4)
	if err != nil {
		return fmt.Errorf("sandboxd's address on the room's network: %w", err)
	}
	if public := sp.Network == NetworkPublic; public || len(sp.Egress) > 0 {
		if err := s.egress.Open(id, addr, sp.Egress, public); err != nil {
			if errors.Is(err, guard.ErrUnavailable) {
				return fmt.Errorf("%w: %w", ErrRefused, err)
			}
			return err
		}
	}
	return nil
}

func (s *Service) selfContainer(ctx context.Context) (string, error) {
	if s.selfID != "" {
		return s.selfID, nil
	}
	id, err := s.engine.ContainerID(ctx, s.self)
	if err != nil {
		return "", fmt.Errorf("finding sandboxd's own container %q: %w", s.self, err)
	}
	s.selfID = id
	return id, nil
}

// Reconcile opens the guard again for every room the daemon holds; run it at
// start, so a restart of sandboxd does not leave rooms with no way out.
func (s *Service) Reconcile(ctx context.Context) error {
	nets, err := s.engine.ListNetworks(ctx, roomLabel)
	if err != nil {
		return err
	}
	for _, n := range nets {
		id := n.Labels[roomLabel]
		sp, err := specOf(n)
		if err != nil {
			s.logger.Error("a room's spec is unreadable", "room", id, "err", err.Error())
			continue
		}
		if err := s.open(ctx, id, sp); err != nil {
			s.logger.Error("reopening a room's egress", "room", id, "err", err.Error())
		}
	}
	return nil
}

func specOf(n docker.Network) (Spec, error) {
	var sp Spec
	err := json.Unmarshal([]byte(n.Labels[specLabel]), &sp)
	return sp, err
}

func (s *Service) room(ctx context.Context, id string) (docker.Network, Spec, error) {
	if !ValidID(id) {
		return docker.Network{}, Spec{}, ErrNotFound
	}
	n, err := s.engine.InspectNetwork(ctx, networkName(id))
	if docker.IsNotFound(err) {
		return n, Spec{}, ErrNotFound
	}
	if err != nil {
		return n, Spec{}, err
	}
	if n.Labels[roomLabel] != id {
		return n, Spec{}, ErrNotFound
	}
	sp, err := specOf(n)
	if err != nil {
		return n, sp, fmt.Errorf("%w: %w", errBadSpec, err)
	}
	return n, sp, nil
}

// Start runs argv as the room's only process.
func (s *Service) Start(ctx context.Context, id string, argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("%w: argv is empty", ErrRefused)
	}
	n, sp, err := s.room(ctx, id)
	if err != nil {
		return err
	}
	self, err := s.selfContainer(ctx)
	if err != nil {
		return err
	}
	m, ok := n.Containers[self]
	if !ok {
		return errors.New("sandboxd is not on the room's network")
	}
	// A container of the room's, running or stopped, is a start already made.
	switch _, err := s.engine.ContainerRunning(ctx, containerName(id)); {
	case err == nil:
		return ErrStarted
	case !docker.IsNotFound(err):
		return err
	}
	// The room's spec is on the daemon, so it outlives a change to the allowed
	// images: check again here.
	if err := s.allow(sp.Image); err != nil {
		return err
	}
	if err := s.ensureImage(ctx, sp.Image); err != nil {
		return err
	}
	if sp.Workspace {
		if err := s.workspaceVolume(ctx, id, sp); err != nil {
			return err
		}
	}
	ct := Harden(id, sp, argv, m.IPv4, s.limits)
	if _, err := s.engine.CreateContainer(ctx, containerName(id), ct); err != nil {
		s.dropVolume(ctx, id, sp)
		return err
	}
	if err := s.engine.StartContainer(ctx, containerName(id)); err != nil {
		_ = s.engine.RemoveContainer(ctx, containerName(id))
		s.dropVolume(ctx, id, sp)
		return err
	}
	return nil
}

// workspaceVolume makes the room's /workspace, unless the workspaces' disk
// is spent: a new one may hold up to WorkspaceBytes. When it is spent, the
// stopped workspaces are deleted, the one stopped longest first and no more
// than it takes, to make room; a running room is never touched. Sandboxd does
// not know teams, so a stopped workspace of any team may be the one deleted.
// Its room is then not found, and the brain replaces it on the next resume.
//
// s.mu is held from the measure to the volume's creation, so one start at a
// time measures, evicts and creates, and two cannot pick the same victim or
// delete a room another is creating. It does not promise the disk never
// passes the budget: a new volume measures about nothing until it is written
// to. It is released before the container is made.
func (s *Service) workspaceVolume(ctx context.Context, id string, sp Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sizes, err := s.engine.VolumeSizes(ctx, roomLabel)
	if err != nil {
		return err
	}
	_, _, disk := s.limits.workspaceSize(sp)
	if used := disk + total(sizes); used > s.limits.DiskBudget {
		plan, ok, err := s.evictionPlan(ctx, id, sizes, used-s.limits.DiskBudget)
		if err != nil {
			return err
		}
		if !ok {
			return s.diskSpent(id, len(sizes), used)
		}
		s.evict(ctx, plan)
		// teardown only logs what it could not remove, so measure again.
		sizes, err = s.engine.VolumeSizes(ctx, roomLabel)
		if err != nil {
			return err
		}
		if used := disk + total(sizes); used > s.limits.DiskBudget {
			return s.diskSpent(id, len(sizes), used)
		}
	}
	return s.engine.CreateVolume(ctx, volumeName(id), map[string]string{roomLabel: id})
}

func total(sizes map[string]int64) int64 {
	var n int64
	for _, b := range sizes {
		n += b
	}
	return n
}

// diskSpent is the refusal of a workspace the disk budget has no room for.
func (s *Service) diskSpent(id string, volumes int, used int64) error {
	s.logger.Warn("a workspace was refused: the workspaces' disk is spent",
		"room", id, "volumes", volumes, "used_bytes", used, "budget_bytes", s.limits.DiskBudget)
	// The numbers are the operator's, so they are logged, not told to the
	// team's agent.
	return fmt.Errorf("%w: the workspaces' disk is spent, "+
		"and deleting stopped workspaces could not free enough", ErrBusy)
}

// victim is a stopped workspace that may be deleted for its disk.
type victim struct {
	id      string
	bytes   int64
	stopped time.Time
}

// evictionPlan picks the stopped workspaces to delete to free gap bytes: the
// ones stopped longest, as few as cover it. ok is false when every one of
// them together would not, and then nothing should be deleted. A room is a
// candidate only if it holds disk and its container exists, is stopped, and
// ran: a volume with no container belongs to a start under way (or to
// nothing sandboxd made), and a container that never ran has no stop time to
// order it by. The room being started is never one. The caller holds s.mu,
// which is what keeps a candidate from being resumed before it is deleted.
func (s *Service) evictionPlan(ctx context.Context, self string, sizes map[string]int64, gap int64) (plan []victim, ok bool, err error) {
	var all []victim
	for name, n := range sizes {
		id, found := strings.CutPrefix(name, networkPrefix)
		if !found || !ValidID(id) || id == self || n <= 0 {
			continue
		}
		running, finished, err := s.engine.ContainerState(ctx, containerName(id))
		if docker.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("inspecting room %s: %w", id, err)
		}
		if running || finished.IsZero() {
			continue
		}
		all = append(all, victim{id: id, bytes: n, stopped: finished})
	}
	// Ties go to the lower id so the choice is the same every time.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].stopped.Equal(all[j].stopped) {
			return all[i].stopped.Before(all[j].stopped)
		}
		return all[i].id < all[j].id
	})
	var freed int64
	for _, v := range all {
		plan = append(plan, v)
		if freed += v.bytes; freed >= gap {
			return plan, true, nil
		}
	}
	return nil, false, nil
}

// evictTimeout bounds the deletes of one plan. s.mu is held while they run,
// and they do not follow the caller's context, so a daemon that hangs on one
// would otherwise hold every create and resume for as long as it hangs.
const evictTimeout = 2 * time.Minute

// evict deletes the planned rooms. The caller giving up must not leave one
// half deleted, so the deletes do not follow its context, only evictTimeout.
func (s *Service) evict(ctx context.Context, plan []victim) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evictTimeout)
	defer cancel()
	for _, v := range plan {
		s.logger.Warn("deleting a stopped workspace to make room for a new one",
			"room", v.id, "bytes", v.bytes, "stopped_at", v.stopped.UTC().Format(time.RFC3339))
		s.teardown(ctx, v.id)
	}
}

func (s *Service) dropVolume(ctx context.Context, id string, sp Spec) {
	if !sp.Workspace {
		return
	}
	if err := s.engine.RemoveVolume(ctx, volumeName(id)); err != nil && !docker.IsNotFound(err) {
		s.logger.Error("removing a room's workspace", "room", id, "err", err.Error())
	}
}

// Stop stops the room's container, which gives its memory back to the
// budget; a workspace's files stay on its volume. The guard stays open, and
// the room keeps its network, so Resume is only a start.
func (s *Service) Stop(ctx context.Context, id string) error {
	if _, _, err := s.room(ctx, id); err != nil {
		return err
	}
	err := s.engine.StopContainer(ctx, containerName(id), stopGrace)
	if docker.IsNotFound(err) {
		return ErrNotStarted
	}
	return err
}

// Resume starts a stopped room's container again, if the budget has room
// for it and its workspace holds no more than it may. A room already
// running is not an error.
func (s *Service) Resume(ctx context.Context, id string) error {
	_, sp, err := s.room(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running, err := s.engine.ContainerRunning(ctx, containerName(id))
	switch {
	case docker.IsNotFound(err):
		return ErrNotStarted
	case err != nil:
		return err
	case running:
		return nil
	}
	if sp.Workspace {
		sizes, err := s.engine.VolumeSizes(ctx, roomLabel)
		if err != nil {
			return err
		}
		if _, _, disk := s.limits.workspaceSize(sp); sizes[volumeName(id)] > disk {
			return fmt.Errorf("%w: %d MiB of %d", ErrOverDisk, sizes[volumeName(id)]>>20, disk>>20)
		}
	}
	if err := s.fits(ctx, id, sp); err != nil {
		return err
	}
	return s.engine.StartContainer(ctx, containerName(id))
}

// Watch measures the workspaces every WatchEvery until ctx ends, and stops
// each room whose workspace holds more than WorkspaceBytes. It is the soft
// half of the disk cap: a room can pass it for up to one interval, and
// Resume refuses it after.
func (s *Service) Watch(ctx context.Context) {
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.measure(ctx)
		}
	}
}

func (s *Service) measure(ctx context.Context) {
	sizes, err := s.engine.VolumeSizes(ctx, roomLabel)
	if err != nil {
		s.logger.Error("measuring the workspaces", "err", err.Error())
		return
	}
	for name, n := range sizes {
		id := strings.TrimPrefix(name, networkPrefix)
		disk := s.limits.WorkspaceBytes
		if _, sp, err := s.room(ctx, id); err == nil {
			_, _, disk = s.limits.workspaceSize(sp)
		}
		if n <= disk {
			continue
		}
		err := s.engine.StopContainer(ctx, containerName(id), stopGrace)
		if err != nil && !docker.IsNotFound(err) {
			s.logger.Error("stopping a workspace over its disk", "room", id, "err", err.Error())
			continue
		}
		s.logger.Warn("stopped a workspace over its disk", "room", id, "bytes", n, "limit", disk)
	}
}

// Target is where a port of the room is reached from sandboxd.
func (s *Service) Target(ctx context.Context, id string, port int) (string, error) {
	n, _, err := s.room(ctx, id)
	if err != nil {
		return "", err
	}
	for _, c := range n.Containers {
		if c.Name == containerName(id) {
			return net.JoinHostPort(c.IPv4, strconv.Itoa(port)), nil
		}
	}
	return "", ErrNotStarted
}

// ErrNotStarted is a room with no container yet.
var ErrNotStarted = errors.New("the room has not started")

// Exists reports whether the room is there.
func (s *Service) Exists(ctx context.Context, id string) error {
	_, _, err := s.room(ctx, id)
	return err
}

// Delete removes the container, the network and the guard's listeners.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, _, err := s.room(ctx, id); err != nil && !errors.Is(err, errBadSpec) {
		return err
	}
	s.teardown(ctx, id)
	return nil
}

var errBadSpec = errors.New("unreadable spec")

func (s *Service) teardown(ctx context.Context, id string) {
	s.egress.Close(id)
	if err := s.engine.RemoveContainer(ctx, containerName(id)); err != nil && !docker.IsNotFound(err) {
		s.logger.Error("removing a room's container", "room", id, "err", err.Error())
	}
	if self, err := s.selfContainer(ctx); err == nil {
		if err := s.engine.DisconnectNetwork(ctx, networkName(id), self); err != nil && !docker.IsNotFound(err) {
			s.logger.Warn("leaving a room's network", "room", id, "err", err.Error())
		}
	}
	if err := s.engine.RemoveNetwork(ctx, networkName(id)); err != nil && !docker.IsNotFound(err) {
		s.logger.Error("removing a room's network", "room", id, "err", err.Error())
	}
	// Every room is tried: a server's has no volume, which is not found.
	if err := s.engine.RemoveVolume(ctx, volumeName(id)); err != nil && !docker.IsNotFound(err) {
		s.logger.Error("removing a room's workspace", "room", id, "err", err.Error())
	}
}

// Harden builds a room's container. Everything in it is fixed here except the
// image, the env, the labels and the egress hosts, which come from the spec;
// a request cannot add a mount, a capability, a network, privilege or a user.
func Harden(id string, sp Spec, argv []string, selfIP string, l Limits) docker.Container {
	env := []string{"HOME=" + HomeDir}
	keys := make([]string, 0, len(sp.Env))
	for k := range sp.Env {
		if k != "HOME" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+sp.Env[k])
	}

	labels := map[string]string{roomLabel: id}
	for k, v := range sp.Labels {
		labels[userLabelPrefix+k] = v
	}

	var hosts []string
	seen := map[string]bool{}
	for _, h := range sp.Egress {
		n := strings.ToLower(h.Name)
		if !seen[n] {
			seen[n] = true
			hosts = append(hosts, n+":"+selfIP)
		}
	}

	// Docker mounts a tmpfs noexec unless told otherwise. /tmp keeps that.
	// The home directory is where npx and uvx unpack what they fetch and then
	// run it, so it is the one place a room may execute from.
	tmp := ",size=" + strconv.FormatInt(l.TmpBytes, 10)
	home := ",size=" + strconv.FormatInt(l.HomeBytes, 10)
	tmpfs := map[string]string{
		"/tmp":  "rw,noexec,nosuid,nodev" + tmp,
		HomeDir: "rw,exec,nosuid,nodev" + home + ",uid=65534,gid=65534,mode=0700",
	}
	memory, cpus := l.Memory, l.NanoCPUs
	var volumes map[string]string
	if sp.Workspace {
		// An agent builds and runs what it writes, so its workspace executes:
		// a volume is mounted exec. It is on disk, so a stopped room keeps it.
		volumes = map[string]string{WorkspaceDir: volumeName(id)}
		memory, cpus, _ = l.workspaceSize(sp)
	}
	if sp.Network == NetworkPublic {
		// The room has no route and no DNS: every client that honours these
		// goes through the guard's proxy, which resolves and checks for it.
		proxy := "http://" + net.JoinHostPort(selfIP, strconv.Itoa(guard.ProxyPort))
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			env = append(env, k+"="+proxy)
		}
		env = append(env, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1")
	}
	return docker.Container{
		Image:      sp.Image,
		Entrypoint: argv,
		Env:        env,
		Labels:     labels,
		User:       NobodyUser,
		HostConfig: docker.HostConfig{
			ReadonlyRootfs: true,
			Privileged:     false,
			Tmpfs:          tmpfs,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			PidsLimit:      l.PidsLimit,
			Memory:         memory,
			MemorySwap:     memory,
			NanoCPUs:       cpus,
			NetworkMode:    networkName(id),
			ExtraHosts:     hosts,
			RestartPolicy:  docker.RestartPolicy{Name: "no"},
			Mounts:         volumes,
		},
	}
}

func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
