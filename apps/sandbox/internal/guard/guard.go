// Package guard is a room's only way out. A room's network is internal, and
// each host the room may reach resolves, in the room, to sandboxd's address on
// that network; a public room reaches the rest of the internet through the
// guard's proxy (proxy.go). The guard listens there, works out which listed host a
// connection is for, dials it, and relays the bytes. TLS stays end to end:
// the guard reads the server name from the ClientHello and nothing else.
package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A Host is one place a room may connect to.
type Host struct {
	Name string `json:"host"`
	Port int    `json:"port"`
}

// plaintextPorts are the ports whose protocol does not begin with a TLS
// handshake: plain HTTP, and mail and database protocols that start in the
// clear and upgrade. The server name cannot be read from them, so a port
// here serves one host only.
var plaintextPorts = map[int]bool{
	21: true, 25: true, 80: true, 110: true, 143: true, 587: true,
	3306: true, 5432: true, 6379: true,
}

// ErrUnavailable is a port the guard cannot listen on, as when it is the
// port sandboxd itself serves on: the spec cannot be honoured here.
var ErrUnavailable = errors.New("egress cannot listen on that port")

// MaxHosts bounds a room's list.
const MaxHosts = 32

// Validate refuses a list the guard cannot honour: a malformed entry, or two
// hosts on a port that cannot tell them apart.
func Validate(hosts []Host) error {
	if len(hosts) > MaxHosts {
		return fmt.Errorf("more than %d egress hosts", MaxHosts)
	}
	byPort := map[int]map[string]bool{}
	for _, h := range hosts {
		if err := validHost(h); err != nil {
			return err
		}
		if byPort[h.Port] == nil {
			byPort[h.Port] = map[string]bool{}
		}
		byPort[h.Port][strings.ToLower(h.Name)] = true
	}
	for port, names := range byPort {
		if len(names) > 1 && plaintextPorts[port] {
			return fmt.Errorf("port %d is not TLS from its first byte, so it can serve one host only", port)
		}
	}
	return nil
}

func validHost(h Host) error {
	if h.Port < 1 || h.Port > 65535 {
		return fmt.Errorf("egress port %d is not a port", h.Port)
	}
	if h.Name == "" || len(h.Name) > 253 {
		return errors.New("an egress host is empty or too long")
	}
	for _, r := range h.Name {
		ok := r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !ok {
			return fmt.Errorf("egress host %q is not a host name", h.Name)
		}
	}
	return nil
}

// A Guard holds the listeners of every room.
type Guard struct {
	logger *slog.Logger
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	// proxyPort is ProxyPort but in tests, which listen on loopback.
	proxyPort int
	// tunnelIdle is TunnelIdle but in tests.
	tunnelIdle time.Duration

	mu    sync.Mutex
	rooms map[string]*room
}

// New returns a guard that dials public addresses only.
func New(logger *slog.Logger) *Guard {
	if logger == nil {
		panic("guard: a nil logger")
	}
	d := &net.Dialer{Timeout: 10 * time.Second, Control: refusePrivate}
	return &Guard{logger: logger, dial: d.DialContext, proxyPort: ProxyPort, tunnelIdle: TunnelIdle, rooms: map[string]*room{}}
}

type room struct {
	id        string
	hosts     []Host
	listeners []net.Listener
	stopProxy func() // a public room's; nil otherwise

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	tunnels int // a public room's open tunnels
	done    bool
}

// Open listens, for each distinct port in hosts, on bind, and on ProxyPort
// when the room is public. Opening a room that is open replaces it.
func (g *Guard) Open(id string, bind netip.Addr, hosts []Host, public bool) error {
	if err := Validate(hosts); err != nil {
		return err
	}
	g.Close(id)
	r := &room{id: id, hosts: hosts, conns: map[net.Conn]struct{}{}}
	if public {
		addr := net.JoinHostPort(bind.String(), strconv.Itoa(g.proxyPort))
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			return fmt.Errorf("%w: the proxy's port %d: %w", ErrUnavailable, g.proxyPort, err)
		}
		r.stopProxy = g.serveProxy(r, l)
	}
	seen := map[int]bool{}
	for _, h := range hosts {
		if seen[h.Port] {
			continue
		}
		seen[h.Port] = true
		addr := net.JoinHostPort(bind.String(), strconv.Itoa(h.Port))
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			r.close()
			return fmt.Errorf("%w: port %d: %w", ErrUnavailable, h.Port, err)
		}
		r.listeners = append(r.listeners, l)
		go g.accept(r, l, h.Port)
	}
	g.mu.Lock()
	g.rooms[id] = r
	g.mu.Unlock()
	return nil
}

// Close stops a room's listeners and cuts its connections.
func (g *Guard) Close(id string) {
	g.mu.Lock()
	r := g.rooms[id]
	delete(g.rooms, id)
	g.mu.Unlock()
	if r != nil {
		r.close()
	}
}

func (r *room) close() {
	r.mu.Lock()
	r.done = true
	conns := r.conns
	r.conns = nil
	r.mu.Unlock()
	for _, l := range r.listeners {
		_ = l.Close()
	}
	if r.stopProxy != nil {
		r.stopProxy()
	}
	for c := range conns {
		_ = c.Close()
	}
}

// track registers a connection, or closes it if the room is already closed.
func (r *room) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		_ = c.Close()
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *room) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
	_ = c.Close()
}

func (g *Guard) accept(r *room, l net.Listener, port int) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		if !r.track(c) {
			continue
		}
		go func() {
			defer r.untrack(c)
			if err := g.serve(r, c, port); err != nil {
				g.logger.Info("egress refused", "room", r.id, "port", port, "reason", err.Error())
			}
		}()
	}
}

// hostsOn lists the names listed for a port.
func (r *room) hostsOn(port int) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range r.hosts {
		n := strings.ToLower(h.Name)
		if h.Port == port && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func (g *Guard) serve(r *room, c net.Conn, port int) error {
	names := r.hostsOn(port)
	var replay []byte
	target := ""
	switch len(names) {
	case 0:
		return errors.New("no host is listed for the port")
	case 1:
		target = names[0]
	default:
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		sni, read, err := peekSNI(c)
		_ = c.SetReadDeadline(time.Time{})
		if err != nil {
			return err
		}
		if !contains(names, sni) {
			return fmt.Errorf("%q is not a listed host", sni)
		}
		target, replay = sni, read
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	up, err := g.dial(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("dialing %s: %w", target, err)
	}
	if !r.track(up) {
		return errors.New("the room is gone")
	}
	defer r.untrack(up)
	if len(replay) > 0 {
		if _, err := up.Write(replay); err != nil {
			return err
		}
	}
	relay(c, up)
	return nil
}

// relay copies both ways until each side has finished.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go pipe(a, b)
	go pipe(b, a)
	wg.Wait()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ErrPrivate is a dial the guard refused for where it would land.
var ErrPrivate = errors.New("the address is private, loopback or link-local")

// refusePrivate runs for every address the dialer is about to connect to,
// after the name is resolved, so a host that resolves to an internal address
// (or is changed to one between a check and the dial) is refused.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrPrivate, address)
	}
	if isPrivate(ap.Addr()) {
		return ErrPrivate
	}
	return nil
}

var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), // reserved, and the broadcast address
	// IPv6 forms that carry an IPv4 address to a translator or a tunnel,
	// which may be a private one: NAT64, local-use NAT64, 6to4 and Teredo.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("fec0::/10"), // site-local, deprecated and still routed by some
	netip.MustParsePrefix("100::/64"),  // discard
}

func isPrivate(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
