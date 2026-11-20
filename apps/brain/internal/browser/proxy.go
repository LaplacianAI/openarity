package browser

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

const (
	proxyDialTimeout   = 10 * time.Second
	proxyHeaderTimeout = 10 * time.Second
	// proxyIdleTimeout is how long a browser's kept-alive connection to the
	// proxy may sit between requests before it is closed.
	proxyIdleTimeout = 2 * time.Minute
	// proxyConnections is how many requests and tunnels the proxy holds at
	// once, for every browser on its network together: past it a browser is
	// refused, never queued.
	proxyConnections = 512
	// tunnelIdleTimeout is how long a tunnel may carry nothing either way
	// before both its ends are closed, so a stalled one gives its slot back.
	tunnelIdleTimeout = 5 * time.Minute

	upstreamResponseHeaderTimeout = time.Minute
	upstreamIdleConnTimeout       = 90 * time.Second
	upstreamMaxIdleConns          = 32
	upstreamMaxIdleConnsPerHost   = 4
)

// A Proxy is the only way out for a browser of the team's own: a forward
// proxy on the browser's network, which has no other route. It dials public
// addresses only, on a web port, and decides on the address it connects to,
// so a name that answers a public address to the guard and a private one to
// the browser reaches nothing: the browser never looks it up. What the guard
// cannot pause, a worker's socket, a preconnect, a frame from another site,
// goes through here too.
type Proxy struct {
	dialer  *net.Dialer
	forward *httputil.ReverseProxy
	slots   chan struct{}
	logger  *slog.Logger
	// port is the destination ports dialed, webPort but in tests.
	port func(uint16) bool
	// idle is tunnelIdleTimeout but in tests.
	idle time.Duration
}

// NewProxy is a proxy that dials what allow allows, on a web port, and never
// a link-local address.
func NewProxy(allow func(netip.Addr) bool, logger *slog.Logger) *Proxy {
	p := &Proxy{slots: make(chan struct{}, proxyConnections), logger: logger, port: webPort, idle: tunnelIdleTimeout}
	p.dialer = &net.Dialer{
		Timeout: proxyDialTimeout,
		// Control runs on each address a name resolved to, just before it is
		// connected to: the address checked is the one used.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			switch {
			case err != nil || !reachable(ap.Addr(), allow):
				return errRefused
			case !p.port(ap.Port()):
				return errPort
			}
			return nil
		},
	}
	p.forward = &httputil.ReverseProxy{
		// The request is already absolute. With Rewrite, ReverseProxy strips
		// the X-Forwarded-* headers it was sent and the hop-by-hop ones
		// (Proxy-Authorization among them) before this runs, and adds no
		// X-Forwarded-For of its own: nothing of the browser's network goes
		// on.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL = r.In.URL
			r.Out.Host = r.In.Host
		},
		Transport: &http.Transport{
			DialContext:           p.dialer.DialContext,
			ResponseHeaderTimeout: upstreamResponseHeaderTimeout,
			IdleConnTimeout:       upstreamIdleConnTimeout,
			MaxIdleConns:          upstreamMaxIdleConns,
			MaxIdleConnsPerHost:   upstreamMaxIdleConnsPerHost,
			// The browser's own Accept-Encoding is passed on, and what comes
			// back is handed it as sent.
			DisableCompression: true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.refuse(w, r.URL.Host, err)
		},
	}
	return p
}

var (
	errRefused = errors.New("not a public address")
	errPort    = errors.New("not a web port")
)

// webPort is a port a site is served on: 80 and 443, and the 8080 and 8443
// of many a staging one. Any other is refused, so the proxy is no relay for
// mail, SSH or a scan of the internet from the host's address.
func webPort(port uint16) bool {
	switch port {
	case 80, 443, 8080, 8443:
		return true
	}
	return false
}

