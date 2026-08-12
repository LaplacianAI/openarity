package ui

import (
	"bytes"
	"testing"
)

var noEnv = envOf(nil)

// Auto-detection is an OSC 11 query to the terminal, and it fails in three
// ordinary situations: inside tmux or screen, which termenv refuses by name
// because one session can be attached to several terminals; in a backgrounded
// process, which cannot touch the terminal at all; and against any terminal
// that does not answer, which costs a five-second stall and then assumes
// dark. Someone on a light terminal inside tmux would get the dark palette,
// and #5FD3BC on white is about 1.9:1.
//
// So the override exists, and these pin that it is honoured.
func TestTheThemeOverrideIsRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		env  map[string]string
		want theme
	}{
		"light":             {map[string]string{"OPENARITY_THEME": "light"}, themeLight},
		"dark":              {map[string]string{"OPENARITY_THEME": "dark"}, themeDark},
		"mixed case":        {map[string]string{"OPENARITY_THEME": "Light"}, themeLight},
		"surrounding space": {map[string]string{"OPENARITY_THEME": " dark "}, themeDark},
		"unset":             {nil, themeAuto},
		"empty":             {map[string]string{"OPENARITY_THEME": ""}, themeAuto},
	} {
		if got := themeFrom(envOf(tc.env)); got != tc.want {
			t.Errorf("%s: themeFrom = %q, want %q", name, got, tc.want)
		}
	}
}

// A typo must not silently pick a palette. Falling back to auto-detection is
// the honest answer — the user gets whatever the terminal reports, which is
// what they would have got without setting anything.
func TestAnUnknownThemeFallsBackToDetection(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"solarized", "true", "1", "LIGHTS", "auto"} {
		if got := themeFrom(envOf(map[string]string{"OPENARITY_THEME": value})); got != themeAuto {
			t.Errorf("OPENARITY_THEME=%q gave %q, want auto-detection", value, got)
		}
	}
}

// The override has to reach the renderer, not just be parsed. Setting it and
// having the styles still query the terminal is the failure this catches.
func TestTheOverrideDecidesTheRenderedColour(t *testing.T) {
	t.Parallel()

	var dark, light bytes.Buffer

	darkStyles := New(&dark, envOf(map[string]string{"OPENARITY_THEME": "dark"}))
	lightStyles := New(&light, envOf(map[string]string{"OPENARITY_THEME": "light"}))

	if darkStyles.hasDarkBackground() == lightStyles.hasDarkBackground() {
		t.Error("the theme override did not change the renderer's background assumption")
	}
	if !darkStyles.hasDarkBackground() {
		t.Error("OPENARITY_THEME=dark did not select the dark palette")
	}
	if lightStyles.hasDarkBackground() {
		t.Error("OPENARITY_THEME=light did not select the light palette")
	}
}

// Nothing set is the normal case, and it must not panic or block on a
// non-terminal writer. A bytes.Buffer is not a TTY, so detection short
// circuits rather than sending an escape sequence into it.
func TestDetectionOnANonTerminalDoesNotEmitAQuery(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	styles := New(&buf, noEnv)

	_ = styles.Title.Render("openarity")

	if buf.Len() != 0 {
		t.Errorf("building styles wrote %q to the writer — an OSC query reached a non-terminal", buf.String())
	}
}
