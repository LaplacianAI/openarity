package cli

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Ordinary text, a paragraph included, comes through as the lines it is.
// Lines exists because Safe would quote all of this into one string.
func TestLinesLeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()

	for in, want := range map[string][]string{
		"":                             {""},
		"one line":                     {"one line"},
		"é ü 漢字 ⏺ — “quoted”":          {"é ü 漢字 ⏺ — “quoted”"},
		"two\nlines":                   {"two", "lines"},
		"crlf\r\nline\r\n":             {"crlf", "line", ""},
		"a\n\nb":                       {"a", "", "b"},
		"emoji 👩\u200d💻 keeps its ZWJ": {"emoji 👩\u200d💻 keeps its ZWJ"},
	} {
		if got := Lines(in); strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("Lines(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each attack a run's text could carry, and what is left once it is taken
// out. A model repeats what a tool fed it, so any of these can arrive in an
// answer, a result or an error.
func TestLinesTakesOutWhatATerminalWouldObey(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in   string
		want []string
	}{
		"clear the screen":           {"before\x1b[2Jafter", []string{"beforeafter"}},
		"move the cursor home":       {"a\x1b[Hb\x1b[10;20Hc", []string{"abc"}},
		"colour":                     {"\x1b[31;1mred\x1b[0m", []string{"red"}},
		"a private CSI":              {"a\x1b[?1049hb", []string{"ab"}},
		"OSC 8 hyperlink, BEL":       {"\x1b]8;;https://evil.example\x07click\x1b]8;;\x07 here", []string{"click here"}},
		"OSC 8 hyperlink, ST":        {"\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", []string{"click"}},
		"OSC 52 clipboard":           {"ok\x1b]52;c;cm0gLXJmIH4K\x07done", []string{"okdone"}},
		"OSC 0 window title":         {"\x1b]0;pwned\x07text", []string{"text"}},
		"an OSC that never ends":     {"text\x1b]52;c;cm0gLXJmIH4K\nnext", []string{"text", "next"}},
		"an OSC cut off at the end":  {"text\x1b]0;pwn", []string{"text"}},
		"ESC inside an OSC":          {"\x1b]0;x\x1b[31mred", []string{"red"}},
		"DCS":                        {"a\x1bPq#0;2;0;0;0\x1b\\b", []string{"ab"}},
		"APC and PM":                 {"a\x1b_payload\x1b\\b\x1b^pm\x07c", []string{"abc"}},
		"reset the terminal":         {"a\x1bcb", []string{"ab"}},
		"save the cursor":            {"a\x1b7b\x1b8c", []string{"abc"}},
		"charset switch":             {"a\x1b(0b\x1b(Bc", []string{"abc"}},
		"ESC at the very end":        {"text\x1b", []string{"text"}},
		"ESC then a control":         {"a\x1b\x07b", []string{"ab"}},
		"C1 CSI":                     {"a\u009b2Jb", []string{"ab"}},
		"C1 OSC":                     {"a\u009d0;pwned\u009cb", []string{"ab"}},
		"C1 OSC ended by BEL":        {"a\u009d52;c;eA==\x07b", []string{"ab"}},
		"C1 DCS, SOS, PM, APC":       {"\u0090x\u009c1\u0098x\u009c2\u009ex\u009c3\u009fx\u009c4", []string{"1234"}},
		"a lone C1":                  {"a\u0085b\u0080c", []string{"abc"}},
		"bell and backspace":         {"a\x07b\x08\x08c", []string{"abc"}},
		"NUL and DEL":                {"a\x00b\x7fc", []string{"abc"}},
		"vertical tab and form feed": {"a\vb\fc", []string{"abc"}},
		"a carriage return overwrite": {
			"safe\rrm -rf ~ # looks like this", []string{"safe", "rm -rf ~ # looks like this"},
		},
		"a carriage return at the end":  {"text\r", []string{"text", ""}},
		"RLO":                           {"invoice\u202eexe.pdf", []string{"invoiceexe.pdf"}},
		"every bidi override":           {"a\u202ab\u202bc\u202cd\u202de\u202ef", []string{"abcdef"}},
		"every bidi isolate":            {"a\u2066b\u2067c\u2068d\u2069e", []string{"abcde"}},
		"bidi marks":                    {"a\u200eb\u200fc\u061cd", []string{"abcd"}},
		"line and paragraph separators": {"a\u2028b\u2029c", []string{"a", "b", "c"}},
		"invalid UTF-8":                 {"a\xffb\xc3", []string{"a�b�"}},
		"an overlong encoding":          {"a\xc0\x9bb", []string{"a��b"}},
		"a tab":                         {"a\tb", []string{"a    b"}},
	} {
		got := Lines(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
			t.Errorf("%s: Lines(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
		assertInert(t, name, got)
	}
}

// Whatever the bytes, nothing that comes out acts on a terminal. Every byte
// value, at every position next to an ESC and a C1 introducer.
func TestNoInputGetsAControlThrough(t *testing.T) {
	t.Parallel()

	for b := range 256 {
		for _, in := range []string{
			string([]byte{byte(b)}),
			"x\x1b" + string([]byte{byte(b)}) + "y",
			"x\x1b[" + string([]byte{byte(b)}) + "y",
			"x\x1b]" + string([]byte{byte(b)}) + "y",
			"x\u009b" + string([]byte{byte(b)}) + "y",
			"x" + string(rune(b)) + "y",
		} {
			assertInert(t, "", Lines(in))
		}
	}
}

func assertInert(t *testing.T, name string, lines []string) {
	t.Helper()
	for _, line := range lines {
		if !utf8.ValidString(line) {
			t.Errorf("%s: %q is not UTF-8", name, line)
		}
		for _, r := range line {
			if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
				t.Errorf("%s: %q still carries %U", name, line, r)
			}
		}
	}
}

// Line is for a name or an argument: one line, whatever it held.
func TestLineKeepsAValueToOneLine(t *testing.T) {
	t.Parallel()

	if got := Line("two\nlines\r\x1b[2Jhere"); got != "two lines here" {
		t.Errorf("Line = %q", got)
	}
}