// reachable is an address the proxy may dial. Link-local is refused here as
// well as by allow, whatever allow says: it is where a cloud's metadata
// lives, and a looser allow, a test's or a later one's, must not reach it.
func reachable(a netip.Addr, allow func(netip.Addr) bool) bool {
	a = a.Unmap()
	if a.Zone() != "" || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return false
	}
	return allow(a)
}

// ServeHTTP proxies a CONNECT or an absolute http:// request, within the
// proxy's connections.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "the browser holds too many connections", http.StatusServiceUnavailable)
		return
	}
	switch {
	case r.Method == http.MethodConnect:
		p.tunnel(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http":
		p.forward.ServeHTTP(w, r)
	default:
		http.Error(w, "only CONNECT and http:// requests are proxied", http.StatusBadRequest)
	}
}

// tunnel joins the browser to the address it named, once that is dialed.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	if _, _, err := net.SplitHostPort(r.Host); err != nil {
		http.Error(w, "CONNECT names a host and a port", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), proxyDialTimeout)
	out, err := p.dialer.DialContext(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		p.refuse(w, r.Host, err)
		return
	}
	defer func() { _ = out.Close() }()
	in, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "the connection could not be tunnelled", http.StatusInternalServerError)
		return
	}
	defer func() { _ = in.Close() }()
	if _, err := io.WriteString(in, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	// What the browser sent past the request line is the tunnel's first
	// bytes. The rest is read from the socket itself: net/http's reader
	// cancels the request at EOF, which would end the tunnel the moment a
	// browser half-closes it.
	if n := rw.Reader.Buffered(); n > 0 {
		first, _ := rw.Peek(n)
		if _, err := out.Write(first); err != nil {
			return
		}
	}

	// Both ends are closed once nothing has crossed either way for idle.
	idle := time.AfterFunc(p.idle, func() {
		_ = in.Close()
		_ = out.Close()
	})
	defer idle.Stop()
	var wg sync.WaitGroup
	pipe := func(dst net.Conn, src io.Reader) {
		defer wg.Done()
		if _, err := io.Copy(activity{dst, idle, p.idle}, src); err != nil {
			// Broken, not finished: neither end has more to say.
			_ = in.Close()
			_ = out.Close()
			return
		}
		// One side is done sending; the other may still answer, as an HTTP
		// client that half-closes after its request expects.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go pipe(out, in)
	go pipe(in, out)
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-r.Context().Done():
	}
}

// activity is a tunnel's end that puts off its idle close with every write.
type activity struct {
	net.Conn
	idle  *time.Timer
	after time.Duration
}

func (a activity) Write(b []byte) (int, error) {
	a.idle.Reset(a.after)
	return a.Conn.Write(b)
}

// refuse answers a request the proxy did not carry, and logs its host only:
// a path or a query may hold what the page should not have sent anywhere.
// The page is told nothing of why a dial failed, so it cannot ask the
// proxy which names exist on the worker's networks.
func (p *Proxy) refuse(w http.ResponseWriter, host string, err error) {
	switch {
	case errors.Is(err, errRefused):
		p.logger.Debug("The browser's proxy refused an address that is not public", "host", host)
		http.Error(w, "the browser may reach public addresses only", http.StatusForbidden)
	case errors.Is(err, errPort):
		p.logger.Debug("The browser's proxy refused a port that is not a web one", "host", host)
		http.Error(w, "the browser may reach ports 80, 443, 8080 and 8443 only", http.StatusForbidden)
	default:
		p.logger.Warn("The browser's proxy could not reach a host", "host", host, "error", err)
		http.Error(w, "could not be reached", http.StatusBadGateway)
	}
}

// Serve runs p on l until ctx ends, or until l fails.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	// Tunnels outlive the server's own close, so they end with ctx.
	srv := &http.Server{
		Handler: p, ReadHeaderTimeout: proxyHeaderTimeout, IdleTimeout: proxyIdleTimeout,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = srv.Close()
	}()
	p.logger.Info("The browser's proxy is listening", "addr", l.Addr().String())
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
