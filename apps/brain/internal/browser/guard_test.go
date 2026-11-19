package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestWhatARequestReaches(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw, host string
		refused   bool
	}{
		{"https://example.com/a.js", "example.com", false},
		{"http://example.com:8080/", "example.com", false},
		{"wss://example.com/socket", "example.com", false},
		{"HTTPS://Example.com/", "Example.com", false},
		{"http://10.0.0.5/", "10.0.0.5", false},
		{"http://[::1]:9222/", "::1", false},
		// A page's own bytes reach nothing.
		{"data:image/png;base64,AAAA", "", false},
		{"blob:https://example.com/1234", "", false},
		{"about:blank", "", false},
		// Anything else is refused, judged or not.
		{"file:///etc/passwd", "", true},
		{"chrome://settings", "", true},
		{"ftp://example.com/", "", true},
		{"http:///nohost", "", true},
		{"http://2130706433/", "", true},
		{"http://0x7f.1/", "", true},
		{"http://127.1/", "", true},
		{"%zz", "", true},
	} {
		host, err := hostOf(tc.raw)
		if (err != nil) != tc.refused || host != tc.host {
			t.Errorf("hostOf(%q) = %q, %v; want %q, refused %v", tc.raw, host, err, tc.host, tc.refused)
		}
	}
}

// Every host a request names is judged, a page's own bytes are not, and a
// host is judged once.
func TestAHostIsJudgedOnceASession(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	g := &guard{tab: t.Context(), judged: map[string]error{}, public: func(_ context.Context, host string) error {
		asked.Add(1)
		if host == "10.0.0.5" {
			return errors.New("not public")
		}
		return nil
	}}

	for range 3 {
		if err := g.allowed("https://example.com/x"); err != nil {
			t.Fatalf("a public host refused: %v", err)
		}
		if err := g.allowed("http://10.0.0.5/"); err == nil {
			t.Fatal("a private host allowed")
		}
	}
	if err := g.allowed("data:text/plain,hi"); err != nil {
		t.Errorf("data refused: %v", err)
	}
	if err := g.allowed("file:///etc/passwd"); err == nil {
		t.Error("file allowed")
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("public was asked %d times, want once a host", n)
	}
}

func TestBlockedIsForgottenOnceRead(t *testing.T) {
	t.Parallel()

	g := &guard{blocked: []string{"http://10.0.0.5/"}}
	if b := g.Blocked(); len(b) != 1 {
		t.Fatalf("Blocked = %v", b)
	}
	if b := g.Blocked(); len(b) != 0 {
		t.Errorf("Blocked again = %v, want nothing", b)
	}
}
