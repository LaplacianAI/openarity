package guard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"
)

// ProxyPort is where a public room's proxy listens, on sandboxd's address in
// the room's network: 3128, as Squid's, so a tool that guesses finds it.
const ProxyPort = 3128

const (
	// MaxTunnels is how many connections one public room holds out at once,
	// its tunnels and its forwarded requests together:
	// enough for a package manager's parallel downloads, and a bound on what
	// one room takes of sandboxd's sockets.
	MaxTunnels = 64
	// TunnelIdle is how long a tunnel may carry nothing before it is cut,
	// so one left open by a command that died holds nothing for long.
	TunnelIdle = 5 * time.Minute
)

// mailPorts are SMTP's: a room that sent mail would send it from sandboxd's
// address, whose reputation is the operator's. Mail can still leave on any
// other port a server listens on; this keeps it off the ones it goes by.
var mailPorts = map[int]bool{25: true, 465: true, 587: true}

// destinationPort is the port a request is for, as a number: the dialer
// takes "025" and a service name such as "smtp" as well, so a port compared
// as written is no check at all. A request with neither a port nor a number
// for one is refused.
func destinationPort(req *http.Request) (int, error) {
	hostport := req.Host
	if req.Method != http.MethodConnect {
		if req.URL.Port() == "" {
			return 80, nil
		}
		hostport = req.URL.Host
	}
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return 0, errors.New("the request names no host and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return 0, errors.New("the port is not a number from 1 to 65535: " + port)
	}
	return n, nil
}

// A public room reaches the internet, and nothing private, through a forward
// proxy: CONNECT for TLS and every other protocol that tunnels, and an
// absolute URL for plain HTTP. The room has no route and no DNS of its own,
// so the proxy resolves every name and the dialer refuses where it lands, as
// for a listed host. A client that ignores HTTP(S)_PROXY reaches nothing,
// which is a refusal, never a leak.
//
// It returns what stops it: the listener, every request in flight with the
// connections the client made, and the idle ones upstream. A tunnel is
// tracked by the room, which cuts it.
func (g *Guard) serveProxy(r *room, l net.Listener) (stop func()) {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
	}
	// The request goes on as it came; ReverseProxy drops the hop-by-hop
	// headers, Proxy-Authorization among them, and adds no X-Forwarded-For.
	forward := &httputil.ReverseProxy{
		Rewrite:   func(pr *httputil.ProxyRequest) { pr.Out.Host = pr.In.Host },
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			g.refused(r, req.Host, err)
			http.Error(w, "the sandbox could not reach "+req.Host+": "+reason(err), http.StatusBadGateway)
		},
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodConnect && !(req.URL.IsAbs() && req.URL.Scheme == "http") {
				http.Error(w, "this is the sandbox's proxy: send an absolute http:// URL, or CONNECT for anything else", http.StatusBadRequest)
				return
			}
			port, err := destinationPort(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if mailPorts[port] {
				g.refused(r, req.Host, errors.New("a mail port"))
				http.Error(w, "the sandbox does not send mail: port "+strconv.Itoa(port)+" is refused", http.StatusForbidden)
				return
			}
			// A tunnel and a forwarded request alike hold a connection out,
			// so both count against the room's.
			if !r.openTunnel() {
				http.Error(w, "the sandbox has "+strconv.Itoa(MaxTunnels)+" connections open; close some first", http.StatusTooManyRequests)
				return
			}
			defer r.closeTunnel()
			if req.Method == http.MethodConnect {
				g.tunnel(r, w, req)
				return
			}
			forward.ServeHTTP(w, req)
		}),
	}
	go func() { _ = srv.Serve(l) }()
	return func() {
		_ = srv.Close()
		transport.CloseIdleConnections()
	}
}

// tunnel dials the CONNECT's host and relays both ways.
func (g *Guard) tunnel(r *room, w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
	up, err := g.dialFor(r)(ctx, "tcp", req.Host)
	cancel()
	if err != nil {
		g.refused(r, req.Host, err)
		http.Error(w, "the sandbox could not reach "+req.Host+": "+reason(err), http.StatusBadGateway)
		return
	}
	defer r.untrack(up)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "the proxy cannot tunnel", http.StatusInternalServerError)
		return
	}
	c, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	if !r.track(c) {
		return
	}
	defer r.untrack(c)
	if _, err := c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	// What the client sent after the CONNECT, before the answer, is the
	// start of the tunnel.
	if n := buf.Reader.Buffered(); n > 0 {
		early, _ := buf.Peek(n)
		if _, err := up.Write(early); err != nil {
			return
		}
	}
	relay(idle(c, g.tunnelIdle), idle(up, g.tunnelIdle))
}

func (r *room) openTunnel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tunnels >= MaxTunnels {
		return false
	}
	r.tunnels++
	return true
}

func (r *room) closeTunnel() {
	r.mu.Lock()
	r.tunnels--
	r.mu.Unlock()
}

// idleConn is one side of a tunnel, whose reads and writes must each come
// within d of its last. A side that only receives is written to, so a
// download keeps both sides open, and a tunnel carrying nothing is cut.
type idleConn struct {
	net.Conn
	d time.Duration
}

func idle(c net.Conn, d time.Duration) *idleConn {
	_ = c.SetDeadline(time.Now().Add(d))
	return &idleConn{Conn: c, d: d}
}

func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		_ = c.SetDeadline(time.Now().Add(c.d))
	}
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		_ = c.SetDeadline(time.Now().Add(c.d))
	}
	return n, err
}

// CloseWrite half-closes the connection under it, as relay expects of one
// that can.
func (c *idleConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// dialFor is the guard's dialer for a tunnel, whose connection the room cuts
// when it closes; the caller untracks it when the tunnel ends.
func (g *Guard) dialFor(r *room) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := g.dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if !r.track(c) {
			return nil, errors.New("the room is gone")
		}
		return c, nil
	}
}

func (g *Guard) refused(r *room, host string, err error) {
	g.logger.Info("egress refused", "room", r.id, "host", host, "reason", err.Error())
}

// reason is why a dial failed, in words for whoever is in the room.
func reason(err error) string {
	if errors.Is(err, ErrPrivate) {
		return "it is a private address, which a sandbox may not reach"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "the name does not resolve"
	}
	return "the connection failed"
}
