package watch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func TestNormalize(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 2048)
	tests := []struct {
		name, in, want, host, err string
	}{
		{name: "case", in: "HTTPS://Example.COM/Path?Q=A", want: "https://example.com/Path?Q=A", host: "example.com"},
		{name: "https default port", in: "https://example.com:443/x", want: "https://example.com/x", host: "example.com"},
		{name: "http default port", in: "http://example.com:80/x", want: "http://example.com/x", host: "example.com"},
		{name: "other port kept", in: "https://example.com:80/x", want: "https://example.com:80/x", host: "example.com"},
		{name: "http 443 kept", in: "http://example.com:443", want: "http://example.com:443", host: "example.com"},
		{name: "fragment dropped", in: "https://example.com/a?b=1#top", want: "https://example.com/a?b=1", host: "example.com"},
		{name: "empty path stays empty", in: "https://example.com", want: "https://example.com", host: "example.com"},
		{name: "empty path with query", in: "https://example.com?x=1", want: "https://example.com?x=1", host: "example.com"},
		{name: "path kept as given", in: "https://example.com/a%2Fb/../c//d", want: "https://example.com/a%2Fb/../c//d", host: "example.com"},
		{name: "ipv6", in: "http://[::1]:80/", want: "http://[::1]/", host: "::1"},
		{name: "bad scheme", in: "ftp://example.com/", err: "url must be http or https"},
		{name: "no scheme", in: "example.com/x", err: "url must be http or https"},
		{name: "user info", in: "https://user:pw@example.com/", err: "url must not contain a user name or password"},
		{name: "no host", in: "https:///path", err: "url must be absolute and name a host"},
		{name: "opaque", in: "https:example.com", err: "url must be absolute and name a host"},
		{name: "numeric host", in: "http://2130706433/", err: "url's host 2130706433 is a number but not an IP address"},
		{name: "short ip", in: "http://127.1/", err: "url's host 127.1 is a number but not an IP address"},
		{name: "real ip fine", in: "http://127.0.0.1/", want: "http://127.0.0.1/", host: "127.0.0.1"},
		{name: "too long", in: long, err: "url must be at most 2048 bytes"},
		{name: "garbage", in: "http://exa mple.com/", err: "url is not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, host, err := Normalize(tt.in)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want || host != tt.host {
				t.Fatalf("got %q, %q, %v; want %q, %q", got, host, err, tt.want, tt.host)
			}
		})
	}
}

func TestCompileSelector(t *testing.T) {
	tests := []struct {
		name, sel string
		bad       bool
	}{
		{"class", "div.price > span", false},
		{"bad", "div[", true},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 513), true},
		{"limit", strings.Repeat("a", 512), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CompileSelector(tt.sel); (err != nil) != tt.bad {
				t.Fatalf("err = %v, want bad=%v", err, tt.bad)
			}
		})
	}
}

func TestExtractPage(t *testing.T) {
	const doc = `<html><head><title>T</title><style>p{}</style></head><body>
<script>var x = 1;</script><noscript>no js</noscript><svg><text>icon</text></svg><template><p>tpl</p></template>
<h1> Big   title </h1><p>one <b>two</b>
 three</p><ul><li>a</li><li>b</li></ul>line<br>after<div></div>
<span class="p"> 10 </span><span class="p">20
 EUR</span></body></html>`
	tests := []struct {
		name, sel, want, err string
	}{
		{name: "body text", want: "Big title\none two three\na\nb\nline\nafter\n10 20 EUR"},
		{name: "selector in order", sel: "span.p", want: "10\n20 EUR"},
		{name: "selector union", sel: "li, h1", want: "Big title\na\nb"},
		{name: "no match", sel: "table", err: "the selector matched nothing"},
		{name: "bad selector", sel: "p[", err: "selector is not valid CSS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractPage([]byte(doc), tt.sel)
			if tt.err != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestSelectorSkipsScriptInside(t *testing.T) {
	got, err := extractPage([]byte(`<div id=a>hi<script>bad()</script> there</div>`), "#a")
	if err != nil || got != "hi there" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name         string
		old, cur     []string
		removed, add []string
	}{
		{"plain", []string{"a", "b"}, []string{"b", "c"}, []string{"a"}, []string{"c"}},
		{"duplicate one fewer", []string{"x", "y", "x"}, []string{"x", "y"}, []string{"x"}, nil},
		{"duplicate one more", []string{"x"}, []string{"x", "x"}, nil, []string{"x"}},
		{"order from old and new", []string{"d", "a", "c"}, []string{"z", "c", "b"}, []string{"d", "a"}, []string{"z", "b"}},
		{"reorder only", []string{"a", "b"}, []string{"b", "a"}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, a := diffLines(tt.old, tt.cur)
			if fmt.Sprint(r) != fmt.Sprint(tt.removed) || fmt.Sprint(a) != fmt.Sprint(tt.add) {
				t.Fatalf("got -%v +%v, want -%v +%v", r, a, tt.removed, tt.add)
			}
		})
	}
}

func page(lines ...string) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("<p>" + l + "</p>")
	}
	return []byte(b.String())
}

