package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeLeavesAPlainValueAlone(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"github", "pdf-processing", "https://mcp.example.com/v1", "npx -y @mcp/server /data",
		"teams/x/mcp/github#token", "é ü 漢字", "", "two words",
	} {
		if got := Safe(s); got != s {
			t.Errorf("Safe(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Anything a terminal would act on is quoted, and a quoted value cannot carry
// a raw escape byte or span two lines.
func TestSafeQuotesWhatATerminalWouldActOn(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"\x1b[2Jgithub",           // clear the screen
		"\x1b]8;;http://x\x07a",   // a hyperlink escape
		"two\nlines",              // a second row
		"tab\there",               // a column out of line
		"\u202egnp.exe",           // a right-to-left override
		"\x9b2J",                  // the C1 form of an escape
		string([]byte{0xff, 'a'}), // invalid UTF-8
	} {
		got := Safe(s)
		if got == s || !strings.HasPrefix(got, `"`) {
			t.Errorf("Safe(%q) = %q, want it quoted", s, got)
		}
		if strings.ContainsAny(got, "\x1b\n\t\x07\u202e") {
			t.Errorf("Safe(%q) = %q still carries a control", s, got)
		}
	}
}

func TestWriteRawRefusesAnEscapeOnlyOnATerminal(t *testing.T) {
	t.Parallel()

	evil := []byte("ok\n\x1b]0;pwned\x07\x1b[2J")
	for name, tc := range map[string]struct {
		body            []byte
		terminal, force bool
		refused         bool
	}{
		"text to a terminal":           {[]byte("print(1)\n\tx\r\n é"), true, false, false},
		"an escape to a terminal":      {evil, true, false, true},
		"an escape to a terminal, raw": {evil, true, true, false},
		"an escape to a pipe":          {evil, false, false, false},
		"a C1 control to a terminal":   {[]byte("a\u009b2Jb"), true, false, true},
		"an RLO to a terminal":         {[]byte("a\u202eb"), true, false, true},
		"bytes that are not UTF-8":     {[]byte{0x89, 'P', 'N', 'G'}, true, false, true},
		"a NUL to a terminal":          {[]byte("a\x00b"), true, false, true},
	} {
		var out strings.Builder
		err := writeRaw(&out, tc.terminal, tc.body, tc.force)
		if tc.refused {
			if !errors.Is(err, ErrUnsafeForTerminal) || out.Len() != 0 {
				t.Errorf("%s: err = %v, wrote %q; want refused and nothing written", name, err, out.String())
			}
			continue
		}
		if err != nil || out.String() != string(tc.body) {
			t.Errorf("%s: err = %v, wrote %q", name, err, out.String())
		}
	}
}
