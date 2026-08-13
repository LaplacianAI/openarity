package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
)

// A pipe is not a terminal. This is the check that stops the wizard rendering
// cursor moves into a CI log, and it is the whole reason IsTerminal takes the
// writer rather than assuming os.Stdout.
func TestABufferIsNotATerminal(t *testing.T) {
	t.Parallel()

	if IsTerminal(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer was reported as a terminal")
	}
}

func TestAPipeIsNotATerminal(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	if IsTerminal(w) {
		t.Error("an os.Pipe write end was reported as a terminal")
	}
}

// The one that matters for output correctness: styles built for a non-terminal
// writer must emit no escape sequences at all. Colour in a redirected file is
// what makes `oa teams list > teams.txt` unreadable and ungreppable.
func TestNoEscapeSequencesReachANonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	styles := New(&buf, theme.Auto)

	rendered := styles.Title.Render("platform") +
		styles.Label.Render("subject") +
		styles.Value.Render("dev") +
		styles.Muted.Render("none") +
		styles.OK.Render("ok") +
		styles.Warn.Render("careful") +
		styles.Err.Render("failed")

	if strings.Contains(rendered, "\x1b") {
		t.Errorf("an escape sequence reached a non-terminal writer: %q", rendered)
	}
}

// Styling must never change the text itself. A width or a padding that
// silently truncates a team name would make the output wrong rather than
// merely plain.
func TestStylingKeepsTheText(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	styles := New(&buf, theme.Auto)

	const name = "a-team-with-a-deliberately-long-name"

	for label, style := range map[string]interface{ Render(...string) string }{
		"Title": styles.Title,
		"Label": styles.Label,
		"Value": styles.Value,
		"Muted": styles.Muted,
		"OK":    styles.OK,
		"Warn":  styles.Warn,
		"Err":   styles.Err,
	} {
		if got := strings.TrimSpace(style.Render(name)); got != name {
			t.Errorf("%s.Render(%q) = %q — the style changed the text", label, name, got)
		}
	}
}

// Every style comes from one renderer bound to the writer it prints to.
// Building a second set for a second writer must not inherit the first's
// colour profile — that is how a piped command starts emitting colour because
// an earlier one was a terminal.
func TestStylesAreBoundToTheirOwnWriter(t *testing.T) {
	t.Parallel()

	var first, second bytes.Buffer

	if New(&first, theme.Auto) == New(&second, theme.Auto) {
		t.Error("New returned the same styles for two writers")
	}
}
