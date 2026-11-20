package guard

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// clientHello is the first bytes a real TLS client sends for name.
func clientHello(t *testing.T, name string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := b.Read(buf)
		got <- buf[:n]
		_ = b.Close()
	}()
	cfg := &tls.Config{ServerName: name, InsecureSkipVerify: true} //nolint:gosec // only the hello is wanted
	if name == "" {
		cfg.ServerName = ""
	}
	_ = tls.Client(a, cfg).HandshakeContext(t.Context())
	return <-got
}

func TestPeekSNIReadsTheNameAndKeepsEveryByte(t *testing.T) {
	t.Parallel()
	hello := clientHello(t, "IMAP.Example.com")
	name, read, err := peekSNI(bytes.NewReader(append(append([]byte{}, hello...), "tail"...)))
	if err != nil || name != "imap.example.com" {
		t.Fatalf("name = %q, %v", name, err)
	}
	if !bytes.Equal(read, hello) {
		t.Fatal("the bytes handed back are not the bytes read: the server would see a different hello")
	}
}

func TestPeekSNIRefusesWhatIsNotTLS(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"EHLO example.com\r\n", "GET / HTTP/1.1\r\n\r\n", "\x16\x03\x01\x00\x00", "\x16\x03\x01\x00\x10short"} {
		if _, _, err := peekSNI(strings.NewReader(in)); err == nil {
			t.Errorf("%q was read as a hello", in)
		}
	}
}

func TestPeekSNIRefusesAHelloWithNoName(t *testing.T) {
	t.Parallel()
	// crypto/tls sends no server_name for an IP literal.
	hello := clientHello(t, "")
	if _, _, err := peekSNI(bytes.NewReader(hello)); !errors.Is(err, errNoSNI) {
		t.Fatalf("err = %v", err)
	}
}

func TestPeekSNINeverPanicsOnTruncatedInput(t *testing.T) {
	t.Parallel()
	hello := clientHello(t, "a.example.com")
	for i := range hello {
		_, _, _ = peekSNI(bytes.NewReader(hello[:i]))
		// and a record whose body is cut short but whose header is whole
		if i > 5 {
			_, _ = parseClientHello(hello[5:i])
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	ok := [][]Host{
		nil,
		{{"imap.example.com", 993}, {"smtp.example.com", 465}},
		{{"a.example.com", 443}, {"b.example.com", 443}}, // TLS: told apart by name
		{{"mail.example.com", 587}},                      // one STARTTLS host is fine
		{{"a.example.com", 443}, {"A.example.com", 443}},
	}
	for _, h := range ok {
		if err := Validate(h); err != nil {
			t.Errorf("Validate(%v) = %v", h, err)
		}
	}
	bad := [][]Host{
		{{"a.example.com", 587}, {"b.example.com", 587}},
		{{"a.example.com", 25}, {"b.example.com", 25}},
		{{"a.example.com", 80}, {"b.example.com", 80}},
		{{"a.example.com", 0}},
		{{"a.example.com", 70000}},
		{{"", 443}},
		{{"a b.example.com", 443}},
		{{"a.example.com:443", 443}},
		{{"1.example.com/x", 443}},
	}
	for _, h := range bad {
		if err := Validate(h); err == nil {
			t.Errorf("Validate(%v) accepted it", h)
		}
	}
	many := make([]Host, MaxHosts+1)
	for i := range many {
		many[i] = Host{"h" + strconv.Itoa(i) + ".example.com", 443}
	}
	if Validate(many) == nil {
		t.Error("an unbounded list was accepted")
	}
}

func TestIsPrivate(t *testing.T) {
	t.Parallel()
	for _, a := range []string{
		"10.0.0.1", "172.16.5.5", "192.168.1.1", "127.0.0.1", "169.254.169.254", "0.0.0.0",
		"::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "::ffff:10.1.1.1", "100.64.0.1", "224.0.0.1",
	} {
		if !isPrivate(netip.MustParseAddr(a)) {
			t.Errorf("%s was allowed", a)
		}
	}
	for _, a := range []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111"} {
		if isPrivate(netip.MustParseAddr(a)) {
			t.Errorf("%s was refused", a)
		}
	}
}

// The check runs in the dialer, on the address it is about to connect to, so
// a name that resolves to a private address is refused after resolution.
func TestTheDefaultDialerRefusesALoopbackListener(t *testing.T) {
	t.Parallel()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	g := New(quiet())
	for _, target := range []string{l.Addr().String(), "localhost:" + strconv.Itoa(portOf(l))} {
		c, err := g.dial(t.Context(), "tcp", target)
		if err == nil {
			_ = c.Close()
			t.Fatalf("dialed %s", target)
		}
		if !errors.Is(err, ErrPrivate) {
			t.Fatalf("%s: err = %v, want ErrPrivate", target, err)
		}
	}
}

// fakeUpstream is what the guard dials in these tests: it records the target
// and echoes.
type fakeUpstream struct {
	mu      sync.Mutex
	targets []string
	got     chan []byte
}

func (f *fakeUpstream) dial(_ context.Context, _, address string) (net.Conn, error) {
	f.mu.Lock()
	f.targets = append(f.targets, address)
	f.mu.Unlock()
	a, b := net.Pipe()
	go func() {
		defer func() { _ = b.Close() }()
		buf, _ := io.ReadAll(b)
		f.got <- buf
	}()
	return a, nil
}

func (f *fakeUpstream) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return portOf(l)
}

