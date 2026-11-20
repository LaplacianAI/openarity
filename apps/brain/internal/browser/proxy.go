package browser

import (
	"context"
	"errors"
	"fmt"
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
	// proxyConnections is how many a browser may hold at once: a page's
	// sockets and a tunnel per connection it keeps open.
	proxyConnections = 512
)

// A Proxy is the only way out for a browser of the team's own: a forward
// proxy on the browser's network, which has no other route. It dials public
// addresses only, and decides on the address it connects to, so a name that
// answers a public address to the guard and a private one to the browser
// reaches nothing: the browser never looks it up. What the guard cannot
// pause, a worker's socket, a preconnect, a frame from another site, goes
// through here too.
type Proxy struct {
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	forward *httputil.ReverseProxy
	slots   chan struct{}
	logger  *slog.Logger
}

// NewProxy is a proxy that dials what allow allows, and never a link-local
// address.
func NewProxy(allow func(netip.Addr) bool, logger *slog.Logger) *Proxy {
	d := &net.Dialer{
		Timeout: proxyDialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !reachable(ap.Addr(), allow) {
				return errRefused
			}
			return nil
		},
	}
	p := &Proxy{dial: d.DialContext, slots: make(chan struct{}, proxyConnections), logger: logger}
	p.forward = &httputil.ReverseProxy{
		// The request is already absolute; nothing of the browser is added,
		// and Rewrite leaves out X-Forwarded-For.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL = r.In.URL
			r.Out.Host = r.In.Host
		},
		Transport: &http.Transport{
			DialContext:           p.dial,
			ResponseHeaderTimeout: time.Minute,
			MaxIdleConnsPerHost:   4,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			refuse(w, err)
		},
	}
	return p
}

var errRefused = errors.New("not a public address")

func reachable(a netip.Addr, allow func(netip.Addr) bool) bool {
	a = a.Unmap()
	if a.Zone() != "" || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return false
	}
	return allow(a)
}

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
	ctx, cancel := context.WithTimeout(r.Context(), proxyDialTimeout)
	out, err := p.dial(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		refuse(w, err)
		return
	}
	defer func() { _ = out.Close() }()
	in, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = in.Close() }()
	if _, err := io.WriteString(in, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}

	var once sync.Once
	done := make(chan struct{})
	pipe := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		once.Do(func() { close(done) })
	}
	// What the browser sent past the request line is the tunnel's first bytes.
	go pipe(out, rw.Reader)
	go pipe(in, out)
	select {
	case <-done:
	case <-r.Context().Done():
	}
}

func refuse(w http.ResponseWriter, err error) {
	if errors.Is(err, errRefused) {
		http.Error(w, "the browser may reach public addresses only", http.StatusForbidden)
		return
	}
	http.Error(w, fmt.Sprintf("could not be reached: %v", err), http.StatusBadGateway)
}

// Serve runs p on l until ctx ends.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	// Tunnels outlive the server's own close, so they end with ctx.
	srv := &http.Server{Handler: p, ReadHeaderTimeout: proxyHeaderTimeout, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	p.logger.Info("the browser's proxy is listening", "addr", l.Addr().String())
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
