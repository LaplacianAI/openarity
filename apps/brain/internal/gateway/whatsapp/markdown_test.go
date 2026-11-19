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

// WhatsApp shows no tables, so each row becomes a line of its own: its first
// cell in bold, the rest under their headers' names. Pipes that are not a
// table, with no rule line under them, stay as written.
func TestATableBecomesALineARow(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"the folders",
			"You have four folders:\n\n| Folder | Size |\n|---|---|\n| Personal | about 155 KB |\n| **Work** | about 100 KB |\n\nWant more?",
			"You have four folders:\n\n• *Personal* — Size: about 155 KB\n• *Work* — Size: about 100 KB\n\nWant more?",
		},
		{
			"aligned, no outer pipes, three columns",
			"Name | Owner | Status\n:--- | :---: | ---:\nbilling | ops | `down`",
			"• *billing* — Owner: ops, Status: `down`",
		},
		{"an escaped pipe", "| Cmd | Use |\n|---|---|\n| a \\| b | pipe |", "• *a | b* — Use: pipe"},
		{"a header alone", "| Folder | Size |\n|---|---|", "Folder · Size"},
		{"an empty first cell", "| A | B |\n|---|---|\n| | two |", "• B: two"},
		{"a cell beyond the header", "| A |\n|---|\n| one | extra |", "• *one* — extra"},
		{"a header with no name", "| A | |\n|---|---|\n| one | two |", "• *one* — two"},
		{"an empty cell", "| A | B | C |\n|---|---|---|\n| one | | three |", "• *one* — C: three"},
		{"no rule line", "a | b\nc | d", "a | b\nc | d"},
		{"in code", "```\n| a | b |\n|---|---|\n```", "```\n| a | b |\n|---|---|\n```"},
	} {
		if got := Format(tc.in); got != tc.want {
			t.Errorf("%s: Format(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
