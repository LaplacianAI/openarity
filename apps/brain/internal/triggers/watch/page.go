package watch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipped are the elements whose text is not what a reader sees.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true, atom.Template: true,
}

// blocks are the elements that start a new line, so "<li>a</li><li>b</li>"
// is two lines and a change to one of them shows as one line.
var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Li: true, atom.Ul: true, atom.Ol: true, atom.Br: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Tr: true, atom.Table: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Nav: true, atom.Aside: true, atom.Main: true, atom.Blockquote: true,
	atom.Pre: true, atom.Hr: true, atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Form: true,
	atom.Figure: true, atom.Figcaption: true, atom.Address: true, atom.Fieldset: true,
}

// extractPage returns the text a reader of the page would see, one line per
// block, or the text of each node a selector matches.
func extractPage(body []byte, selector string) (string, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", errors.New("the page could not be read as HTML")
	}
	if selector != "" {
		sel, err := cascadia.Compile(selector)
		if err != nil {
			return "", fmt.Errorf("selector is not valid CSS: %w", err)
		}
		var lines []string
		for _, n := range sel.MatchAll(doc) {
			var b lineBuilder
			b.walk(n)
			if text := strings.Join(strings.Fields(strings.Join(b.all(), " ")), " "); text != "" {
				lines = append(lines, text)
			}
		}
		if len(lines) == 0 {
			return "", errors.New("the selector matched nothing")
		}
		return strings.Join(lines, "\n"), nil
	}
	root := doc
	if body := findBody(doc); body != nil {
		root = body
	}
	var b lineBuilder
	b.walk(root)
	return strings.Join(b.all(), "\n"), nil
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findBody(c); found != nil {
			return found
		}
	}
	return nil
}

// lineBuilder collects text into lines, closing one at every block element.
type lineBuilder struct {
	lines []string
	cur   strings.Builder
}

func (b *lineBuilder) flush() {
	if line := strings.Join(strings.Fields(b.cur.String()), " "); line != "" {
		b.lines = append(b.lines, line)
	}
	b.cur.Reset()
}

func (b *lineBuilder) all() []string {
	b.flush()
	return b.lines
}

func (b *lineBuilder) walk(n *html.Node) {
	if n.Type == html.TextNode {
		b.cur.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode {
		if skipped[n.DataAtom] {
			return
		}
		if blocks[n.DataAtom] {
			b.flush()
			defer b.flush()
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.walk(c)
	}
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// comparePage reports the new state of a page and what changed in it.
func comparePage(old State, text string) (next State, change string, changed, baseline bool) {
	next = State{Hash: hashText(text), Text: text}
	if len(text) > maxText {
		next.Text = ""
	}
	switch old.Hash {
	case "":
		return next, "", false, true
	case next.Hash:
		return next, "", false, false
	}
	// The old text was not kept when it was too long, so every line of the
	// new text is shown as added: that is all that can be said.
	oldText := old.Text
	if oldText == "" {
		return next, cut(strings.Join(prefixed("+ ", splitLines(text)), "\n")), true, false
	}
	removed, added := diffLines(splitLines(oldText), splitLines(text))
	if len(removed) == 0 && len(added) == 0 {
		return next, "the lines were reordered", true, false
	}
	return next, cut(strings.Join(append(prefixed("- ", removed), prefixed("+ ", added)...), "\n")), true, false
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// prefixed renders each line with a prefix.
func prefixed(prefix string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return out
}

// diffLines is a multiset difference: a line that was there twice and is
// there once is one removal. Removals keep the old order and additions the
// new one.
func diffLines(old, cur []string) (removed, added []string) {
	return minus(old, cur), minus(cur, old)
}

// minus returns the lines of a that b has no copy left to cancel.
func minus(a, b []string) []string {
	left := make(map[string]int, len(b))
	for _, l := range b {
		left[l]++
	}
	var out []string
	for _, l := range a {
		if left[l] > 0 {
			left[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}
