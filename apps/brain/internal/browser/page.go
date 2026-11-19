package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	// settle is how long an action may take to finish loading what it
	// started; poll is how often it is asked.
	settle = 5 * time.Second
	poll   = 150 * time.Millisecond
	// releaseTimeout is how long letting a session go may take, on a context
	// of its own: the run's is done by then.
	releaseTimeout = 10 * time.Second
)

//go:embed snapshot.js
var snapshotJS string

// A Snapshot is a page as the model reads it.
type Snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Cut   bool   `json:"cut"`
	Refs  int    `json:"refs"`
	// Blocked is every request refused since the last snapshot.
	Blocked []string `json:"-"`
}

// A Page is one tab in a session's browser, every request it makes checked.
type Page struct {
	tab     context.Context
	cancel  func()
	guard   *guard
	release func(context.Context) error
}

// Start opens a tab in s's browser, reached over client. Its requests reach
// only hosts public accepts; required says whether a browser that will not
// have them checked is refused.
func Start(ctx context.Context, s Session, client *http.Client, public Public, required bool) (*Page, error) {
	// The tab outlives the call that opened it, until Close.
	alloc, cancelAlloc := NewAllocator(context.WithoutCancel(ctx), s.URL, client)
	tab, cancelTab := chromedp.NewContext(alloc)
	p := &Page{tab: tab, release: s.Release, cancel: func() {
		cancelTab()
		cancelAlloc()
	}}
	g, err := startGuard(tab, public, required)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.guard = g
	return p, nil
}

// Close shuts the tab and lets the session go. Safe on a nil Page.
func (p *Page) Close() {
	if p == nil {
		return
	}
	p.cancel()
	if p.release != nil {
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()
		_ = p.release(ctx)
	}
}

// within is the tab, stopped when ctx is: chromedp runs on the tab's context.
func (p *Page) within(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithCancel(p.tab)
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
		if strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
			return Snapshot{}, errors.New("the page is not at a public address, so it is not opened")
		}
		return Snapshot{}, fmt.Errorf("the page could not be opened: %w", err)
	}
	return p.read(c)
}

// Snapshot reads the page as it is now.
func (p *Page) Snapshot(ctx context.Context) (Snapshot, error) {
	c, done := p.within(ctx)
	defer done()
	return p.read(c)
}

const clickJS = `((ref) => {
  const el = document.querySelector('[data-oa-ref="' + ref + '"]');
  if (!el) return false;
  if (el.scrollIntoView) el.scrollIntoView({block: "center"});
  if (el.focus) el.focus();
  el.click();
  return true;
})(%d)`

// Click clicks the control numbered ref, waits for what it starts, and reads
// the page.
func (p *Page) Click(ctx context.Context, ref int) (Snapshot, error) {
	return p.act(ctx, ref, fmt.Sprintf(clickJS, ref))
}

const typeJS = `((ref, text, submit) => {
  const el = document.querySelector('[data-oa-ref="' + ref + '"]');
  if (!el) return false;
  if (el.focus) el.focus();
  if (el.isContentEditable) {
    el.textContent = text;
  } else {
    // React and its kind watch the native setter, not the property.
    const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype :
      el.tagName === "SELECT" ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    const set = Object.getOwnPropertyDescriptor(proto, "value");
    if (set && set.set) set.set.call(el, text); else el.value = text;
  }
  el.dispatchEvent(new Event("input", {bubbles: true}));
  el.dispatchEvent(new Event("change", {bubbles: true}));
  if (submit) {
    const form = el.form || (el.closest && el.closest("form"));
    if (form && form.requestSubmit) form.requestSubmit();
    else if (form) form.submit();
    else {
      el.dispatchEvent(new KeyboardEvent("keydown", {key: "Enter", code: "Enter", bubbles: true}));
      el.dispatchEvent(new KeyboardEvent("keyup", {key: "Enter", code: "Enter", bubbles: true}));
    }
  }
  return true;
})(%d, %s, %t)`

// Type puts text in the field numbered ref, and submits its form when
// submit says so, then reads the page.
func (p *Page) Type(ctx context.Context, ref int, text string, submit bool) (Snapshot, error) {
	return p.act(ctx, ref, fmt.Sprintf(typeJS, ref, jsString(text), submit))
}

// Screenshot is the visible page as a PNG.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	c, done := p.within(ctx)
	defer done()
	png, err := chromedp.Run(c, chromedp.CaptureScreenshot())
	if err != nil {
		return nil, fmt.Errorf("the browser took no screenshot: %w", err)
	}
	return png, nil
}

func (p *Page) act(ctx context.Context, ref int, js string) (Snapshot, error) {
	c, done := p.within(ctx)
	defer done()
	found, err := chromedp.Run(c, chromedp.Evaluate[bool](js))
	if err != nil {
		return Snapshot{}, fmt.Errorf("the page did not take the action: %w", err)
	}
	if !found {
		return Snapshot{}, fmt.Errorf("no control %d on this page: take a snapshot to see what is there", ref)
	}
	p.settle(c)
	return p.read(c)
}

// settle waits, up to its limit, for the page to finish loading anything an
// action started: complete on two asks in a row, after the first poll. An
// ask that fails is a page between documents.
func (p *Page) settle(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, settle)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	ready := 0
	for ready < 2 {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		state, err := chromedp.Run(ctx, chromedp.Evaluate[string]("document.readyState"))
		if err == nil && state == "complete" {
			ready++
		} else {
			ready = 0
		}
	}
}

func (p *Page) read(ctx context.Context) (Snapshot, error) {
	s, err := chromedp.Run(ctx, chromedp.Evaluate[Snapshot](snapshotJS))
	if err != nil {
		return Snapshot{}, fmt.Errorf("the page could not be read: %w", err)
	}
	if p.guard != nil {
		s.Blocked = p.guard.Blocked()
	}
	return s, nil
}

// jsString is s as a JavaScript string literal: JSON's string is one.
func jsString(s string) string {
	b, _ := json.Marshal(s) //nolint:errchkjson // a string always marshals
	return string(b)
}
