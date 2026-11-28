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

// report is a delegate's report, with what it told the person while it
// worked (told), so the parent does not say it again. What was said is the
// child's own words, so it goes inside the report, as data, after any cut so
// a long answer never loses it; it is held to one cut of its own.
func report(child db.Run, end string, told []string) string {
	body := ""
	switch {
	case child.Output != nil:
		body = *child.Output
	case child.Error != nil:
		body = *child.Error
	}
	body = cutReport(reportTag.ReplaceAllString(body, "&lt;$1"), child)
	if len(told) > 0 {
		said := reportTag.ReplaceAllString("[it told the person while it worked, so do not tell them again:\n- "+
			strings.Join(told, "\n- ")+"]", "&lt;$1")
		body += "\n" + cutReport(said, child)
	}
	// A run stopped at a limit kept what it had; say it is not a finished answer.
	if child.Output != nil && child.Status != "succeeded" && child.Error != nil {
		body = fmt.Sprintf("%s\n[stopped early: %s]", body, reportTag.ReplaceAllString(*child.Error, "&lt;$1"))
	}

	return fmt.Sprintf("<report agent=\"%s\" run=\"%s\" status=\"%s\" end=\"%s\">\n%s\n</report end=\"%s\">",
		html.EscapeString(child.AgentName), child.ID, child.Status, end, body, end)
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
