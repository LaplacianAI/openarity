package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
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
		// Go and a browser may read these differently, so neither is judged.
		{"http://user:pass@example.com/", "", true},
		{"http://example.com\\@10.0.0.5/", "", true},
		{"http://[::ffff:10.0.0.5]/", "::ffff:10.0.0.5", false},
	} {
		host, err := hostOf(tc.raw)
		if (err != nil) != tc.refused || host != tc.host {
			t.Errorf("hostOf(%q) = %q, %v; want %q, refused %v", tc.raw, host, err, tc.host, tc.refused)
		}
	}
}

// Every host a request names is judged and a page's own bytes are not. A host
// let through is remembered; a refusal is judged again, as it may have been a
// lookup that failed for a moment.
func TestOnlyAHostLetThroughIsRemembered(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	g := &guard{tab: t.Context(), allowed: map[string]bool{}, public: func(_ context.Context, host string) error {
		asked.Add(1)
		if host == "10.0.0.5" {
			return errors.New("not public")
		}
		return nil
	}}

	for range 3 {
		if err := g.allows("https://example.com/x"); err != nil {
			t.Fatalf("a public host refused: %v", err)
		}
		if err := g.allows("http://10.0.0.5/"); err == nil {
			t.Fatal("a private host allowed")
		}
	}
	if err := g.allows("data:text/plain,hi"); err != nil {
		t.Errorf("data refused: %v", err)
	}
	if err := g.allows("file:///etc/passwd"); err == nil {
		t.Error("file allowed")
	}
	// example.com once, 10.0.0.5 each of three times.
	if n := asked.Load(); n != 4 {
		t.Errorf("public was asked %d times, want 4", n)
	}
}

// The canary is refused whatever public says: it is how a browser that does
// not pause its requests is found out.
func TestTheCanaryIsNeverAllowed(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context(), allowed: map[string]bool{}, public: func(context.Context, string) error { return nil }}
	if err := g.allows("http://" + canaryHost + "/"); err == nil {
		t.Error("the canary was allowed")
	}
}

// Past maxJudged a host is judged every time, never let through unjudged.
func TestPastTheCapAHostIsJudgedEveryTime(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	g := &guard{tab: t.Context(), allowed: map[string]bool{}, public: func(context.Context, string) error {
		asked.Add(1)
		return nil
	}}
	for i := range maxJudged {
		g.allowed[fmt.Sprintf("h%d.example", i)] = true
	}
	for range 2 {
		if err := g.allows("https://new.example/"); err != nil {
			t.Fatal(err)
		}
	}
	if n := asked.Load(); n != 2 || len(g.allowed) != maxJudged {
		t.Errorf("asked %d times, %d remembered; want 2 and no more than the cap", n, len(g.allowed))
	}
}

// A page chooses what it asks for, so what it was refused is named at most a
// hundred times, each cut short, and counted past that.
func TestWhatIsBlockedIsBoundedAndForgottenOnceRead(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context()}
	long := "http://10.0.0.5/" + strings.Repeat("a", 1000)
	for range maxBlocked + 50 {
		g.refuse(paused{url: long})
	}
	b, n := g.Blocked()
	if len(b) != maxBlocked || n != maxBlocked+50 {
		t.Fatalf("named %d, counted %d; want %d named of %d", len(b), n, maxBlocked, maxBlocked+50)
	}
	if len(b[0]) > maxBlockedURL+len("…") {
		t.Errorf("a blocked address is %d bytes, want at most %d", len(b[0]), maxBlockedURL)
	}
	if b, n := g.Blocked(); len(b) != 0 || n != 0 {
		t.Errorf("Blocked again = %v, %d; want nothing", b, n)
	}
}

func TestClipCutsOnARune(t *testing.T) {
	t.Parallel()

	if got := clip("ééé", 3); got != "é…" {
		t.Errorf("clip = %q, want the first whole rune and an ellipsis", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip = %q", got)
	}
}

// A request the judges cannot take now is refused, never waited for: the
// browser's event loop cannot be held.
func TestARequestNoJudgeCanTakeIsRefused(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context(), queue: make(chan paused)}
	g.pause(&fetch.EventRequestPaused{RequestID: "r1", Request: &network.Request{URL: "https://example.com/"}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, n := g.Blocked(); n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a request no judge could take was not refused")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAReadIsHeldToItsLimit(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", maxText+10)
	if got := clamp(Snapshot{Text: long}); !got.Cut || len([]rune(got.Text)) != maxText {
		t.Errorf("clamped to %d runes, cut %v; want %d and cut", len([]rune(got.Text)), got.Cut, maxText)
	}
	if got := clamp(Snapshot{Text: "short"}); got.Cut || got.Text != "short" {
		t.Errorf("a short read changed: %+v", got)
	}
}
