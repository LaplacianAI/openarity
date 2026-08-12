package ui

import (
	"bytes"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
)

// New takes a resolved theme rather than reading OPENARITY_THEME itself.
// Auto-detection is an OSC 11 query to the terminal, and it is unavailable
// more often than it looks: tmux and screen are refused by name because one
// session can be attached to several terminals, a backgrounded process cannot
// query at all, and a terminal that never answers costs a five-second stall
// before defaulting to dark. Someone on a light terminal inside tmux would
// get the dark palette, and #5FD3BC on white is about 1.9:1.
func TestTheThemeDecidesTheRenderedColour(t *testing.T) {
	t.Parallel()

	var dark, light bytes.Buffer

	darkStyles := New(&dark, theme.Dark)
	lightStyles := New(&light, theme.Light)

	if darkStyles.hasDarkBackground() == lightStyles.hasDarkBackground() {
		t.Error("the theme did not change the renderer's background assumption")
	}
	if !darkStyles.hasDarkBackground() {
		t.Error("theme.Dark did not select the dark palette")
	}
	if lightStyles.hasDarkBackground() {
		t.Error("theme.Light did not select the light palette")
	}
}

// Auto is the normal case, and it must not panic or block on a non-terminal
// writer. A bytes.Buffer is not a TTY, so detection short circuits rather
// than sending an escape sequence into it.
func TestAutoDoesNotQueryANonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	styles := New(&buf, theme.Auto)

	_ = styles.Title.Render("openarity")

	if buf.Len() != 0 {
		t.Errorf("building styles wrote %q to the writer — an OSC query reached a non-terminal", buf.String())
	}
}

// Every theme has to produce usable styles. A value that fell through to a
// zero Styles would panic on the first Render, and the panic would be in
// whatever command happened to print first.
func TestEveryThemeProducesStyles(t *testing.T) {
	t.Parallel()

	for _, one := range theme.All() {
		var buf bytes.Buffer
		styles := New(&buf, one)

		if got := styles.Title.Render("openarity"); got == "" {
			t.Errorf("theme %q rendered nothing", one)
		}
	}
}