func mustCompare(t *testing.T, kind, sel string, old State, body []byte, now time.Time, last *time.Time, cd time.Duration) Result {
	t.Helper()
	r, err := Compare(kind, sel, old, body, now, last, cd)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return r
}

func TestPageChangeRendering(t *testing.T) {
	base := mustCompare(t, WebPage, "", State{}, page("a", "b", "b"), t0, nil, 0).State
	r := mustCompare(t, WebPage, "", base, page("b", "c"), t0, nil, 0)
	if r.Outcome != Changed || r.Change != "- a\n- b\n+ c" {
		t.Fatalf("got %s %q", r.Outcome, r.Change)
	}
	r = mustCompare(t, WebPage, "", base, page("b", "a", "b"), t0, nil, 0)
	if r.Outcome != Changed || r.Change != "the lines were reordered" {
		t.Fatalf("got %s %q", r.Outcome, r.Change)
	}
}

func TestPageTextCap(t *testing.T) {
	small := mustCompare(t, WebPage, "", State{}, page("x"), t0, nil, 0).State
	if small.Text != "x" || small.Hash == "" {
		t.Fatalf("small state %+v", small)
	}
	// Exactly at the cap the text is kept; one byte over only the hash is.
	at := mustCompare(t, WebPage, "", State{}, page(strings.Repeat("a", maxText)), t0, nil, 0).State
	if len(at.Text) != maxText {
		t.Fatalf("text at cap dropped: %d", len(at.Text))
	}
	over := mustCompare(t, WebPage, "", State{}, page(strings.Repeat("a", maxText+1)), t0, nil, 0).State
	if over.Text != "" || over.Hash == "" {
		t.Fatalf("over cap state: text %d hash %q", len(over.Text), over.Hash)
	}
	// With the old text gone, the change is the new text as additions.
	r := mustCompare(t, WebPage, "", over, page("p", "q"), t0, nil, 0)
	if r.Outcome != Changed || r.Change != "+ p\n+ q" {
		t.Fatalf("got %s %q", r.Outcome, r.Change)
	}
}

func TestChangeCut(t *testing.T) {
	old := mustCompare(t, WebPage, "", State{}, page("start"), t0, nil, 0).State
	var lines []string
	for i := range 2000 {
		lines = append(lines, fmt.Sprintf("line number %04d", i))
	}
	r := mustCompare(t, WebPage, "", old, page(lines...), t0, nil, 0)
	if len(r.Change) > maxChange || !strings.HasSuffix(r.Change, "\n… (cut)") {
		t.Fatalf("len %d, tail %q", len(r.Change), r.Change[len(r.Change)-20:])
	}
	// The cut falls between lines: every kept line is whole.
	for _, l := range strings.Split(r.Change, "\n")[1 : len(strings.Split(r.Change, "\n"))-1] {
		if len(l) != len("+ line number 0000") {
			t.Fatalf("partial line %q", l)
		}
	}
	// One line longer than the limit is cut inside, on a rune boundary.
	long := strings.Repeat("é", maxChange)
	r = mustCompare(t, WebPage, "", old, page(long), t0, nil, 0)
	if len(r.Change) > maxChange || !strings.HasSuffix(r.Change, "\n… (cut)") {
		t.Fatalf("long line: len %d", len(r.Change))
	}
}

const rss = `<?xml version="1.0"?><rss version="2.0"><channel><title>x</title>
<item><title> First  post </title><link>https://e.com/1</link><guid>g1</guid><pubDate>Mon, 01 Jan 2026 00:00:00 GMT</pubDate>
<description>&lt;p&gt;Hello &lt;b&gt;big&lt;/b&gt; world&lt;br&gt;again&lt;/p&gt;</description></item>
<item><title>Second</title><link>https://e.com/2</link></item>
<item><title>Third</title><pubDate>d3</pubDate></item>
</channel></rss>`

