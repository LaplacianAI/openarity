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

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/guard"
)

var (
	// ErrNotFound is a room the daemon does not hold.
	ErrNotFound = errors.New("no such room")
	// ErrBusy is a create refused because the memory budget is spent.
	ErrBusy = errors.New("the sandbox is busy")
	// ErrRefused is a spec sandboxd cannot honour. The error wrapping it says why.
	ErrRefused = errors.New("the spec cannot be honoured")
	// ErrStarted is a start of a room that has already started.
	ErrStarted = errors.New("the room has already started")
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
	// NobodyUser is the user every room runs as.
	NobodyUser = "65534"
)

// Limits are what each room gets, and what all of them together may take.
type Limits struct {
	Memory       int64 // bytes per room
	PidsLimit    int64
	NanoCPUs     int64
	MemoryBudget int64 // bytes, across rooms
	TmpfsBytes   int64
}

// DefaultLimits are 256 MiB, 256 pids, half a CPU, 64 MiB tmpfs, 3 GiB in all.
func DefaultLimits() Limits {
	return Limits{Memory: 256 << 20, PidsLimit: 256, NanoCPUs: 500_000_000, MemoryBudget: 3 << 30, TmpfsBytes: 64 << 20}
}

// Spec is what a request may say about a room: nothing else reaches the
// container.
type Spec struct {
	Image  string            `json:"image"`
	Env    map[string]string `json:"env"`
	Egress []guard.Host      `json:"egress"`
	Labels map[string]string `json:"labels"`
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
	RemoveContainer(ctx context.Context, name string) error
	ContainerID(ctx context.Context, nameOrID string) (string, error)
}

// Egress is the guard.
type Egress interface {
	Open(id string, bind netip.Addr, hosts []guard.Host) error
	Close(id string)
}

// Service is the rooms of one daemon.
type Service struct {
	engine Engine
	egress Egress
	logger *slog.Logger
	limits Limits
	self   string // sandboxd's own container
	key    []byte // derives each room's proxy token

	mu     sync.Mutex // serialises the budget check with the create
	selfID string
}

// New returns a service. self names sandboxd's own container, which joins
// every room's network.
func New(e Engine, g Egress, logger *slog.Logger, l Limits, self string, key []byte) *Service {
	if logger == nil {
		panic("rooms: a nil logger")
	}
	return &Service{engine: e, egress: g, logger: logger, limits: l, self: self, key: key}
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
	return nil
}

// Create makes the room's network, joins sandboxd to it, and opens the guard.
func (s *Service) Create(ctx context.Context, sp Spec) (string, error) {
	if err := Validate(sp); err != nil {
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
	nets, err := s.engine.ListNetworks(ctx, roomLabel)
	if err != nil {
		return "", err
	}
	if int64(len(nets)+1)*s.limits.Memory > s.limits.MemoryBudget {
		return "", ErrBusy
	}
	if err := s.engine.CreateNetwork(ctx, networkName(id), map[string]string{roomLabel: id, specLabel: string(spec)}); err != nil {
		return "", err
	}
	if err := s.open(ctx, id, sp.Egress); err != nil {
		s.teardown(ctx, id)
		return "", err
	}
	return id, nil
}

// open joins sandboxd to the room's network and listens on its address there.
func (s *Service) open(ctx context.Context, id string, egress []guard.Host) error {
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
	if len(egress) > 0 {
		if err := s.egress.Open(id, addr, egress); err != nil {
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
		if err := s.open(ctx, id, sp.Egress); err != nil {
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
	for _, c := range n.Containers {
		if c.Name == containerName(id) {
			return ErrStarted
		}
	}
	ct := Harden(id, sp, argv, m.IPv4, s.limits)
	if _, err := s.engine.CreateContainer(ctx, containerName(id), ct); err != nil {
		return err
	}
	if err := s.engine.StartContainer(ctx, containerName(id)); err != nil {
		_ = s.engine.RemoveContainer(ctx, containerName(id))
		return err
	}
	return nil
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

	tmpfs := "rw,nosuid,nodev,size=" + strconv.FormatInt(l.TmpfsBytes, 10)
	return docker.Container{
		Image:      sp.Image,
		Entrypoint: argv,
		Env:        env,
		Labels:     labels,
		User:       NobodyUser,
		HostConfig: docker.HostConfig{
			ReadonlyRootfs: true,
			Privileged:     false,
			Tmpfs: map[string]string{
				"/tmp":  tmpfs,
				HomeDir: tmpfs + ",uid=65534,gid=65534,mode=0700",
			},
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			PidsLimit:     l.PidsLimit,
			Memory:        l.Memory,
			MemorySwap:    l.Memory,
			NanoCPUs:      l.NanoCPUs,
			NetworkMode:   networkName(id),
			ExtraHosts:    hosts,
			RestartPolicy: docker.RestartPolicy{Name: "no"},
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
