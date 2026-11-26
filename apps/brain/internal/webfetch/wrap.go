package webfetch

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// An Attr is one attribute of an envelope's opening tag, cut to Max bytes
// after escaping when Max is above zero.
type Attr struct {
	Name, Value string
	Max         int
}

// Wrap puts text from the web in the envelope the model is told to read as
// data: <tag … end="end"> … </tag end="end">. end is random per call, so only
// the closing tag that carries it ends the text; anything in body that opens
// or closes tag is escaped, and every attribute is escaped and cut on a rune.
// web_fetch's pages, the browser's snapshots and a watcher's changes all come
// in it.
func Wrap(tag string, attrs []Attr, body, end string) string {
	opener := regexp.MustCompile(`(?i)<(/?` + regexp.QuoteMeta(tag) + `)`)
	var b strings.Builder
	b.WriteString("<" + tag)
	for _, a := range attrs {
		fmt.Fprintf(&b, ` %s="%s"`, a.Name, attribute(a.Value, a.Max))
	}
	fmt.Fprintf(&b, " end=\"%s\">\n%s\n</%s end=\"%s\">", end, opener.ReplaceAllString(body, "&lt;$1"), tag, end)
	return b.String()
}

// attribute is s escaped, then cut on a rune to at most n bytes when n is
// above zero: a cut after escaping can split an entity, never leave a quote.
func attribute(s string, n int) string {
	s = html.EscapeString(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