const atomFeed = `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom">
<entry><title>A</title><id>urn:a</id><link rel="self" href="https://e.com/self"/><link rel="alternate" href="https://e.com/a"/>
<updated>2026-01-02T00:00:00Z</updated><published>2026-01-01T00:00:00Z</published><summary>sum &amp; stuff</summary><content>content</content></entry>
<entry><title>B</title><link href="https://e.com/b"/><published>2026-01-03T00:00:00Z</published><content type="html">&lt;i&gt;only content&lt;/i&gt;</content></entry>
</feed>`

func TestParseFeed(t *testing.T) {
	tests := []struct {
		name, body string
		want       []item
		err        string
	}{
		{"rss", rss, []item{
			{"g1", "First post", "https://e.com/1", "Mon, 01 Jan 2026 00:00:00 GMT", "Hello big world again"},
			{"https://e.com/2", "Second", "https://e.com/2", "", ""},
			{"sha256:" + hashText("Third\nd3"), "Third", "", "d3", ""},
		}, ""},
		{"atom", atomFeed, []item{
			{"urn:a", "A", "https://e.com/a", "2026-01-02T00:00:00Z", "sum & stuff"},
			{"https://e.com/b", "B", "https://e.com/b", "2026-01-03T00:00:00Z", "only content"},
		}, ""},
		{"html", "<html><body>hi</body></html>", nil, "the feed is neither RSS nor Atom"},
		{"empty", "", nil, "the feed is neither RSS nor Atom"},
		{"other xml", "<foo/>", nil, "the feed is neither RSS nor Atom"},
		{
			"unknown charset", `<?xml version="1.0" encoding="ISO-8859-1"?><rss><channel><item><guid>1</guid></item></channel></rss>`,
			[]item{{ID: "1"}},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFeed([]byte(tt.body))
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("got %v, %v\nwant %v", got, err, tt.want)
			}
		})
	}
}

func TestSummaryCut(t *testing.T) {
	it := makeItem("x", "t", "", "", strings.Repeat("é", 800))
	if len(it.Summary) > maxSummary || len(it.Summary)%2 != 0 {
		t.Fatalf("summary %d bytes", len(it.Summary))
	}
}

func feedBody(ids ...string) []byte {
	var b strings.Builder
	b.WriteString("<rss><channel>")
	for _, id := range ids {
		fmt.Fprintf(&b, "<item><title>t%s</title><guid>%s</guid></item>", id, id)
	}
	b.WriteString("</channel></rss>")
	return []byte(b.String())
}

func TestFeedSeenCap(t *testing.T) {
	var ids []string
	for i := range 600 {
		ids = append(ids, fmt.Sprint("n", i))
	}
	r := mustCompare(t, Feed, "", State{}, feedBody(ids...), t0, nil, 0)
	if len(r.State.Seen) != maxSeen || r.State.Seen[0] != "n0" || r.State.Seen[499] != "n499" {
		t.Fatalf("seen %d", len(r.State.Seen))
	}
	// New items go first and the oldest memories fall off the end.
	r2 := mustCompare(t, Feed, "", r.State, feedBody("new", "n0"), t0, nil, 0)
	if len(r2.State.Seen) != maxSeen || r2.State.Seen[0] != "new" || r2.State.Seen[1] != "n0" || r2.State.Seen[2] != "n1" ||
		r2.State.Seen[499] != "n498" {
		t.Fatalf("seen order %v ... %v", r2.State.Seen[:3], r2.State.Seen[497:])
	}
	if !strings.Contains(r2.Change, "tnew") || strings.Contains(r2.Change, "tn0") {
		t.Fatalf("change %q", r2.Change)
	}
}

func TestFeedChangeLimits(t *testing.T) {
	base := mustCompare(t, Feed, "", State{}, feedBody("old"), t0, nil, 0).State
	var ids []string
	for i := range 30 {
		ids = append(ids, fmt.Sprint("n", i))
	}
	r := mustCompare(t, Feed, "", base, feedBody(ids...), t0, nil, 0)
	if got := strings.Count(r.Change, "\n\n") + 1; got != 20 {
		t.Fatalf("%d items, want 20", got)
	}
	if strings.Index(r.Change, "tn0") > strings.Index(r.Change, "tn1\n") {
		t.Fatalf("not in document order")
	}

	// Whole items stop at 8 KiB; the first alone is cut.
	big := func(n int) string {
		return fmt.Sprintf("<item><guid>g%d</guid><title>%s</title></item>", n, strings.Repeat("w", 3000))
	}
	body := []byte("<rss><channel>" + big(1) + big(2) + big(3) + "</channel></rss>")
	r = mustCompare(t, Feed, "", base, body, t0, nil, 0)
	if n := strings.Count(r.Change, "\n\n") + 1; n != 2 || len(r.Change) > maxChange {
		t.Fatalf("%d items, %d bytes", n, len(r.Change))
	}
	body = []byte("<rss><channel><item><guid>h</guid><title>" + strings.Repeat("w", 9000) + "</title></item></channel></rss>")
	r = mustCompare(t, Feed, "", base, body, t0, nil, 0)
	if len(r.Change) > maxChange || !strings.HasSuffix(r.Change, "… (cut)") {
		t.Fatalf("first item not cut: %d", len(r.Change))
	}
}

