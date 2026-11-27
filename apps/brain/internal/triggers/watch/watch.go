// Package watch is the extract-and-compare step of a watcher: a watcher polls
// a web page or a feed and starts an agent run when what it finds changes.
// Everything here is a pure function of its arguments. It reads no clock,
// opens no connection and touches no database, so the poller that fetches the
// body and stores the state owns all of that and this package stays easy to
// test.
package watch

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/cascadia"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

// The kinds of thing a watcher can poll.
const (
	WebPage = "web_page"
	Feed    = "feed"
)

// The outcomes of one comparison.
const (
	// Baseline is the first look: the state is stored and nothing is run.
	Baseline = "baseline"
	// Unchanged means there is nothing new and nothing held back to deliver.
	Unchanged = "unchanged"
	// Changed means a run should start and be given Result.Change.
	Changed = "changed"
	// Held means something changed but the cooldown has not passed, so the
	// change waits in State.Pending.
	Held = "held"
)

const (
	maxURL      = 2048
	maxSelector = 512
	maxText     = 64 << 10
	maxChange   = 8 << 10
	maxSeen     = 500
	maxItems    = 20
	maxSummary  = 1 << 10
	cutMarker   = "… (cut)"
)

// State is what a watcher remembers between checks, stored as jsonb. A web page
// keeps Hash and Text, a feed keeps Seen.
type State struct {
	Hash string `json:"hash,omitempty"`
	// Text is the extracted text, or empty when it was over 64 KiB and only
	// the hash was kept.
	Text string `json:"text,omitempty"`
	// Seen has no omitempty on purpose: a feed with no items yet must come
	// back from JSON as an empty list, not nil, or the next check would read
	// as a baseline again.
	Seen []string `json:"seen"`
	// Pending is a change that was held back by the cooldown.
	Pending     string     `json:"pending,omitempty"`
	ErrorsSince *time.Time `json:"errors_since,omitempty"`
}

// Result is what one comparison decided.
type Result struct {
	Outcome string
	// Change is what the run is given. It is empty unless Outcome is Changed.
	Change string
	// State is what to store, whatever the outcome.
	State State
}

// Normalize makes a watched URL canonical so two spellings of one address are
// one watcher. It returns the URL and its host (without port).
func Normalize(raw string) (normalized, host string, err error) {
	if len(raw) > maxURL {
		return "", "", fmt.Errorf("url must be at most %d bytes", maxURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("url must be http or https")
	}
	if u.User != nil {
		return "", "", fmt.Errorf("url must not contain a user name or password")
	}
	host = strings.ToLower(u.Hostname())
	if host == "" || u.Opaque != "" {
		return "", "", fmt.Errorf("url must be absolute and name a host")
	}
	if webfetch.NumericHost(host) {
		return "", "", fmt.Errorf("url's host %s is a number but not an IP address", host)
	}

	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	port := u.Port()
	if port != "" && port != defaultPort[u.Scheme] {
		authority += ":" + port
	}

	// The path and query are taken from the text as given, because re-encoding
	// them could change which page a server returns.
	rest := raw
	rest, _, _ = strings.Cut(rest, "#")
	_, rest, _ = strings.Cut(rest, "://")
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[i:]
	} else {
		rest = ""
	}
	return u.Scheme + "://" + authority + rest, host, nil
}

var defaultPort = map[string]string{"http": "80", "https": "443"}

// CompileSelector says whether s is a CSS selector the page extractor can use.
func CompileSelector(s string) error {
	if len(s) > maxSelector {
		return fmt.Errorf("selector must be at most %d bytes", maxSelector)
	}
	if _, err := cascadia.Compile(s); err != nil {
		return fmt.Errorf("selector is not valid CSS: %w", err)
	}
	return nil
}

// Compare decides what a fresh body means against what the watcher remembered.
// now and lastRun are passed in so the cooldown can be tested; lastRun is nil
// when the watcher has never started a run. A body that cannot be read as the
// kind it should be returns an error and no Result, so the caller counts it
// as a failed check and the stored state is left alone.
func Compare(kind, selector string, old State, body []byte, now time.Time, lastRun *time.Time, cooldown time.Duration) (Result, error) {
	var (
		next     State
		change   string
		changed  bool
		baseline bool
	)
	switch kind {
	case WebPage:
		text, err := extractPage(body, selector)
		if err != nil {
			return Result{}, err
		}
		next, change, changed, baseline = comparePage(old, text)
	case Feed:
		items, err := parseFeed(body)
		if err != nil {
			return Result{}, err
		}
		next, change, changed, baseline = compareFeed(old, items)
	default:
		return Result{}, fmt.Errorf("unknown watcher kind %q", kind)
	}
	// A successful read ends any run of errors, and next never carries one.
	next.ErrorsSince = nil

	if baseline {
		return Result{Outcome: Baseline, State: next}, nil
	}
	if !changed {
		next.Pending = old.Pending
		return Release(next, now, lastRun, cooldown), nil
	}
	switch {
	case Ready(now, lastRun, cooldown):
		return Result{Outcome: Changed, Change: merge(old.Pending, change), State: next}, nil
	default:
		next.Pending = merge(old.Pending, change)
		return Result{Outcome: Held, State: next}, nil
	}
}

// Release is a look that found nothing new, a body the same or a 304: a
// change held by the cooldown runs once the cooldown has passed, and
// otherwise stays held. Errors end, as the address answered.
func Release(st State, now time.Time, lastRun *time.Time, cooldown time.Duration) Result {
	st.ErrorsSince = nil
	if st.Pending != "" && Ready(now, lastRun, cooldown) {
		change := st.Pending
		st.Pending = ""
		return Result{Outcome: Changed, Change: change, State: st}
	}
	return Result{Outcome: Unchanged, State: st}
}

// Ready is the cooldown passed: no run yet, or the last one long enough ago.
func Ready(now time.Time, lastRun *time.Time, cooldown time.Duration) bool {
	return lastRun == nil || !lastRun.Add(cooldown).After(now)
}

// merge joins a held change and a new one, oldest first. When the result is
// too long the start is kept, so the oldest unseen change is never lost.
func merge(pending, change string) string {
	if pending == "" {
		return cut(change)
	}
	return cut(pending + "\n" + change)
}

// cut shortens s to at most maxChange bytes, on a line boundary where it can, and
// says so on a last line.
func cut(s string) string {
	if len(s) <= maxChange {
		return s
	}
	room := maxChange - len("\n"+cutMarker)
	head := s[:room]
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	} else {
		// One line longer than the limit: cut it without splitting a rune.
		for !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
	}
	return head + "\n" + cutMarker
}
