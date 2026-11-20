package browser

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/chromedp"
)

// The limits are variables so a test can shorten them.
var (
	// startTimeout is how long a tab may take to open and be guarded.
	startTimeout = 20 * time.Second
	// callTimeout is the most one call on the page may take, whatever its
	// caller allows: a page that never finishes loading, or a script that
	// never returns, does not hold the run.
	callTimeout = 30 * time.Second
	// quiet is how long a page that has not begun to leave must stay quiet
	// after an action before it is read; settleTimeout is the most a page
	// that is leaving may take to arrive.
	quiet         = 900 * time.Millisecond
	settleTimeout = 15 * time.Second
	poll          = 150 * time.Millisecond
)

const (
	// releaseTimeout is how long letting a session go may take, on a context
	// of its own: the run's is done by then.
	releaseTimeout = 10 * time.Second
	// maxText is the most of a page's text a read keeps, in characters: the
	// script stops there, and it is held here too, as the page could say
	// otherwise.
	maxText = 200_000
	// maxTitle and maxURL are the most of a page's title and address shown,
	// in bytes.
	maxTitle = 300
	maxURL   = 2048
	// maxScreenshot is the largest picture handed to the model.
	maxScreenshot = 8 << 20
)

//go:embed snapshot.js
var snapshotJS string

// ErrNotTaken is an action the page did not take because it named nothing
// there: no control by that number, no option by that name. The page stayed
// where it was.
var ErrNotTaken = errors.New("the action named nothing on the page")

type notTaken string

func (e notTaken) Error() string { return string(e) }
func (notTaken) Unwrap() error   { return ErrNotTaken }

// ErrOffSite is an action refused because the page, or the form it would
// send, is on a site other than those it may be taken on. Nothing was done.
var ErrOffSite = errors.New("the action is on another site")

type offSite string

func (e offSite) Error() string { return string(e) }
func (offSite) Unwrap() error   { return ErrOffSite }

// A Snapshot is a page as the model reads it.
type Snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Cut   bool   `json:"cut"`
	Refs  int    `json:"refs"`
	// Blocked names requests refused since the last read, at most a hundred;
	// Refused counts them all, and Shed those held back because the page
	// asked for more at once than are judged.
	Blocked []string `json:"-"`
	Refused int      `json:"-"`
	Shed    int      `json:"-"`
}

// A Page is one tab in a session's browser, every request it makes checked.
type Page struct {
	tab     context.Context
	abort   func()
	guard   *guard
	release func(context.Context) error
	once    sync.Once
}

// Start opens a tab in s's browser, reached over client. Its requests reach
// only hosts public accepts; required says whether a browser that will not
// have them checked is refused, and a browser that is required to be checked
// is the team's own, so the tab gets a browser context of its own: cookies
// and storage are not carried from one run to the next.
func Start(ctx context.Context, s Session, client *http.Client, public Public, required bool) (*Page, error) {
	// The tab outlives the call that opened it, until Close.
	alloc, cancelAlloc := NewAllocator(context.WithoutCancel(ctx), s.URL, client)
	var opts []chromedp.ContextOption
	if required {
		opts = append(opts, chromedp.WithNewBrowserContext())
	}
	tab, cancelTab := chromedp.NewContext(alloc, opts...)
	p := &Page{tab: tab, release: s.Release, abort: func() {
		cancelTab()
		cancelAlloc()
	}}

	watch := time.AfterFunc(startTimeout, p.abort)
	unhook := context.AfterFunc(ctx, p.abort)
	g, err := startGuard(tab, public, required)
	// The watchdog firing cancels the tab, so tab.Err says so.
	watch.Stop()
	unhook()
	if err == nil && (ctx.Err() != nil || tab.Err() != nil) {
		err = errors.New("the browser did not start in time")
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	p.guard = g
	return p, nil
}

// Close shuts the tab and lets the session go, once. Safe on a nil Page. The
// tab is closed, and its browser context disposed, before a hosted session is
// let go, for as long as releaseTimeout allows.
func (p *Page) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			_ = chromedp.Cancel(p.tab)
		}()
		select {
		case <-closed:
		case <-time.After(releaseTimeout):
		}
		p.abort()
		if p.release != nil {
			ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
			defer cancel()
			_ = p.release(ctx)
		}
	})
}

