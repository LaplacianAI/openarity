package plain

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

// page serves body as contentType on 127.0.0.1 and reads it back.
func page(t *testing.T, contentType string, body []byte) (webfetch.Page, error) {
	t.Helper()
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	})
	return fetch(t, onlyLoopback4, srv.URL+"/doc/page")
}

func mustPage(t *testing.T, contentType, body string) webfetch.Page {
	t.Helper()
	p, err := page(t, contentType, []byte(body))
	if err != nil {
		t.Fatalf("fetch %s: %v", contentType, err)
	}
	return p
}

func TestEachReadableTypeIsRead(t *testing.T) {
	t.Parallel()

	for contentType, tc := range map[string]struct{ body, want string }{
		"text/html; charset=utf-8": {"<h1>Release</h1><p>Notes</p>", "## Release\n\nNotes"},
		"application/xhtml+xml":    {"<html><body><h2>Two</h2></body></html>", "## Two"},
		"text/plain":               {"plain <b>stays</b>", "plain <b>stays</b>"},
		"text/markdown":            {"# Already\n\nmarkdown", "# Already\n\nmarkdown"},
		"application/json":         {`{"a":1}`, `{"a":1}`},
	} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			if p := mustPage(t, contentType, tc.body); strings.TrimSpace(p.Markdown) != tc.want {
				t.Errorf("markdown = %q, want %q", p.Markdown, tc.want)
			}
		})
	}
}

func TestAPDFIsRefusedNamingItsType(t *testing.T) {
	t.Parallel()

	_, err := page(t, "application/pdf", []byte("%PDF-1.7"))
	if err == nil || err.Error() != "the page is application/pdf, which web_fetch does not read" {
		t.Errorf("err = %v, want the type named", err)
	}
}

// Many real pages are over 2 MiB of HTML with their text near the top, so a
// long body is read as far as the limit and marked cut, not refused.
func TestAHeavyPageIsCutNotRefused(t *testing.T) {
	t.Parallel()

	body := "<html><body><h1>Top</h1><p>" + strings.Repeat("filler ", 3<<20/7) + "</p></body></html>"
	p, err := page(t, "text/html", []byte(body))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !p.Cut || !strings.Contains(p.Markdown, "# Top") {
		t.Errorf("cut %v, top kept %v; want cut with the top kept", p.Cut, strings.Contains(p.Markdown, "# Top"))
	}
}

func TestAPageUnderTheLimitIsNotCut(t *testing.T) {
	t.Parallel()

	if p := mustPage(t, "text/html", "<p>short</p>"); p.Cut {
		t.Error("a short page is marked cut")
	}
}

// The library drops script, style, noscript and iframe itself and leaks an
// svg's text; the probe in the ledger is why svg is named here.
func TestScriptsStylesFramesAndSVGAreDropped(t *testing.T) {
	t.Parallel()

	p := mustPage(t, "text/html", `<html><head><style>.x{color:red}</style><script>steal()</script></head>
<body><noscript>enable js</noscript><iframe src="https://evil.example"></iframe>
<svg><text>svgtext</text></svg><p>kept</p></body></html>`)
	for _, gone := range []string{"color:red", "steal()", "enable js", "evil.example", "svgtext"} {
		if strings.Contains(p.Markdown, gone) {
			t.Errorf("%q survived in %q", gone, p.Markdown)
		}
	}
	if !strings.Contains(p.Markdown, "kept") {
		t.Errorf("the page's text went too: %q", p.Markdown)
	}
}

func TestTheTitleIsThePagesTitle(t *testing.T) {
	t.Parallel()

	p := mustPage(t, "text/html", "<html><head><title>  Go 1.26\n Release Notes </title></head><body>x</body></html>")
	if p.Title != "Go 1.26 Release Notes" {
		t.Errorf("title = %q, want the title folded to one line", p.Title)
	}
}

