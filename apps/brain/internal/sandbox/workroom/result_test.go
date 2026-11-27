package workroom

import "testing"

// The exit line is the whole last line, nothing after it.
func TestExitOf(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		text string
		code int
		ok   bool
	}{
		{"out\nexit code 2", 2, true},
		{"exit code 1", 1, true},
		{"part\nexit code -1", -1, true},
		{"x\r\nexit code 2", 2, true},
		{"x\nexit code 2\n", 0, false},
		{"x\nexit code 2 ", 0, false},
		{"it failed with exit code 3", 0, false},
		{"exit code 1\nthen more", 0, false},
		{"x\nexit code 99999999999999999999", 0, false},
		{"", 0, false},
	} {
		if code, ok := ExitOf(c.text); code != c.code || ok != c.ok {
			t.Errorf("ExitOf(%q) = %d, %v; want %d, %v", c.text, code, ok, c.code, c.ok)
		}
	}
}

// The timeout note is the whole first line.
func TestTimedOut(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		TimedOutNote:                        true,
		TimedOutNote + "\npart":             true,
		TimedOutNote + " and more\npart":    false,
		"a log\n" + TimedOutNote + "\npart": false,
		"The command ran out of time":       false,
		"":                                  false,
	} {
		if got := TimedOut(text); got != want {
			t.Errorf("TimedOut(%q) = %v, want %v", text, got, want)
		}
	}
}
