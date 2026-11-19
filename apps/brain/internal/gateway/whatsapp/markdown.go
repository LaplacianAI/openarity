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
	// tableRule is the line under a table's header: |---|:--:| and the like.
	tableRule = regexp.MustCompile(`^\s*\|?(\s*:?-+:?\s*\|)+(\s*:?-+:?\s*)?$`)
)

// boldMark stands in for a bold * while italics are converted, so the two
// never read each other's marks.
const boldMark = "\x00"

// Format turns a model's Markdown into WhatsApp's marks: *bold*, _italic_,
// ~struck~, a heading as bold, a link as its words and its address, a table
// as a line a row. Code, in a block or inline, is left exactly as written;
// WhatsApp shows ``` and ` as code itself.
func Format(md string) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			out = append(out, line)
			continue
		}
		if inBlock {
			out = append(out, line)
			continue
		}
		if i+1 < len(lines) && isRow(line) && tableRule.MatchString(lines[i+1]) {
			head, rows := cells(line), 0
			for i += 2; i < len(lines) && isRow(lines[i]); i++ {
				out = append(out, formatLine(rowLine(head, cells(lines[i]))))
				rows++
			}
			if rows == 0 {
				out = append(out, formatLine(strings.Join(head, " · ")))
			}
			i--
			continue
		}
		out = append(out, formatLine(line))
	}
	return strings.Join(out, "\n")
}

// isRow is whether a line can be a table's row: WhatsApp shows none, so a
// table's pipes would arrive as they are.
func isRow(line string) bool {
	return strings.Contains(line, "|")
}

// cells is a row's cells, its outer pipes dropped; an escaped \| is a pipe
// inside a cell.
func cells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var out []string
	var cell strings.Builder
	for k := 0; k < len(line); k++ {
		switch {
		case line[k] == '\\' && k+1 < len(line) && line[k+1] == '|':
			cell.WriteByte('|')
			k++
		case line[k] == '|':
			out = append(out, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(line[k])
		}
	}
	return append(out, strings.TrimSpace(cell.String()))
}

// rowLine is a row as one bullet, still Markdown: its first cell in bold,
// then each other cell under its header's name, "• **Work** — Size: 100 KB".
func rowLine(head, row []string) string {
	var rest []string
	for k := 1; k < len(row); k++ {
		if row[k] == "" {
			continue
		}
		if k < len(head) && head[k] != "" {
			rest = append(rest, head[k]+": "+row[k])
		} else {
			rest = append(rest, row[k])
		}
	}
	first := row[0]
	if first != "" && !strings.ContainsAny(first, "*_`") {
		first = "**" + first + "**"
	}
	switch {
	case first == "":
		return "• " + strings.Join(rest, ", ")
	case len(rest) == 0:
		return "• " + first
	}
	return "• " + first + " — " + strings.Join(rest, ", ")
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