func TestAPageWithNoTitleHasNone(t *testing.T) {
	t.Parallel()

	if p := mustPage(t, "text/html", "<p>untitled</p>"); p.Title != "" {
		t.Errorf("title = %q, want none", p.Title)
	}
	if p := mustPage(t, "text/plain", "<title>not html</title>"); p.Title != "" {
		t.Errorf("a text page has title %q", p.Title)
	}
}

func TestALatin1PageReadsAsUTF8(t *testing.T) {
	t.Parallel()

	latin1 := []byte("<p>caf\xe9</p>")
	for name, tc := range map[string]struct {
		contentType string
		body        []byte
	}{
		"from the header": {"text/html; charset=iso-8859-1", latin1},
		"from a meta tag": {"text/html", append([]byte(`<meta charset="iso-8859-1">`), latin1...)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := page(t, tc.contentType, tc.body)
			if err != nil || !strings.Contains(p.Markdown, "café") {
				t.Errorf("markdown %q err %v, want café", p.Markdown, err)
			}
		})
	}
}

func TestTablesStayTables(t *testing.T) {
	t.Parallel()

	p := mustPage(t, "text/html", "<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>")
	if !strings.Contains(p.Markdown, "| A | B |") || !strings.Contains(p.Markdown, "| 1 | 2 |") {
		t.Errorf("markdown = %q, want a table", p.Markdown)
	}
}

// A relative link is resolved against the address the page was read from,
// so the model is handed a link it can fetch next.
func TestRelativeLinksBecomeAbsolute(t *testing.T) {
	t.Parallel()

	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a href="/doc/next">next</a>`))
	})
	p, err := fetch(t, onlyLoopback4, srv.URL+"/doc/page")
	if err != nil || !strings.Contains(p.Markdown, "("+srv.URL+"/doc/next)") {
		t.Errorf("markdown %q err %v, want the link absolute", p.Markdown, err)
	}
}

// The cut keeps the start of a page, so the start has to be the page's
// content: without the article taken out first, a model reading a GitHub
// issue was handed menus and never reached the issue.
func TestTheArticleComesBeforeTheNavigation(t *testing.T) {
	t.Parallel()

	para := "<p>" + strings.Repeat("The release brings a new garbage collector, faster builds, and a smaller binary. ", 6) + "</p>"
	p := mustPage(t, "text/html", `<html><head><title>Notes</title></head><body>
<nav><a href="/home">Home</a> <a href="/menu">Menu item</a></nav><header><a href="/signin">Sign in</a></header>
<article><h2>Introduction</h2>`+para+para+para+`</article><footer><a href="/privacy">Privacy</a></footer></body></html>`)
	if !strings.HasPrefix(p.Markdown, "## Introduction") {
		t.Errorf("the page starts %q, want the article first", p.Markdown[:min(len(p.Markdown), 80)])
	}
	for _, chrome := range []string{"Menu item", "Sign in", "Privacy"} {
		if strings.Contains(p.Markdown, chrome) {
			t.Errorf("%q survived around the article", chrome)
		}
	}
}

// Readability finds no article in a page that is only an image or a
// script, and the whole page is converted instead of answering nothing.
func TestAPageWithNoArticleFallsBackToTheWholePage(t *testing.T) {
	t.Parallel()

	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><img src="/chart.png" alt="chart"></body></html>`))
	})
	p, err := fetch(t, onlyLoopback4, srv.URL+"/doc/page")
	if err != nil || !strings.Contains(p.Markdown, "![chart]("+srv.URL+"/chart.png)") {
		t.Errorf("markdown %q err %v, want the whole page", p.Markdown, err)
	}
}

