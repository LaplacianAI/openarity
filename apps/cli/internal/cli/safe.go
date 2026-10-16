package cli

import (
	"errors"
	"io"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

// Safe is a value as a table cell may show it. A plain value is returned as
// it is; one holding anything a terminal would act on rather than print — an
// escape sequence, a control character, a line break, a bidirectional
// override, invalid UTF-8 — is quoted with strconv.QuoteToGraphic, which
// escapes all of those and keeps one value to one row.
//
// Names, URLs, commands and references are written by any member with the
// right permission, and printed to whoever lists them, an admin included.
// Quoting everything would make every ordinary name look odd; quoting
// nothing lets one member clear another's screen. Only inside a Table
// callback: -o json carries what the brain sent.
func Safe(s string) string {
	if !utf8.ValidString(s) {
		return strconv.QuoteToGraphic(s)
	}
	for _, r := range s {
		if !strconv.IsGraphic(r) {
			return strconv.QuoteToGraphic(s)
		}
	}
	return s
}

// ErrUnsafeForTerminal is a file that would act on the terminal it is printed
// to rather than be read on it.
var ErrUnsafeForTerminal = errors.New("the file holds control characters a terminal would act on — " +
	"redirect it to a file, or pass --raw to print it anyway")

// WriteRaw writes a file's bytes as they are stored. To a pipe or a file that
// is always right. To a terminal it is right for text and wrong for anything
// carrying an escape sequence: a skill's files are written by any member with
// skill:write, or by whoever published an imported skill, and printing one
// could rewrite the screen of whoever ran the command. force is the person
// saying they know.
func WriteRaw(opts *Options, body []byte, force bool) error {
	return writeRaw(opts.Stdout, ui.IsTerminal(opts.Stdout), body, force)
}

func writeRaw(w io.Writer, terminal bool, body []byte, force bool) error {
	if terminal && !force && actsOnTerminal(body) {
		return ErrUnsafeForTerminal
	}
	_, err := w.Write(body)
	return err
}

// actsOnTerminal reports bytes a terminal interprets rather than shows: any
// control character but tab and line feed, the C1 controls a UTF-8 terminal
// also honours, a carriage return that does not end a line — alone it moves
// back to the start and lets the rest overwrite what was printed — line and
// paragraph separators, bidirectional controls, and bytes that are not UTF-8.
func actsOnTerminal(body []byte) bool {
	if !utf8.Valid(body) {
		return true
	}
	text := string(body)
	for i, r := range text {
		switch {
		case r == '\t' || r == '\n':
		case r == '\r':
			if i+1 >= len(text) || text[i+1] != '\n' {
				return true
			}
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), unicode.In(r, unicode.Zl, unicode.Zp):
			return true
		}
	}
	return false
}
