package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
)

// There is no terminal art yet, so the banner is the wordmark alone. If the
// mark is ever drawn here it is the whole of it in braille, never a different
// picture. See BRAND.md.
func TestTheBannerCarriesTheName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	banner := New(&buf, theme.Auto).Banner()

	if !strings.Contains(banner, "openarity") {
		t.Errorf("the banner does not carry the name: %q", banner)
	}
}

// Lowercase, always. BRAND.md makes this a rule rather than a preference, and
// a capitalised wordmark is the sort of thing that reaches a screenshot and
// then a slide deck.
func TestTheWordmarkIsLowercase(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	banner := New(&buf, theme.Auto).Banner()

	for _, wrong := range []string{"OpenArity", "OPENARITY", "Openarity"} {
		if strings.Contains(banner, wrong) {
			t.Errorf("the banner renders %q", wrong)
		}
	}
}

// A first-run screen has to fit a default terminal, and eighty columns is the
// floor.
func TestTheBannerFitsAnEightyColumnTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	for _, line := range strings.Split(New(&buf, theme.Auto).Banner(), "\n") {
		if width := utf8.RuneCountInString(line); width > 80 {
			t.Errorf("a banner line is %d columns: %q", width, line)
		}
	}
}

// Decoration must not reach a log. Same rule as the styles: a non-terminal
// writer gets no escape sequences.
func TestTheBannerIsPlainOnANonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if strings.Contains(New(&buf, theme.Auto).Banner(), "\x1b") {
		t.Error("the banner emitted an escape sequence to a non-terminal writer")
	}
}
