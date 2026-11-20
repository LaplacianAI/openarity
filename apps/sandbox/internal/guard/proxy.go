package guard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

// ProxyPort is where a public room's proxy listens, on sandboxd's address in
// the room's network: 3128, as Squid's, so a tool that guesses finds it.
const ProxyPort = 3128

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
			switch {
			case req.Method == http.MethodConnect:
				g.tunnel(r, w, req)
			case req.URL.IsAbs() && req.URL.Scheme == "http":
				forward.ServeHTTP(w, req)
			default:
				http.Error(w, "this is the sandbox's proxy: send an absolute http:// URL, or CONNECT for anything else", http.StatusBadRequest)
			}
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
	if _, _, err := net.SplitHostPort(req.Host); err != nil {
		http.Error(w, "CONNECT needs a host and a port", http.StatusBadRequest)
		return
	}
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
	relay(c, up)
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