// Unguarded is a hosted browser that would not pause its requests.
func (p *Page) Unguarded() bool { return p.guard != nil && p.guard.unguarded }

// Uncontained is a page not kept in its tab: a hosted browser that would not
// take containJS, where a page may open a socket or a popup, or a popup that
// could not be closed.
func (p *Page) Uncontained() bool {
	if p.guard == nil {
		return false
	}
	p.guard.mu.Lock()
	defer p.guard.mu.Unlock()
	return p.guard.uncontained
}

// within is the tab, stopped when ctx is or when the call has taken its
// limit: chromedp runs on the tab's context.
func (p *Page) within(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithTimeout(p.tab, callTimeout)
	stop := context.AfterFunc(ctx, cancel)
	return c, func() {
		stop()
		cancel()
	}
}

// Open goes to address and reads the page there.
func (p *Page) Open(ctx context.Context, address string) (Snapshot, error) {
	c, done := p.within(ctx)
	defer done()
	if err := chromedp.Do(c, chromedp.Navigate(address)); err != nil {
		return Snapshot{}, navigationError(err)
	}
	return p.read(c)
}

func navigationError(err error) error {
	if strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
		return errors.New("the page is not at a public address, so it is not opened")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("the page did not finish loading in time")
	}
	return fmt.Errorf("the page could not be opened: %w", err)
}

// Snapshot reads the page as it is now.
func (p *Page) Snapshot(ctx context.Context) (Snapshot, error) {
	c, done := p.within(ctx)
	defer done()
	return p.read(c)
}

// markJS marks the document an action starts in, and notes when it begins to
// leave it: settle tells a page that is going somewhere from one that stays.
const markJS = `window.__oaMark = %s; window.__oaLeaving = false;
addEventListener("beforeunload", () => { window.__oaLeaving = true; }, {once: true});`

// leaves notes that the action is starting a navigation, for a browser that
// fires no beforeunload: a link to another page, or a form, whose event no
// script of the page's cancelled.
const leavesJS = `const leaves = (el, kind) => {
  let prevented = false;
  addEventListener(kind, (e) => { prevented = e.defaultPrevented; }, {once: true});
  return () => {
    if (prevented) return;
    const a = el.closest && el.closest("a[href]");
    if (kind === "submit" || el.form && (el.type === "submit" || el.tagName === "BUTTON")) {
      window.__oaLeaving = true;
    } else if (a) {
      const to = new URL(a.href, location.href);
      if ((to.protocol === "http:" || to.protocol === "https:") &&
        (to.origin + to.pathname + to.search) !== (location.origin + location.pathname + location.search)) {
        window.__oaLeaving = true;
      }
    }
  };
};`

// sitesJS keeps an action to the sites it may be taken on: the page must be
// on one, and a form it sends must go to one. sites is null for anywhere.
const sitesJS = `const off = (host) => sites !== null && !sites.includes(String(host).toLowerCase());
const sends = (form, by) => {
  try {
    const raw = (by && by.getAttribute && by.getAttribute("formaction")) || form.getAttribute("action") || "";
    return new URL(raw, document.baseURI).hostname;
  } catch (e) { return ""; }
};
if (off(location.hostname)) return "elsewhere " + location.hostname;`

