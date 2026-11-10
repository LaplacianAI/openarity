package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeLeavesCodeAsWritten(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"![a](u)":                          "[a](u)",
		"![](u)":                           "[image](u)",
		"x <script>y</script>":             "x &lt;script>y&lt;/script>",
		"`<b>` and ![a](u) and `` ` `` <":  "`<b>` and [a](u) and `` ` `` &lt;",
		"an unclosed ` then <":             "an unclosed ` then &lt;",
		"```html\n<b>![a](u)</b>\n```\n<":  "```html\n<b>![a](u)</b>\n```\n&lt;",
		"~~~\n```go\n<x>\n~~~\n<":          "~~~\n```go\n<x>\n~~~\n&lt;",
		"```\n<x>\n```go\n<y>\n```\n<":     "```\n<x>\n```go\n<y>\n```\n&lt;",
		"    ```\n<":                       "    ```\n&lt;",
		"no change at all, **bold** | t |": "no change at all, **bold** | t |",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAShortAnswerIsOnePartExactly(t *testing.T) {
	t.Parallel()

	text := "# Title\n\nA paragraph\nover two lines.\n\n- a\n- b"
	if got := Split(text); len(got) != 1 || got[0] != text {
		t.Errorf("Split = %q", got)
	}
	if got := Split("  \n\n "); got != nil {
		t.Errorf("an empty answer split into %q", got)
	}
}

// fenced reports whether every fence a part opens, it closes.
func fenced(part string) bool {
	open := ""
	for _, line := range strings.Split(part, "\n") {
		if f, ok := fenceOf(line); ok {
			switch {
			case open == "":
				open = f
			case closes(line, open):
				open = ""
			}
		}
	}
	return open == ""
}

func within(t *testing.T, parts []string, l limits) {
	t.Helper()
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > l.runes {
			t.Errorf("part %d is %d characters, over %d", i, n, l.runes)
		}
		if !fenced(p) {
			t.Errorf("part %d leaves a code block open:\n%s", i, p)
		}
	}
}

// Blocks are kept whole: a cut falls between them, and the parts are the
// answer again when joined.
func TestASplitFallsBetweenBlocks(t *testing.T) {
	t.Parallel()

	blocks := []string{"para one is here", "- item\n- item", "```\ncode\n\nmore code\n```", "| a | b |\n|---|---|\n| 1 | 2 |"}
	text := strings.Join(blocks, "\n\n")
	l := limits{runes: 40, blocks: 500}
	parts := split(text, l)
	within(t, parts, l)
	if strings.Join(parts, "\n\n") != text {
		t.Errorf("joined parts %q are not the answer", parts)
	}
	for _, p := range parts {
		for _, b := range blocks {
			if strings.Contains(text, b) && !strings.Contains(strings.Join(parts, "\n\n"), b) {
				t.Errorf("block %q was cut", b)
			}
		}
		_ = p
	}
}

// One code block bigger than a message is cut by lines, every piece still
// a code block of the same language.
func TestABigCodeBlockIsCutIntoCodeBlocks(t *testing.T) {
	t.Parallel()

	var lines []string
	for range 30 {
		lines = append(lines, "fmt.Println(\"line\")")
	}
	text := "```go\n" + strings.Join(lines, "\n") + "\n```"
	l := limits{runes: 120, blocks: 500}
	parts := split(text, l)
	if len(parts) < 2 {
		t.Fatalf("%d parts, want the block cut", len(parts))
	}
	within(t, parts, l)
	got := 0
	for _, p := range parts {
		if !strings.HasPrefix(p, "```go\n") || !strings.HasSuffix(p, "\n```") {
			t.Errorf("a piece is not a go block:\n%s", p)
		}
		got += strings.Count(p, "fmt.Println")
	}
	if got != 30 {
		t.Errorf("%d lines survived, want 30", got)
	}
}

// A line longer than a whole message is cut at character boundaries, never
// inside a character.
func TestALineLongerThanAMessageIsCut(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("日本", 50)
	l := limits{runes: 30, blocks: 500}
	parts := split(text, l)
	within(t, parts, l)
	if strings.Join(parts, "") != text {
		t.Error("the cut lost or broke characters")
	}
	for _, p := range parts {
		if !utf8.ValidString(p) {
			t.Errorf("part %q is not valid UTF-8", p)
		}
	}
}

// Telegram counts blocks too: a list of many short items is split before it
// reaches the block limit, though it is well under the character limit.
func TestManyBlocksAreSplitBeforeTheBlockLimit(t *testing.T) {
	t.Parallel()

	var items []string
	for range 40 {
		items = append(items, "- x")
	}
	parts := split(strings.Join(items, "\n\n"), limits{runes: 100000, blocks: 10})
	if len(parts) < 4 {
		t.Fatalf("%d parts for 40 blocks at 10 a part", len(parts))
	}
	for i, p := range parts {
		if c := cost(p); c > 10*2+10 {
			t.Errorf("part %d may cost %d blocks", i, c)
		}
	}
}

func TestTheRealLimitsAreTelegrams(t *testing.T) {
	t.Parallel()

	if richLimits != (limits{runes: 32768, blocks: 500}) {
		t.Errorf("limits = %+v, want Telegram's 32768 characters and 500 blocks", richLimits)
	}
}
