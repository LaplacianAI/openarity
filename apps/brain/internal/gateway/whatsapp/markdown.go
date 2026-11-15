package whatsapp

import (
	"regexp"
	"strings"
)

var (
	bold      = regexp.MustCompile(`\*\*([^*\n]+?)\*\*|__([^_\n]+?)__`)
	italic    = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*\n]*?)\*`)
	strike    = regexp.MustCompile(`~~([^~\n]+?)~~`)
	heading   = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*$`)
	bullet    = regexp.MustCompile(`^(\s*)[*+]\s+`)
	link      = regexp.MustCompile(`!?\[([^\]\n]*)\]\(([^)\s]+)\)`)
	inlineRun = regexp.MustCompile("`[^`\n]*`")
)

// boldMark stands in for a bold * while italics are converted, so the two
// never read each other's marks.
const boldMark = "\x00"

// Format turns a model's Markdown into WhatsApp's marks: *bold*, _italic_,
// ~struck~, a heading as bold, a link as its words and its address. Code,
// in a block or inline, is left exactly as written; WhatsApp shows ``` and `
// as code itself.
func Format(md string) string {
	lines := strings.Split(md, "\n")
	inBlock := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			continue
		}
		if !inBlock {
			lines[i] = formatLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

// formatLine converts one line outside a code block, leaving inline code be.
func formatLine(line string) string {
	var b strings.Builder
	last := 0
	for _, loc := range inlineRun.FindAllStringIndex(line, -1) {
		b.WriteString(formatText(line[last:loc[0]], last == 0))
		b.WriteString(line[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(formatText(line[last:], last == 0))
	return b.String()
}

func formatText(s string, lineStart bool) string {
	if lineStart {
		if m := heading.FindStringSubmatch(s); m != nil {
			return "*" + m[1] + "*"
		}
		s = bullet.ReplaceAllString(s, "$1• ")
	}
	s = link.ReplaceAllStringFunc(s, func(m string) string {
		parts := link.FindStringSubmatch(m)
		if parts[1] == "" || parts[1] == parts[2] {
			return parts[2]
		}
		return parts[1] + " (" + parts[2] + ")"
	})
	s = bold.ReplaceAllString(s, boldMark+"$1$2"+boldMark)
	s = italic.ReplaceAllString(s, "${1}_${2}_")
	s = strike.ReplaceAllString(s, "~$1~")
	return strings.ReplaceAll(s, boldMark, "*")
}