func TestBaseline(t *testing.T) {
	tests := []struct {
		name, kind string
		body       []byte
	}{
		{"page", WebPage, page("hello")},
		{"feed", Feed, feedBody("a")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := State{ErrorsSince: &t0}
			r := mustCompare(t, tt.kind, "", old, tt.body, t0, nil, 0)
			if r.Outcome != Baseline || r.Change != "" || r.State.ErrorsSince != nil {
				t.Fatalf("got %+v", r)
			}
			again := mustCompare(t, tt.kind, "", r.State, tt.body, t0, nil, 0)
			if again.Outcome != Unchanged {
				t.Fatalf("second check was %s", again.Outcome)
			}
		})
	}
}

func TestEmptyFeedThenItem(t *testing.T) {
	r := mustCompare(t, Feed, "", State{}, feedBody(), t0, nil, 0)
	if r.Outcome != Baseline || r.State.Seen == nil {
		t.Fatalf("got %+v", r)
	}
	// The state must survive the database as it is written.
	raw, err := json.Marshal(r.State)
	if err != nil {
		t.Fatal(err)
	}
	var stored State
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Seen == nil {
		t.Fatalf("empty seen lost in JSON: %s", raw)
	}
	r2 := mustCompare(t, Feed, "", stored, feedBody("a"), t0, nil, 0)
	if r2.Outcome != Changed || !strings.Contains(r2.Change, "ta") {
		t.Fatalf("got %+v", r2)
	}
}