// Latin-1 happens to survive being read as UTF-8 further down; KOI8-R does
// not, so this is the page that shows the declared charset is honoured.
// "мир" is \xcd\xc9\xd2 in KOI8-R.
func TestAKOI8RPageReadsAsUTF8(t *testing.T) {
	t.Parallel()

	koi8 := []byte("<p>\xcd\xc9\xd2</p>")
	for name, tc := range map[string]struct {
		contentType string
		body        []byte
	}{
		"html, from the header": {"text/html; charset=koi8-r", koi8},
		"html, from a meta tag": {"text/html", append([]byte(`<meta charset="koi8-r">`), koi8...)},
		"plain text":            {"text/plain; charset=koi8-r", []byte("\xcd\xc9\xd2")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := page(t, tc.contentType, tc.body)
			if err != nil || !strings.Contains(p.Markdown, "мир") {
				t.Errorf("markdown %q err %v, want мир", p.Markdown, err)
			}
		})
	}
}

// A page that names no charset is most often UTF-8: JSON always is, and many
// servers send text and markdown without one. The charset sniffer reads only
// the first KiB and falls back to windows-1252 when that is ASCII, which read
// "café" as "cafÃ©".
func TestAUTF8PageThatNamesNoCharsetReadsAsUTF8(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("a", 1100)
	for contentType, body := range map[string]string{
		"application/json": `{"pad":"` + prefix + `","word":"café ünïcode"}`,
		"text/markdown":    prefix + "\n\ncafé ünïcode",
		"text/plain":       prefix + "\ncafé ünïcode",
		"text/html":        "<p>" + prefix + "</p><p>café ünïcode</p>",
	} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			p, err := page(t, contentType, []byte(body))
			if err != nil || !strings.Contains(p.Markdown, "café ünïcode") || strings.Contains(p.Markdown, "Ã") {
				t.Errorf("err %v, markdown ends %q; want café ünïcode", err, p.Markdown[max(0, len(p.Markdown)-40):])
			}
		})
	}
}

// The cut at 2 MiB can fall inside a character; the half kept is dropped, not
// taken as a sign that the page is not UTF-8.
func TestAPageCutInsideACharacterStaysUTF8(t *testing.T) {
	t.Parallel()

	p, err := page(t, "text/plain", []byte("a"+strings.Repeat("é", 1<<20+10)))
	if err != nil || !p.Cut || !utf8.ValidString(p.Markdown) || strings.Contains(p.Markdown, "Ã") ||
		strings.Trim(p.Markdown, "aé") != "" {
		t.Errorf("err %v, cut %v, valid %v, ends %q", err, p.Cut, utf8.ValidString(p.Markdown), p.Markdown[len(p.Markdown)-8:])
	}
}

// An empty page is a page with no text, not an encoding that could not be read.
func TestAnEmptyPageIsNotAnError(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{"text/plain", "text/html; charset=utf-8"} {
		if p, err := page(t, contentType, nil); err != nil || strings.TrimSpace(p.Markdown) != "" {
			t.Errorf("%s: page %+v, err %v; want an empty page", contentType, p, err)
		}
	}
}

// The cut is exactly the 2 MiB the fetch reads, not a byte more.
func TestACutPageKeepsExactly2MiB(t *testing.T) {
	t.Parallel()

	p, err := page(t, "text/plain", []byte(strings.Repeat("a", 3<<20)))
	if err != nil || !p.Cut || len(p.Markdown) != 2<<20 {
		t.Errorf("err %v, cut %v, %d bytes; want exactly %d", err, p.Cut, len(p.Markdown), 2<<20)
	}
}

// A charset the header names wins, even over bytes that would also read as
// UTF-8: 0xC3 0xA9 is "Ã©" in Latin-1, and the page said Latin-1.
func TestADeclaredCharsetWinsOverBytesThatLookLikeUTF8(t *testing.T) {
	t.Parallel()

	p, err := page(t, "text/plain; charset=iso-8859-1", []byte("caf\xc3\xa9"))
	if err != nil || p.Markdown != "cafÃ©" {
		t.Errorf("markdown %q, err %v; want cafÃ©", p.Markdown, err)
	}
}
