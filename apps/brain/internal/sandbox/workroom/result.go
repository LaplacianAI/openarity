package workroom

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TimedOutNote is the first line of a command's result, as the runtime
// writes it for the model, when the command ran out of time.
const TimedOutNote = "The command ran out of time and was stopped, with everything it started."

// ExitLine is the last line of a command's result when it exited non-zero.
// The runtime writes it and the improver reads it back, so both use this.
func ExitLine(code int) string { return fmt.Sprintf("exit code %d", code) }

var exitLine = regexp.MustCompile(`(?:^|\n)exit code (-?\d+)\z`)

// ExitOf is the exit code a command's result ends with: its last line, whole,
// is ExitLine's. None for a command that exited zero, as nothing is written.
func ExitOf(text string) (int, bool) {
	m := exitLine.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	code, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// TimedOut is whether a command's result begins with TimedOutNote, as its
// whole first line.
func TimedOut(text string) bool {
	return text == TimedOutNote || strings.HasPrefix(text, TimedOutNote+"\n")
}
