package telegram

import (
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/markdown"
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

func TestTheRealLimitsAreTelegrams(t *testing.T) {
	t.Parallel()

	if richLimits != (markdown.Limits{Runes: 32768, Blocks: 500}) {
		t.Errorf("limits = %+v, want Telegram's 32768 characters and 500 blocks", richLimits)
	}
}