const clickJS = `((ref, sites) => {
  ` + leavesJS + `
  ` + sitesJS + `
  const el = document.querySelector('[data-oa-ref="' + ref + '"]');
  if (!el) return "missing";
  if (el.form && (el.type === "submit" || el.type === "image") && off(sends(el.form, el))) {
    return "sends elsewhere " + sends(el.form, el);
  }
  if (el.target) el.target = "_self";
  if (el.scrollIntoView) el.scrollIntoView({block: "center"});
  if (el.focus) el.focus();
  const noted = leaves(el, "click");
  el.click();
  noted();
  return "done";
})(%d, %s)`

// Click clicks the control numbered ref, waits for what it starts, and reads
// the page. With sites, only on a page on one of them, and never a button
// that sends its form to another; nil is anywhere.
func (p *Page) Click(ctx context.Context, ref int, sites []string) (Snapshot, error) {
	return p.act(ctx, ref, fmt.Sprintf(clickJS, ref, jsSites(sites)))
}

const typeJS = `((ref, text, submit, sites) => {
  ` + leavesJS + `
  ` + sitesJS + `
  const el = document.querySelector('[data-oa-ref="' + ref + '"]');
  if (!el) return "missing";
  const form = el.form || (el.closest && el.closest("form"));
  if (submit && form && off(sends(form, null))) return "sends elsewhere " + sends(form, null);
  if (el.focus) el.focus();
  if (el.tagName === "SELECT") {
    const want = text.trim().toLowerCase();
    const i = Array.from(el.options).findIndex((o) =>
      o.textContent.trim().toLowerCase() === want || o.value.toLowerCase() === want);
    if (i < 0) return "no option";
    el.selectedIndex = i;
  } else if (el.isContentEditable) {
    el.textContent = text;
  } else {
    // React and its kind watch the native setter, not the property.
    const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const set = Object.getOwnPropertyDescriptor(proto, "value");
    if (set && set.set) set.set.call(el, text); else el.value = text;
  }
  el.dispatchEvent(new Event("input", {bubbles: true}));
  el.dispatchEvent(new Event("change", {bubbles: true}));
  if (submit) {
    if (form && form.target) form.target = "_self";
    if (form) {
      const noted = leaves(form, "submit");
      if (form.requestSubmit) form.requestSubmit(); else form.submit();
      noted();
    } else {
      el.dispatchEvent(new KeyboardEvent("keydown", {key: "Enter", code: "Enter", bubbles: true}));
      el.dispatchEvent(new KeyboardEvent("keyup", {key: "Enter", code: "Enter", bubbles: true}));
    }
  }
  return "done";
})(%d, %s, %t, %s)`

// Type puts text in the field numbered ref, choosing the option it names in
// a select, and submits its form when submit says so, then reads the page.
// sites keeps it as Click's does.
func (p *Page) Type(ctx context.Context, ref int, text string, submit bool, sites []string) (Snapshot, error) {
	return p.act(ctx, ref, fmt.Sprintf(typeJS, ref, jsString(text), submit, jsSites(sites)))
}

// jsSites is sites as the scripts take them: lower case, or null for any.
func jsSites(sites []string) string {
	if sites == nil {
		return "null"
	}
	lower := make([]string, len(sites))
	for i, s := range sites {
		lower[i] = strings.ToLower(s)
	}
	b, _ := json.Marshal(lower) //nolint:errchkjson // strings always marshal
	return string(b)
}

// A Picture is the visible page as a PNG, and where it is.
type Picture struct {
	PNG        []byte
	URL, Title string
}

// Screenshot is the visible page as a picture.
func (p *Page) Screenshot(ctx context.Context) (Picture, error) {
	c, done := p.within(ctx)
	defer done()
	png, err := chromedp.Run(c, chromedp.CaptureScreenshot())
	if err != nil {
		return Picture{}, fmt.Errorf("the browser took no screenshot: %w", err)
	}
	if len(png) > maxScreenshot {
		return Picture{}, fmt.Errorf("the screenshot is %d MiB, more than the %d MiB a picture may be", len(png)>>20, maxScreenshot>>20)
	}
	at, err := chromedp.Run(c, chromedp.Evaluate[[]string](`[location.href, document.title]`))
	if err != nil || len(at) != 2 {
		at = []string{"", ""}
	}
	return Picture{PNG: png, URL: clip(at[0], maxURL), Title: clip(at[1], maxTitle)}, nil
}

