package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
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
	b, n, _ := g.Blocked()
	if len(b) != maxBlocked || n != maxBlocked+50 {
		t.Fatalf("named %d, counted %d; want %d named of %d", len(b), n, maxBlocked, maxBlocked+50)
	}
	if len(b[0]) > maxBlockedURL+len("…") {
		t.Errorf("a blocked address is %d bytes, want at most %d", len(b[0]), maxBlockedURL)
	}
	if b, n, _ := g.Blocked(); len(b) != 0 || n != 0 {
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

// A request no judge can take now is handed to be failed, never waited for
// and never let through: the browser's event loop cannot be held. Only the
// tab's are counted in its read; a browser-wide one may be another run's.
// Past the failers' queue a request stays paused.
func TestARequestNoJudgeCanTakeIsFailedAndOnlyTheTabsCounted(t *testing.T) {
	t.Parallel()

	g := &guard{
		tab: t.Context(), queue: make(chan paused), wide: make(chan paused),
		failQ: make(chan paused, 150), wideFail: make(chan paused, 150),
	}
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		// Other runs' flood first: it must leave the tab's own room.
		for _, browser := range []bool{true, false} {
			for range 200 {
				g.pause(browser)(&fetch.EventRequestPaused{RequestID: "r1", Request: &network.Request{URL: "https://example.com/"}})
			}
		}
	}()
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the pause handler blocked the browser's event loop")
	}
	if b, n, shed := g.Blocked(); len(b) != 0 || n != 0 || shed != 200 {
		t.Errorf("named %v, refused %d, held back %d; want only the tab's 200 held back", b, n, shed)
	}
	if len(g.failQ) != cap(g.failQ) || len(g.wideFail) != cap(g.wideFail) {
		t.Errorf("handed to be failed: the tab's %d, browser-wide %d; want each queue's own %d, %d",
			len(g.failQ), len(g.wideFail), cap(g.failQ), cap(g.wideFail))
	}
}

// The canary's own request held back under load and failed by a failer is
// still noticed as paused: the guard paused it.
func TestACanaryHeldBackIsStillNoticed(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context(), watch: map[string]chan struct{}{}, failQ: make(chan paused, 1), wideFail: make(chan paused, 1)}
	sub := g.watching("/sub-a")
	g.wideFail <- paused{url: "http://" + canaryHost + "/sub-a", browser: true}
	go g.failer()
	select {
	case <-sub:
	case <-time.After(5 * time.Second):
		t.Fatal("the canary was failed and not noticed")
	}
}

// A name server that never answers is a refusal in judgeTimeout: a page's
// own lookups cannot hold the judges.
func TestALookupThatHangsIsARefusal(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context(), allowed: map[string]bool{}, public: func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	judged := make(chan error, 1)
	go func() { judged <- g.allows("https://tarpit.example/") }()
	select {
	case err := <-judged:
		if err == nil {
			t.Fatal("a lookup that never answered let the request through")
		}
	case <-time.After(judgeTimeout + 2*time.Second):
		t.Fatalf("no verdict in %v: a lookup held a judge", judgeTimeout+2*time.Second)
	}
	if g.allowed["tarpit.example"] {
		t.Error("a lookup that never answered was remembered as allowed")
	}
}

// What the browser-wide guard refuses is not named in the tab's read: on a
// browser runs share it may be another run's request.
func TestARequestRefusedBrowserWideIsNotNamed(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context()}
	g.refuse(paused{url: "http://10.0.0.5/elsewhere", browser: true})
	if b, n, _ := g.Blocked(); len(b) != 0 || n != 0 {
		t.Errorf("named %v, counted %d; want nothing from outside the tab", b, n)
	}
}

// Only the canary's own request, by its path, says it was paused: not its
// other request, not another run's canary, and pausing it twice, by the
// tab's guard and the browser-wide one, is noticed once.
func TestOnlyTheCanarysOwnRequestProvesIt(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context(), watch: map[string]chan struct{}{}}
	nav, sub := g.watching("/nav-a"), g.watching("/sub-a")
	g.refuse(paused{url: "http://" + canaryHost + "/sub-another-run", browser: true})
	g.refuse(paused{url: "http://" + canaryHost + "/nav-a"})
	g.refuse(paused{url: "http://" + canaryHost + "/nav-a", browser: true})
	select {
	case <-sub:
		t.Fatal("the fetch was taken as paused when only the navigation and another run's were")
	default:
	}
	select {
	case <-nav:
	default:
		t.Error("the navigation was paused and not noticed")
	}
	if b, n, _ := g.Blocked(); len(b) != 0 || n != 0 {
		t.Errorf("named %v, counted %d; the canary is never the page's", b, n)
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
	got := clamp(Snapshot{Title: strings.Repeat("é", maxTitle), URL: "https://a.example/" + strings.Repeat("a", maxURL)})
	if len(got.Title) > maxTitle+len("…") || len(got.URL) > maxURL+len("…") || !utf8.ValidString(got.Title) {
		t.Errorf("title %d bytes, address %d bytes; want at most %d and %d, on a rune", len(got.Title), len(got.URL), maxTitle, maxURL)
	}
}

// A popup is handed to be closed once, whatever events it sends; one that
// finds the closer full is tried again on its next, and the read says the
// page was not kept in its tab; one that is gone is forgotten. Another run's
// page, and the tab itself, are left alone.
func TestEachPopupIsClosedOnceAndRetriedWhenFull(t *testing.T) {
	t.Parallel()

	g := &guard{tab: t.Context()}
	w := &popups{g: g, ours: "tab", own: "ctx", closed: map[target.ID]bool{}, closing: make(chan target.ID, 1)}
	popup := func(id target.ID) *target.Info {
		return &target.Info{TargetID: id, Type: "page", OpenerID: "tab", BrowserContextID: "ctx"}
	}
	w.event(&target.EventTargetCreated{TargetInfo: &target.Info{TargetID: "tab", Type: "page", BrowserContextID: "ctx"}})
	w.event(&target.EventTargetCreated{TargetInfo: &target.Info{TargetID: "theirs", Type: "page", BrowserContextID: "other"}})
	w.event(&target.EventTargetCreated{TargetInfo: popup("a")})
	w.event(&target.EventTargetInfoChanged{TargetInfo: popup("a")})
	w.event(&target.EventTargetCreated{TargetInfo: popup("b")})
	if len(w.closing) != 1 || <-w.closing != "a" {
		t.Fatal("the first popup was not handed to be closed, once, alone")
	}
	g.mu.Lock()
	if !g.uncontained {
		t.Error("a popup the closer could not take is not said")
	}
	g.mu.Unlock()
	w.event(&target.EventTargetInfoChanged{TargetInfo: popup("b")})
	if len(w.closing) != 1 || <-w.closing != "b" {
		t.Error("the popup that found the closer full was not tried again")
	}
	w.event(&target.EventTargetDestroyed{TargetID: "a"})
	w.event(&target.EventTargetDestroyed{TargetID: "b"})
	if len(w.closed) != 0 {
		t.Errorf("popups gone are still kept: %v", w.closed)
	}
}