func TestExtractionErrors(t *testing.T) {
	tests := []struct {
		name, kind, sel, body, err string
	}{
		{"no match", WebPage, "table", "<p>x</p>", "the selector matched nothing"},
		{"not a feed", Feed, "", "<html></html>", "the feed is neither RSS nor Atom"},
		{"unknown kind", "podcast", "", "", `unknown watcher kind "podcast"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Compare(tt.kind, tt.sel, State{Hash: "x"}, []byte(tt.body), t0, nil, 0)
			if err == nil || err.Error() != tt.err || r.Outcome != "" {
				t.Fatalf("got %+v, %v", r, err)
			}
		})
	}
}

func TestCompareDecisions(t *testing.T) {
	hour := time.Hour
	recent := t0.Add(-30 * time.Minute)
	exactly := t0.Add(-hour)
	base := mustCompare(t, WebPage, "", State{}, page("a"), t0, nil, 0).State

	tests := []struct {
		name    string
		old     State
		body    []byte
		last    *time.Time
		outcome string
		change  string
		pending string
	}{
		{"unchanged", base, page("a"), nil, Unchanged, "", ""},
		{"unchanged clears errors", State{Hash: base.Hash, Text: "a", ErrorsSince: &t0}, page("a"), nil, Unchanged, "", ""},
		{"changed never run", base, page("b"), nil, Changed, "- a\n+ b", ""},
		{"changed past cooldown", base, page("b"), &exactly, Changed, "- a\n+ b", ""},
		{"held within cooldown", base, page("b"), &recent, Held, "", "- a\n+ b"},
		{"unchanged keeps pending in cooldown", State{Hash: base.Hash, Text: "a", Pending: "held"}, page("a"), &recent, Unchanged, "", "held"},
		{"unchanged releases pending", State{Hash: base.Hash, Text: "a", Pending: "held"}, page("a"), &exactly, Changed, "held", ""},
		{"unchanged releases pending never run", State{Hash: base.Hash, Text: "a", Pending: "held"}, page("a"), nil, Changed, "held", ""},
		{"changed merges pending past cooldown", State{Hash: base.Hash, Text: "a", Pending: "earlier"}, page("b"), nil, Changed, "earlier\n- a\n+ b", ""},
		{"held merges pending", State{Hash: base.Hash, Text: "a", Pending: "earlier"}, page("b"), &recent, Held, "", "earlier\n- a\n+ b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustCompare(t, WebPage, "", tt.old, tt.body, t0, tt.last, hour)
			if r.Outcome != tt.outcome || r.Change != tt.change || r.State.Pending != tt.pending || r.State.ErrorsSince != nil {
				t.Fatalf("got %s %q pending %q errs %v", r.Outcome, r.Change, r.State.Pending, r.State.ErrorsSince)
			}
		})
	}
}

func TestHeldThenReleased(t *testing.T) {
	cd := time.Hour
	base := mustCompare(t, WebPage, "", State{}, page("a"), t0, nil, cd).State
	last := t0
	// A change two minutes after a run is held, and a second one merges in.
	r := mustCompare(t, WebPage, "", base, page("b"), t0.Add(2*time.Minute), &last, cd)
	if r.Outcome != Held {
		t.Fatalf("got %s", r.Outcome)
	}
	r = mustCompare(t, WebPage, "", r.State, page("c"), t0.Add(3*time.Minute), &last, cd)
	if r.Outcome != Held || r.State.Pending != "- a\n+ b\n- b\n+ c" {
		t.Fatalf("got %s %q", r.Outcome, r.State.Pending)
	}
	// Nothing new, still inside the cooldown: waits.
	r = mustCompare(t, WebPage, "", r.State, page("c"), t0.Add(30*time.Minute), &last, cd)
	if r.Outcome != Unchanged || r.State.Pending == "" {
		t.Fatalf("got %s %q", r.Outcome, r.State.Pending)
	}
	// Nothing new, cooldown over: the held change is delivered once.
	r = mustCompare(t, WebPage, "", r.State, page("c"), t0.Add(61*time.Minute), &last, cd)
	if r.Outcome != Changed || r.Change != "- a\n+ b\n- b\n+ c" || r.State.Pending != "" {
		t.Fatalf("got %s %q %q", r.Outcome, r.Change, r.State.Pending)
	}
}

func TestFeedHeldThenReleased(t *testing.T) {
	cd := time.Hour
	last := t0
	base := mustCompare(t, Feed, "", State{}, feedBody("a"), t0, nil, cd).State
	r := mustCompare(t, Feed, "", base, feedBody("b", "a"), t0.Add(time.Minute), &last, cd)
	if r.Outcome != Held || !strings.Contains(r.State.Pending, "tb") {
		t.Fatalf("got %+v", r)
	}
	r = mustCompare(t, Feed, "", r.State, feedBody("b", "a"), t0.Add(2*time.Hour), &last, cd)
	if r.Outcome != Changed || !strings.Contains(r.Change, "tb") || r.State.Pending != "" {
		t.Fatalf("got %+v", r)
	}
}

func TestPendingMergeCut(t *testing.T) {
	var lines []string
	for i := range 1000 {
		lines = append(lines, fmt.Sprintf("held line %04d", i))
	}
	pending := strings.Join(lines, "\n")
	got := merge(pending, "- new")
	if len(got) > maxChange || !strings.HasPrefix(got, "held line 0000\n") || !strings.HasSuffix(got, "\n… (cut)") {
		t.Fatalf("merge: len %d", len(got))
	}
	if merge("", "- new") != "- new" {
		t.Fatal("merge with no pending")
	}
}

// A look that finds nothing new, a body the same or a 304, runs a held
// change once the cooldown has passed, and keeps it held before.
func TestReleaseRunsAHeldChangeOnlyPastTheCooldown(t *testing.T) {
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	ran := now.Add(-30 * time.Minute)
	since := now.Add(-time.Hour)
	held := State{Hash: "h", Pending: "+ $15", ErrorsSince: &since}
	for _, c := range []struct {
		name    string
		st      State
		at      time.Time
		lastRun *time.Time
		want    string
		change  string
	}{
		{"within the cooldown", held, now, &ran, Unchanged, ""},
		{"at its end", held, ran.Add(time.Hour), &ran, Changed, "+ $15"},
		{"past it", held, now.Add(time.Hour), &ran, Changed, "+ $15"},
		{"never run", held, now, nil, Changed, "+ $15"},
		{"nothing held", State{Hash: "h"}, now, nil, Unchanged, ""},
	} {
		r := Release(c.st, c.at, c.lastRun, time.Hour)
		stillHeld := c.want == Unchanged && c.st.Pending != ""
		if r.Outcome != c.want || r.Change != c.change || r.State.ErrorsSince != nil || stillHeld != (r.State.Pending != "") {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}