func connect(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func within(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
		return nil
	}
}

func TestOneHostOnAPortIsDialledWithoutReadingTheClient(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	port := freePort(t)
	if err := g.Open("r1", netip.MustParseAddr("127.0.0.1"), []Host{{"imap.example.com", port}}, false); err != nil {
		t.Fatal(err)
	}
	defer g.Close("r1")

	c := connect(t, port)
	_, _ = c.Write([]byte("A1 CAPABILITY\r\n"))
	closeWrite(c)
	if got := within(t, up.got); string(got) != "A1 CAPABILITY\r\n" {
		t.Fatalf("upstream got %q", got)
	}
	if d := up.dialed(); len(d) != 1 || d[0] != "imap.example.com:"+strconv.Itoa(port) {
		t.Fatalf("dialed %v", d)
	}
}

func TestSeveralHostsOnAPortAreToldApartBySNIAndTheHelloIsReplayed(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	port := freePort(t)
	hosts := []Host{{"a.example.com", port}, {"b.example.com", port}}
	if err := g.Open("r1", netip.MustParseAddr("127.0.0.1"), hosts, false); err != nil {
		t.Fatal(err)
	}
	defer g.Close("r1")

	hello := clientHello(t, "b.example.com")
	c := connect(t, port)
	_, _ = c.Write(hello)
	closeWrite(c)
	if got := within(t, up.got); !bytes.Equal(got, hello) {
		t.Fatal("the upstream did not receive the client's hello unchanged")
	}
	if d := up.dialed(); len(d) != 1 || d[0] != "b.example.com:"+strconv.Itoa(port) {
		t.Fatalf("dialed %v", d)
	}
}

func TestAnUnlistedNameOnASharedPortIsRefusedAndNeverDialled(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	port := freePort(t)
	hosts := []Host{{"a.example.com", port}, {"b.example.com", port}}
	if err := g.Open("r1", netip.MustParseAddr("127.0.0.1"), hosts, false); err != nil {
		t.Fatal(err)
	}
	defer g.Close("r1")

	for name, payload := range map[string][]byte{
		"unlisted": clientHello(t, "evil.example.net"),
		"no name":  clientHello(t, ""),
		"not tls":  []byte("EHLO x\r\n"),
	} {
		c := connect(t, port)
		_, _ = c.Write(payload)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadAll(c); err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "reset") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if d := up.dialed(); len(d) != 0 {
		t.Fatalf("the guard dialled %v for a refused connection", d)
	}
}

func TestClosingARoomStopsItsListenersAndCutsConnections(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	port := freePort(t)
	if err := g.Open("r1", netip.MustParseAddr("127.0.0.1"), []Host{{"imap.example.com", port}}, false); err != nil {
		t.Fatal(err)
	}
	c := connect(t, port)
	_, _ = c.Write([]byte("x"))
	g.Close("r1")

	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection survived its room")
	}
	if c2, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		_ = c2.Close()
		t.Fatal("the listener survived its room")
	}
}

func TestOpenRefusesTwoPlaintextHostsOnOnePort(t *testing.T) {
	t.Parallel()
	g := New(quiet())
	err := g.Open("r1", netip.MustParseAddr("127.0.0.1"), []Host{{"a.example.com", 25}, {"b.example.com", 25}}, false)
	if err == nil {
		g.Close("r1")
		t.Fatal("opened")
	}
}

func TestNewPanicsOnANilLogger(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	New(nil)
}

func portOf(l net.Listener) int { return int(netip.MustParseAddrPort(l.Addr().String()).Port()) }

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}