func (p *Page) act(ctx context.Context, ref int, js string) (Snapshot, error) {
	c, done := p.within(ctx)
	defer done()
	mark := rand.Text()
	if _, err := chromedp.Run(c, chromedp.Evaluate[any](fmt.Sprintf(markJS, jsString(mark)))); err != nil {
		return Snapshot{}, fmt.Errorf("the page did not take the action: %w", err)
	}
	said, err := chromedp.Run(c, chromedp.Evaluate[string](js))
	if err != nil {
		return Snapshot{}, fmt.Errorf("the page did not take the action: %w", err)
	}
	switch {
	case said == "missing":
		return Snapshot{}, notTaken(fmt.Sprintf("no control %d on this page: take a snapshot to see what is there", ref))
	case said == "no option":
		return Snapshot{}, notTaken(fmt.Sprintf("control %d has no option by that name or value", ref))
	case strings.HasPrefix(said, "elsewhere "):
		return Snapshot{}, offSite(fmt.Sprintf("the page is now on %s, not the site it was opened for, so nothing is done on it: open it first",
			clip(strings.TrimPrefix(said, "elsewhere "), maxTitle)))
	case strings.HasPrefix(said, "sends elsewhere "):
		return Snapshot{}, offSite(fmt.Sprintf("control %d would send its form to %s, not the site the page was opened for, so it is not sent: open that site first",
			ref, clip(strings.TrimPrefix(said, "sends elsewhere "), maxTitle)))
	}
	p.settle(c, mark)
	return p.read(c)
}

// settle waits for what an action started. A page still in the document it
// marked, and not leaving it, is read once it has been quiet a moment. One
// that began to leave, or has arrived in another document, is read once the
// new one has loaded, up to settleTimeout. An ask that fails is a page
// between documents.
func (p *Page) settle(ctx context.Context, mark string) {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	began := time.Now()
	ready := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var s struct {
			Same    bool   `json:"same"`
			Leaving bool   `json:"leaving"`
			State   string `json:"state"`
		}
		raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](fmt.Sprintf(
			`JSON.stringify({same: window.__oaMark === %s, leaving: !!window.__oaLeaving, state: document.readyState})`,
			jsString(mark))))
		if err != nil || json.Unmarshal([]byte(raw), &s) != nil {
			ready = 0
			continue
		}
		switch {
		case s.Same && !s.Leaving:
			if s.State == "complete" && time.Since(began) >= quiet {
				return
			}
		case s.Same:
			// Leaving, not yet arrived.
		case s.State == "complete":
			if ready++; ready >= 2 {
				return
			}
		default:
			ready = 0
		}
	}
}

func (p *Page) read(ctx context.Context) (Snapshot, error) {
	s, err := chromedp.Run(ctx, chromedp.Evaluate[Snapshot](snapshotJS))
	if err != nil {
		return Snapshot{}, fmt.Errorf("the page could not be read: %w", err)
	}
	s = clamp(s)
	if p.guard != nil {
		s.Blocked, s.Refused, s.Shed = p.guard.Blocked()
	}
	return s, nil
}

// clamp holds a read to maxText characters, whatever the page's script said,
// and its title and address to what a heading needs: a page sets both.
func clamp(s Snapshot) Snapshot {
	if utf8.RuneCountInString(s.Text) > maxText {
		s.Text, s.Cut = string([]rune(s.Text)[:maxText]), true
	}
	s.Title, s.URL = clip(s.Title, maxTitle), clip(s.URL, maxURL)
	return s
}

// jsString is s as a JavaScript string literal: JSON's string is one.
func jsString(s string) string {
	b, _ := json.Marshal(s) //nolint:errchkjson // a string always marshals
	return string(b)
}
