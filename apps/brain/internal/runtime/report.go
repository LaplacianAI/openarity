package runtime

import (
	"crypto/rand"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const maxReport = 32 << 10

var reportTag = regexp.MustCompile(`(?i)<(/?report)`)

// toldPerson is what a delegate told its person while it worked, as far as it
// could be read.
type toldPerson struct {
	said   []string
	unread bool
}

// report is a delegate's report, with what it told the person while it
// worked, so the parent does not say it again. What was said is the child's
// own words, so it goes inside the report, as data, after any cut so a long
// answer never loses it.
func report(child db.Run, end string, t toldPerson) string {
	body := ""
	switch {
	case child.Output != nil:
		body = *child.Output
	case child.Error != nil:
		body = *child.Error
	}
	body = cutReport(reportTag.ReplaceAllString(body, "&lt;$1"), child)
	if s := saidBlock(t.said); s != "" {
		body += "\n" + s
	}
	if t.unread {
		body += "\n[what it told the person while it worked could not be read: it may have told them some of this]"
	}
	// A run stopped at a limit kept what it had; say it is not a finished answer.
	if child.Output != nil && child.Status != "succeeded" && child.Error != nil {
		body = fmt.Sprintf("%s\n[stopped early: %s]", body, reportTag.ReplaceAllString(*child.Error, "&lt;$1"))
	}

	return fmt.Sprintf("<report agent=\"%s\" run=\"%s\" status=\"%s\" end=\"%s\">\n%s\n</report end=\"%s\">",
		html.EscapeString(child.AgentName), child.ID, child.Status, end, body, end)
}

// saidBlock lists what was said, its newest lines whole that fit in a
// report, saying how many earlier ones were left out.
//
// Each line is one line, escaped as the report is, so a line break in what
// was said cannot start a note of the brain's after it, and its size is
// counted as it is written, the head and the room for its count too.
func saidBlock(said []string) string {
	if len(said) == 0 {
		return ""
	}
	lines := make([]string, len(said))
	for i, s := range said {
		lines[i] = "- " + reportTag.ReplaceAllString(strings.Join(strings.Fields(s), " "), "&lt;$1")
	}
	const head = "[it told the person while it worked, so do not tell them again:"
	room := maxReport - len(head) - len(" (10000 earlier, not shown)") - len("]")
	keep, size := len(lines), 0
	for keep > 0 && size+len(lines[keep-1])+1 <= room {
		size += len(lines[keep-1]) + 1
		keep--
	}
	h := head
	if keep > 0 {
		h += fmt.Sprintf(" (%d earlier, not shown)", keep)
	}
	if keep == len(lines) {
		return h + "]"
	}
	return h + "\n" + strings.Join(lines[keep:], "\n") + "]"
}

// cutReport is s held to maxReport, cut on a rune and saying where the rest is.
func cutReport(s string, child db.Run) string {
	if len(s) <= maxReport {
		return s
	}
	cut := maxReport
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[the report was cut at 32 KiB; the whole answer is run %s]", s[:cut], child.ID)
}

func reportEnd() string {
	return rand.Text()
}
