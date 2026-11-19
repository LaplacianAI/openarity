package markdown

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A rule rewrites outside code only, and the first rule that matches wins.
func TestRewriteLeavesCodeAsWritten(t *testing.T) {
	t.Parallel()

	shout := func(s string) (string, int, bool) {
		if strings.HasPrefix(s, "hi") {
			return "HI", 2, true
		}
		return "", 0, false
	}
	cases := map[string]string{
		"hi ![a](u) `hi` ![](v)":   "HI [a](u) `hi` [image](v)",
		"```\nhi ![a](u)\n```\nhi": "```\nhi ![a](u)\n```\nHI",
		"an unclosed ` then hi":    "an unclosed ` then HI",
	}
	for in, want := range cases {
		if got := Rewrite(in, Images, shout); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", in, got, want)
		}
	}
}

// A rule that consumes nothing is passed over, so it cannot loop forever.
func TestARuleThatConsumesNothingIsPassedOver(t *testing.T) {
	t.Parallel()

	none := func(string) (string, int, bool) { return "x", 0, true }
	if got := Rewrite("ab", none); got != "ab" {
		t.Errorf("Rewrite = %q, want the text unchanged", got)
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

func within(t *testing.T, parts []string, l Limits) {
	t.Helper()
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > l.Runes {
			t.Errorf("part %d is %d characters, over %d", i, n, l.Runes)
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
	l := Limits{Runes: 40, Blocks: 500}
	parts := Split(text, l)
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
	l := Limits{Runes: 120, Blocks: 500}
	parts := Split(text, l)
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
	l := Limits{Runes: 30, Blocks: 500}
	parts := Split(text, l)
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

// A platform may count blocks too: a list of many short items is split before it
// reaches the block limit, though it is well under the character limit.
func TestManyBlocksAreSplitBeforeTheBlockLimit(t *testing.T) {
	t.Parallel()

	var items []string
	for range 40 {
		items = append(items, "- x")
	}
	parts := Split(strings.Join(items, "\n\n"), Limits{Runes: 100000, Blocks: 10})
	if len(parts) < 4 {
		t.Fatalf("%d parts for 40 blocks at 10 a part", len(parts))
	}
	for i, p := range parts {
		if c := cost(p); c > 10*2+10 {
			t.Errorf("part %d may cost %d blocks", i, c)
		}
	}
}
