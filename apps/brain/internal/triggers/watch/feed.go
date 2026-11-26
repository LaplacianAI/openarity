package watch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type rssDoc struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

type atomDoc struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	ID        string     `xml:"id"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// item is one entry of a feed, whichever format it came in.
type item struct {
	ID, Title, Link, Date, Summary string
}

// parseFeed reads RSS 2.0 or Atom, newest first as the feed lists them.
func parseFeed(body []byte) ([]item, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	// A feed may declare an encoding Go does not know. Reading it as is keeps
	// the check working, and the text only has to be the same each time.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var start xml.StartElement
	for start.Name.Local == "" {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("the feed is neither RSS nor Atom")
		}
		if s, ok := tok.(xml.StartElement); ok {
			start = s
		}
	}
	var items []item
	switch start.Name.Local {
	case "rss":
		var doc rssDoc
		if err := dec.DecodeElement(&doc, &start); err != nil {
			return nil, errors.New("the feed could not be read as RSS")
		}
		for _, it := range doc.Items {
			items = append(items, makeItem(it.GUID, it.Title, strings.TrimSpace(it.Link), it.PubDate, it.Description))
		}
	case "feed":
		var doc atomDoc
		if err := dec.DecodeElement(&doc, &start); err != nil {
			return nil, errors.New("the feed could not be read as Atom")
		}
		for _, e := range doc.Entries {
			date := e.Updated
			if strings.TrimSpace(date) == "" {
				date = e.Published
			}
			summary := e.Summary
			if strings.TrimSpace(summary) == "" {
				summary = e.Content
			}
			items = append(items, makeItem(e.ID, e.Title, e.link(), date, summary))
		}
	default:
		return nil, errors.New("the feed is neither RSS nor Atom")
	}
	return items, nil
}

// link is the entry's own page: the alternate link, or one with no rel.
func (e atomEntry) link() string {
	for _, l := range e.Links {
		if (l.Rel == "" || l.Rel == "alternate") && strings.TrimSpace(l.Href) != "" {
			return strings.TrimSpace(l.Href)
		}
	}
	return ""
}

// makeItem derives the id: the feed's own, else the link, else a hash of what
// identifies the entry to a reader.
func makeItem(id, title, link, date, summary string) item {
	it := item{
		ID:      strings.TrimSpace(id),
		Title:   collapse(title),
		Link:    link,
		Date:    strings.TrimSpace(date),
		Summary: cutRunes(plainText(summary), maxSummary),
	}
	switch {
	case it.ID != "":
	case it.Link != "":
		it.ID = it.Link
	default:
		sum := sha256.Sum256([]byte(it.Title + "\n" + it.Date))
		it.ID = "sha256:" + hex.EncodeToString(sum[:])
	}
	return it
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// plainText strips tags from a summary, which feeds write as HTML.
func plainText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapse(b.String())
		case html.TextToken:
			b.Write(z.Text())
		case html.CommentToken, html.DoctypeToken:
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			// A tag is a break between words: "a<br>b" must not become "ab".
			b.WriteByte(' ')
		}
	}
}

// cutRunes shortens s to at most max bytes without splitting a rune.
func cutRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// compareFeed reports the new state of a feed and the items it did not
// have before.
func compareFeed(old State, items []item) (next State, change string, changed, baseline bool) {
	ids := make([]string, 0, len(items))
	in := make(map[string]bool, len(items))
	for _, it := range items {
		if !in[it.ID] {
			in[it.ID] = true
			ids = append(ids, it.ID)
		}
	}
	// Ids that fell out of the feed stay remembered, so an item that comes
	// back is not announced twice.
	for _, id := range old.Seen {
		if !in[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxSeen {
		ids = ids[:maxSeen]
	}
	next = State{Seen: ids}
	if old.Seen == nil {
		return next, "", false, true
	}

	was := make(map[string]bool, len(old.Seen))
	for _, id := range old.Seen {
		was[id] = true
	}
	var fresh []item
	for _, it := range items {
		if !was[it.ID] {
			was[it.ID] = true
			fresh = append(fresh, it)
		}
	}
	if len(fresh) == 0 {
		return next, "", false, false
	}
	return next, renderItems(fresh), true, false
}

// renderItems writes at most 20 items as short blocks, adding whole items
// until the next would pass the limit.
func renderItems(items []item) string {
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	var out string
	for i, it := range items {
		block := it.render()
		switch {
		case i == 0:
			out = cut(block)
		case len(out)+2+len(block) > maxChange:
			return out
		default:
			out += "\n\n" + block
		}
	}
	return out
}

func (it item) render() string {
	var parts []string
	for _, p := range []string{it.Title, it.Link, it.Date, it.Summary} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}
