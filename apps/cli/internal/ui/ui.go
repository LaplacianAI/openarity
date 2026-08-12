package ui

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type (
	Env   func(string) string
	theme string
)

var (
	accentColor = lipgloss.AdaptiveColor{Light: "#0B6A5C", Dark: "#5FD3BC"}
	mutedColor  = lipgloss.AdaptiveColor{Light: "#5F6763", Dark: "#8A9391"}
	okColor     = lipgloss.AdaptiveColor{Light: "#41692A", Dark: "#93C97E"}
	warnColor   = lipgloss.AdaptiveColor{Light: "#85540F", Dark: "#E3B778"}
	errColor    = lipgloss.AdaptiveColor{Light: "#A83229", Dark: "#E88A82"}
)

const (
	themeAuto  theme = "auto"
	themeLight theme = "light"
	themeDark  theme = "dark"
)

func themeFrom(env Env) theme {
	switch strings.ToLower(strings.TrimSpace(env("OPENARITY_THEME"))) {
	case "light":
		return themeLight
	case "dark":
		return themeDark
	default:
		return themeAuto
	}
}

type Styles struct {
	Mark  lipgloss.Style
	Title lipgloss.Style
	Label lipgloss.Style
	Value lipgloss.Style
	Muted lipgloss.Style
	OK    lipgloss.Style
	Warn  lipgloss.Style
	Err   lipgloss.Style

	renderer *lipgloss.Renderer
	env      Env
}

func New(w io.Writer, env Env) *Styles {
	r := lipgloss.NewRenderer(w)

	switch themeFrom(env) {
	case themeLight:
		r.SetHasDarkBackground(false)
	case themeDark:
		r.SetHasDarkBackground(true)
	case themeAuto:
		// Left to the terminal. On a non-terminal writer termenv short
		// circuits rather than writing a query into it.
	}

	return &Styles{
		Mark:  r.NewStyle().Foreground(accentColor),
		Title: r.NewStyle().Bold(true).Foreground(accentColor),
		Label: r.NewStyle().Foreground(mutedColor),
		Value: r.NewStyle(),
		Muted: r.NewStyle().Foreground(mutedColor),
		OK:    r.NewStyle().Foreground(okColor),
		Warn:  r.NewStyle().Foreground(warnColor),
		Err:   r.NewStyle().Foreground(errColor),

		renderer: r,
		env:      env,
	}
}

func (s *Styles) hasDarkBackground() bool { return s.renderer.HasDarkBackground() }

func (s *Styles) Banner() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		s.Title.Render("openarity"),
		s.Muted.Render("an agent platform, graph-native"),
	)
}

func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
