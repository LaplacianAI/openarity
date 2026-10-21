package cli

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
	// The C1 forms of the sequence introducers, which a UTF-8 terminal also
	// honours: CSI, OSC, DCS, SOS, PM, APC, and ST, which ends a string.
	c1CSI = 0x9b
	c1OSC = 0x9d
	c1DCS = 0x90
	c1SOS = 0x98
	c1PM  = 0x9e
	c1APC = 0x9f
	c1ST  = 0x9c

	tabStop = "    "
)

// Lines is untrusted text — a model's answer, a tool's output — as lines a
// terminal shows and does not act on. Safe quotes a value whole, which keeps
// a name to one cell but turns a paragraph into one long escaped string;
// this keeps the paragraph and takes out what a terminal would obey.
//
// An escape sequence is removed whole, its parameters with it, so OSC 8 keeps
// the link's text and loses the link, and OSC 52 never reaches the clipboard.
// Any other C0 or C1 control is dropped. A carriage return that does not end
// a line becomes a line break — alone it would move back and let the rest
// overwrite what came before. Bidirectional controls are dropped, tabs become
// spaces, bytes that are not UTF-8 become U+FFFD, and the line and paragraph
// separators break the line like \n.
func Lines(s string) []string {
	var (
		lines []string
		line  strings.Builder
	)
	breakLine := func() {
		lines = append(lines, line.String())
		line.Reset()
	}

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size

		switch {
		case r == utf8.RuneError && size <= 1:
			line.WriteRune(utf8.RuneError)
		case r == '\n', r == '\u2028', r == '\u2029':
			breakLine()
		case r == '\r':
			if i < len(s) && s[i] == '\n' {
				i++
			}
			breakLine()
		case r == '\t':
			line.WriteString(tabStop)
		case r == esc:
			i = skipEscape(s, i)
		case r == c1CSI:
			i = skipCSI(s, i)
		case r == c1OSC, r == c1DCS, r == c1SOS, r == c1PM, r == c1APC:
			i = skipString(s, i)
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
		default:
			line.WriteRune(r)
		}
	}
	breakLine()
	return lines
}

// Line is Lines joined with spaces, for a value that must stay on one line —
// a name, an argument, a reason.
func Line(s string) string {
	return strings.Join(Lines(s), " ")
}

// skipEscape skips what follows an ESC at s[i:]. A sequence that is cut off
// is skipped to where it stops; a line break is never swallowed.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		return skipCSI(s, i+1)
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(s, i+1)
	case c >= 0x20 && c <= 0x2f:
		// Intermediates, then one final byte: ESC ( B, ESC # 8.
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	case c >= 0x30 && c <= 0x7e:
		// A two-character sequence: ESC c resets the terminal, ESC 7 saves
		// the cursor.
		return i + 1
	default:
		return i
	}
}

// skipCSI skips a control sequence's parameters, intermediates and final
// byte. Anything else ends it unconsumed.
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString skips an OSC, DCS, SOS, PM or APC body up to its terminator — BEL,
// ESC \ or the C1 ST — or up to a line break, which is left for the caller:
// a body that never ends would otherwise eat the rest of the text.
func skipString(s string, i int) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case bel, c1ST:
			return i + size
		case esc:
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i
		case '\n', '\r':
			return i
		}
		i += size
	}
	return i
}
