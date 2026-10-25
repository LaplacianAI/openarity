package runtime

import (
	"crypto/rand"
	"fmt"
	"html"
	"regexp"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const maxReport = 32 << 10

var reportTag = regexp.MustCompile(`(?i)<(/?report)`)

func report(child db.Run, end string) string {
	body := ""
	switch {
	case child.Output != nil:
		body = *child.Output
	case child.Error != nil:
		body = *child.Error
	}

	body = reportTag.ReplaceAllString(body, "&lt;$1")
	if len(body) > maxReport {
		cut := maxReport
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = fmt.Sprintf("%s\n[the report was cut at 32 KiB; the whole answer is run %s]", body[:cut], child.ID)
	}

	return fmt.Sprintf("<report agent=\"%s\" run=\"%s\" status=\"%s\" end=\"%s\">\n%s\n</report end=\"%s\">",
		html.EscapeString(child.AgentName), child.ID, child.Status, end, body, end)
}

func reportEnd() string {
	return rand.Text()
}
