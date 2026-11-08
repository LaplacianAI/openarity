package telegram

import (
	"strings"
	"unicode/utf8"
)

// limits is what one rich message holds: Telegram's 32768 characters and 500
// blocks. Blocks are estimated from above, a line each and one more for the
// block around them, so a part never comes back refused.
type limits struct {
	runes  int
	blocks int
}

var richLimits = limits{runes: 32768, blocks: 500}

// Sanitize makes an answer safe to send as rich Markdown. An agent's answer can
// carry text a prompt injection chose, so:
//
//   - an image becomes a link. Telegram's servers fetch an image's URL to show
//     it, which would send whatever is in the URL somewhere without a click; a
//     link waits for the reader, behind Telegram's "Open this link?".
//   - raw HTML is escaped. Rich Markdown accepts it, and a tag Telegram does not
//     know refuses the whole message, which no retry would fix.
//
// Code is left exactly as written, where both are only text.
func Sanitize(text string) string {
	lines := strings.Split(text, "\n")
	var fence string
	for i, line := range lines {
		if f, ok := fenceOf(line); ok {
			switch {
			case fence == "":
				fence = f
			case closes(line, fence):
				fence = ""
			}
			continue
		}
		if fence == "" {
			lines[i] = sanitizeLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

// sanitizeLine rewrites one line outside a fence, skipping inline code spans:
// a run of backticks opens a span that the same length of run closes.
func sanitizeLine(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '`' {
			n := run(line, i, '`')
			if end := strings.Index(line[i+n:], strings.Repeat("`", n)); end >= 0 {
				stop := i + n + end + n
				b.WriteString(line[i:stop])
				i = stop
				continue
			}
			b.WriteString(line[i : i+n])
			i += n
			continue
		}
		switch {
		case strings.HasPrefix(line[i:], "![]"):
			b.WriteString("[image]")
			i += 3
		case strings.HasPrefix(line[i:], "!["):
			b.WriteString("[")
			i += 2
		case line[i] == '<':
			b.WriteString("&lt;")
			i++
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String()
}

// Split cuts a Markdown answer into rich messages, between blocks: never
// inside a paragraph, a list, a table or a code block unless one block is
// itself too big, and then a code block is closed and opened again around
// the cut. The same text always splits the same way, which is what lets a
// retry resume at the part it stopped at.
func Split(text string) []string { return split(text, richLimits) }

func split(text string, l limits) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var parts []string
	var cur []string
	runes, blocks := 0, 0
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, strings.Join(cur, "\n\n"))
			cur, runes, blocks = nil, 0, 0
		}
	}
	for _, b := range blocksOf(text) {
		for _, piece := range fit(b, l) {
			r, n := utf8.RuneCountInString(piece), cost(piece)
			if len(cur) > 0 && (runes+2+r > l.runes || blocks+n > l.blocks) {
				flush()
			}
			cur = append(cur, piece)
			runes += r + 2
			blocks += n
		}
	}
	flush()
	return parts
}

// blocksOf is the text's blocks: a fenced code block whole, blank lines and
// all, and otherwise each run of lines between blank lines.
func blocksOf(text string) []string {
	var out, cur []string
	var fence string
	end := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		f, isFence := fenceOf(line)
		switch {
		case fence != "":
			cur = append(cur, line)
			if closes(line, fence) {
				fence = ""
				end()
			}
		case isFence:
			end()
			fence = f
			cur = append(cur, line)
		case strings.TrimSpace(line) == "":
			end()
		default:
			cur = append(cur, line)
		}
	}
	end()
	return out
}

// fit cuts one block that is too big for a message by lines, closing and
// reopening a fence around each cut so every piece is still code.
func fit(block string, l limits) []string {
	if utf8.RuneCountInString(block) <= l.runes && cost(block) <= l.blocks {
		return []string{block}
	}
	lines := strings.Split(block, "\n")
	open, close := "", ""
	if f, ok := fenceOf(lines[0]); ok {
		open, close = lines[0], f
		lines = lines[1:]
		if n := len(lines); n > 0 {
			if closes(lines[n-1], f) {
				lines = lines[:n-1]
			}
		}
	}
	wrap := func(body []string) string {
		if open == "" {
			return strings.Join(body, "\n")
		}
		return open + "\n" + strings.Join(body, "\n") + "\n" + close
	}
	// Room for the fence lines, and the blocks they count as.
	room := limits{runes: l.runes - utf8.RuneCountInString(open+close) - 2, blocks: l.blocks - 1}
	if open != "" {
		room.blocks = l.blocks
	}

	var out, cur []string
	runes := 0
	for _, line := range lines {
		for _, seg := range cutLine(line, room.runes) {
			r := utf8.RuneCountInString(seg) + 1
			if len(cur) > 0 && (runes+r > room.runes || (open == "" && len(cur)+1 > room.blocks)) {
				out = append(out, wrap(cur))
				cur, runes = nil, 0
			}
			cur = append(cur, seg)
			runes += r
		}
	}
	if len(cur) > 0 {
		out = append(out, wrap(cur))
	}
	return out
}

// cutLine cuts a line longer than max characters at character boundaries.
func cutLine(line string, maxRunes int) []string {
	rs := []rune(line)
	if len(rs) <= maxRunes || maxRunes <= 0 {
		return []string{line}
	}
	var out []string
	for len(rs) > maxRunes {
		out = append(out, string(rs[:maxRunes]))
		rs = rs[maxRunes:]
	}
	return append(out, string(rs))
}

// cost is how many blocks a piece may count as: a code block is one, and
// otherwise every line may be one, plus the list or table around them.
func cost(piece string) int {
	if _, ok := fenceOf(strings.SplitN(piece, "\n", 2)[0]); ok {
		return 1
	}
	return strings.Count(piece, "\n") + 2
}

// fenceOf is the fence a line opens or closes: three or more backticks or
// tildes after at most three spaces of indent.
func fenceOf(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return "", false
	}
	n := run(t, 0, t[0])
	if n < 3 {
		return "", false
	}
	if t[0] == '`' && strings.Contains(t[n:], "`") {
		return "", false
	}
	return t[:n], true
}

// closes is whether a line closes an open fence: the same character, at least
// as long, and nothing after it but space. "```go" inside a block is code.
func closes(line, open string) bool {
	f, ok := fenceOf(line)
	if !ok || f[0] != open[0] || len(f) < len(open) {
		return false
	}
	return strings.TrimSpace(strings.TrimLeft(line, " ")[len(f):]) == ""
}

func run(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}
