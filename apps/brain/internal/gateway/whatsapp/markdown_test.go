package whatsapp

import "testing"

func TestMarkdownBecomesWhatsAppsMarks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"**bold** and __bold__", "*bold* and *bold*"},
		{"*italic* and _italic_", "_italic_ and _italic_"},
		{"**bold** then *italic*", "*bold* then _italic_"},
		{"~~gone~~", "~gone~"},
		{"## Why it failed", "*Why it failed*"},
		{"# Title #", "*Title*"},
		{"* one\n+ two\n- three", "• one\n• two\n- three"},
		{"see [the logs](https://example.com/logs)", "see the logs (https://example.com/logs)"},
		{"![chart](https://example.com/c.png)", "chart (https://example.com/c.png)"},
		{"[https://example.com](https://example.com)", "https://example.com"},
		{"2 * 3 * 4", "2 * 3 * 4"},
		{"use `**not bold**` here", "use `**not bold**` here"},
		{"```\n**kept** *as is*\n```\n**after**", "```\n**kept** *as is*\n```\n*after*"},
		{"plain", "plain"},
	} {
		if got := Format(tc.in); got != tc.want {
			t.Errorf("Format(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
