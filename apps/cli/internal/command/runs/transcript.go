package runs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

const (
	fallbackWidth = 100
	maxArg        = 60
	maxValue      = 60
	previewLines  = 5
	graphTool     = "graph"
	delegateTool  = "delegate"

	bullet     = "⏺"
	elbow      = "  ⎿  "
	underElbow = "     "
	clearLine  = "\r\x1b[2K"
)

// paint is the styling a transcript applies, as functions so a test can see
// exactly where each style starts and ends.
type paint struct {
	mark, strong, code, muted, ok, warn, err func(string) string
}

func paintOf(s *ui.Styles) paint {
	return paint{
		mark: render(s.Mark), strong: render(s.Strong), code: render(s.Code),
		muted: render(s.Muted), ok: render(s.OK), warn: render(s.Warn), err: render(s.Err),
	}
}

func render(style lipgloss.Style) func(string) string {
	return func(s string) string { return style.Render(s) }
}

// transcript prints a run's events the way a person reads a conversation: a
// header, each tool call with its result beneath it, the answer, and one line
// saying how it ended. Every value in it came from the run, so each passes
// through cli.Lines or cli.Line before it is styled — styling never sees a
// byte the terminal would act on.
type transcript struct {
	w       io.Writer
	paint   paint
	width   int
	tty     bool
	verbose bool
	team    string

	printed bool
	live    bool
	err     error

	awaiting bool
	lastCall string
	calls    map[string]string

	step      int
	steps     int
	stepShown bool
	tokens    int64
}

// team is the team as it was typed, for the command a delegation's result
// names to read its child.
func newTranscript(w io.Writer, styles *ui.Styles, verbose bool, team string) *transcript {
	return &transcript{
		w: w, paint: paintOf(styles), width: ui.Width(w, fallbackWidth),
		tty: ui.IsTerminal(w), verbose: verbose, team: team,
		calls: map[string]string{},
	}
}

func (t *transcript) write(s string) {
	if t.err != nil {
		return
	}
	_, t.err = io.WriteString(t.w, s)
}

func (t *transcript) line(s string) { t.write(s + "\n") }

// settle takes the "running…" line back off the screen. Only a terminal ever
// has one: anywhere else \r and an erase are bytes in a log.
func (t *transcript) settle() {
	if t.live {
		t.write(clearLine)
		t.live = false
	}
}

// block starts something new: a blank line after whatever came before.
func (t *transcript) block() {
	t.settle()
	t.showStep()
	if t.printed {
		t.line("")
	}
	t.printed = true
}

func (t *transcript) event(f frame, p payload) error {
	switch f.event {
	case "started":
		t.started(p)
	case "resumed":
		t.block()
		t.line(t.paint.muted("↻ resumed after " + strconv.Itoa(p.Messages) + " messages"))
	case "step":
		t.showStep()
		t.step, t.stepShown = p.Step, false
		t.steps = max(t.steps, p.Step)
	case "usage":
		t.tokens += p.InputTokens + p.OutputTokens
		if t.verbose {
			t.stepShown = true
			t.block()
			t.line(t.paint.muted(t.clip(t.stepName() + " · " + usageText(p))))
		}
	case "tool_call":
		t.call(p)
	case "tool_result":
		t.result(p)
	case "delegated":
		// The child of a delegate call still running; its report names it too.
	case "tool_unavailable":
		t.block()
		t.line(t.paint.warn(t.clip("⚠ " + cli.Line(p.Name) + " unavailable: " + cli.Line(p.Reason))))
	case finishedEvent, endEvent:
	default:
		t.block()
		t.line(t.paint.muted(t.clip(strings.TrimSpace(cli.Line(f.event) + " " + cli.Line(f.data)))))
	}
	return t.err
}

func (t *transcript) started(p payload) {
	t.block()
	sep := t.paint.muted(" · ")
	t.line(t.paint.strong(clipRunes(cli.Line(p.Agent), maxValue)) + sep +
		clipRunes(cli.Line(p.Model), maxValue) + sep + clipRunes(cli.Line(p.Pattern), maxValue))
	if len(p.Tools) > 0 {
		t.line(t.paint.muted(t.clip("  tools " + cli.Line(strings.Join(p.Tools, ", ")))))
	}
	if len(p.Skills) > 0 {
		t.line(t.paint.muted(t.clip("  skills " + cli.Line(strings.Join(p.Skills, ", ")))))
	}
}

func (t *transcript) stepName() string {
	if t.step == 0 {
		return "usage"
	}
	return "step " + strconv.Itoa(t.step)
}

// showStep prints a step that has not been shown yet, under --verbose: one
// whose usage never came, before whatever the step did next.
func (t *transcript) showStep() {
	if !t.verbose || t.step == 0 || t.stepShown {
		return
	}
	t.stepShown = true
	t.block()
	t.line(t.paint.muted(t.stepName()))
}

func usageText(p payload) string {
	text := grouped(p.InputTokens) + " in / " + grouped(p.OutputTokens) + " out"
	if p.CachedInputTokens > 0 {
		text += " · " + grouped(p.CachedInputTokens) + " cached"
	}
	return text
}

func (t *transcript) call(p payload) {
	t.block()
	text := t.callLine(p)
	t.line(text)
	t.calls[p.ID] = text
	t.awaiting, t.lastCall = true, p.ID
	if t.tty {
		t.write(t.paint.muted(elbow + "running…"))
		t.live = true
	}
}

func (t *transcript) callLine(p payload) string {
	args := p.Arguments
	if p.Name == delegateTool {
		args = briefOf(args)
	}
	text := t.clip(bullet + " " + cli.Line(p.Name) + "(" + arguments(args) + ")")
	return t.paint.mark(bullet) + strings.TrimPrefix(text, bullet)
}

// result goes straight under its call when nothing came between them. When
// another call did — the model asked for two tools at once — the call is
// repeated so the result is never read as the wrong one's.
func (t *transcript) result(p payload) {
	if t.awaiting && t.lastCall == p.ID {
		t.settle()
	} else {
		t.block()
		head, ok := t.calls[p.ID]
		if !ok {
			head = t.callLine(payload{Name: p.Name})
		}
		t.line(head)
	}
	delete(t.calls, p.ID)
	t.awaiting = false

	tail := " · " + took(time.Duration(p.DurationMS)*time.Millisecond)
	if p.Truncated {
		tail += " · truncated by the brain"
	}

	if p.Error != "" {
		lines := trimBlank(cli.Lines(p.Error))
		if len(lines) == 0 {
			lines = []string{"failed"}
		}
		t.line(elbow + t.paint.err(t.clipAfter(elbow+tail, lines[0])) + t.paint.muted(tail))
		t.preview(lines[1:], t.paint.err)
		return
	}

	summary, rows := describe(p)
	if r, ok := reportIn(p.Output); ok && p.Name == delegateTool {
		summary, rows = t.reported(r)
	}
	t.line(elbow + t.clipAfter(elbow+tail, summary) + t.paint.muted(tail))
	t.preview(rows, func(s string) string { return s })
}

// preview prints the first few lines of a result, or all of them under
// --verbose, and says how many it left out.
func (t *transcript) preview(rows []string, style func(string) string) {
	shown := rows
	if !t.verbose && len(rows) > previewLines {
		shown = rows[:previewLines]
	}
	for _, row := range shown {
		if t.verbose {
			t.line(strings.TrimRight(underElbow+style(row), " "))
			continue
		}
		t.line(strings.TrimRight(underElbow+style(t.clipAfter(underElbow, row)), " "))
	}
	if hidden := len(rows) - len(shown); hidden > 0 {
		t.line(t.paint.muted(underElbow + "… +" + strconv.Itoa(hidden) + " lines (oa runs events --verbose to see all)"))
	}
}

// briefOf is a delegate call's arguments cut to whom it asked and what for:
// the rest of the brief is in the child's own input. Arguments that are not a
// JSON object are left for arguments to show as they are.
func briefOf(raw string) string {
	pairs, ok := inOrder(strings.TrimSpace(raw))
	if !ok {
		return raw
	}
	kept := make([]string, 0, 2)
	for _, kv := range pairs {
		if kv[0] == "agent" || kv[0] == "objective" {
			key, _ := json.Marshal(kv[0])
			kept = append(kept, string(key)+":"+kv[1])
		}
	}
	return "{" + strings.Join(kept, ",") + "}"
}

type report struct{ agent, run, status, body string }

var reportHead = regexp.MustCompile(`^<report agent="([^"]*)" run="([^"]*)" status="([^"]*)" end="([^"]*)">\n`)

// reportIn reads a delegate's output as the envelope the brain wraps a
// child's answer in. The body ends only at the closing tag carrying the
// envelope's own end, which the child never saw.
func reportIn(output string) (report, bool) {
	m := reportHead.FindStringSubmatch(output)
	if m == nil {
		return report{}, false
	}
	closing := "\n</report end=\"" + m[4] + "\">"
	if !strings.HasSuffix(output, closing) || len(output) < len(m[0])+len(closing) {
		return report{}, false
	}
	return report{
		agent:  html.UnescapeString(m[1]),
		run:    m[2],
		status: m[3],
		body:   output[len(m[0]) : len(output)-len(closing)],
	}, true
}

// reported is a delegate's report as how its child ended, the command that
// reads the child's run, and the child's answer beneath.
func (t *transcript) reported(r report) (string, []string) {
	summary := cli.Line(r.agent) + " " + cli.Line(r.status) +
		" · oa runs events " + cli.Line(t.team) + " " + cli.Line(r.run)
	return summary, trimBlank(cli.Lines(r.body))
}

// describe is a tool's output as a summary line and the rows under it.
func describe(p payload) (string, []string) {
	if p.Name == graphTool {
		if summary, rows, ok := graphRows(p.Output); ok {
			return summary, rows
		}
	}

	text := p.Output
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(trimmed), "", "  ") == nil {
			text = pretty.String()
		}
	}

	lines := trimBlank(cli.Lines(text))
	switch len(lines) {
	case 0:
		return "no output", nil
	case 1:
		return lines[0], nil
	default:
		return strconv.Itoa(len(lines)) + " lines", lines
	}
}

type graphHit struct {
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
	Path  []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"path"`
}

// graphRows reads the graph tool's {results: [...]} as aligned rows: kind,
// name, score, and the path that led to a match when it is more than the node
// itself. Output that is not that shape is not a graph result, and is shown
// like any other.
func graphRows(output string) (string, []string, bool) {
	var out struct {
		Results *[]graphHit `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &out); err != nil || out.Results == nil {
		return "", nil, false
	}

	hits := *out.Results
	if len(hits) == 0 {
		return "nothing in reach matched", nil, true
	}

	cells := make([][]string, 0, len(hits))
	widths := make([]int, 3)
	for _, h := range hits {
		row := []string{
			clipRunes(cli.Line(h.Kind), maxValue),
			clipRunes(cli.Line(h.Name), maxValue),
			strconv.FormatFloat(h.Score, 'f', 3, 64),
		}
		for i, cell := range row {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
		if len(h.Path) > 1 {
			steps := make([]string, 0, len(h.Path)-1)
			for _, s := range h.Path[1:] {
				steps = append(steps, clipRunes(cli.Line(s.Kind), maxValue)+" "+clipRunes(cli.Line(s.Name), maxValue))
			}
			row = append(row, "via "+strings.Join(steps, ", "))
		}
		cells = append(cells, row)
	}

	rows := make([]string, 0, len(cells))
	for _, row := range cells {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			if i < len(widths) && i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)))
			}
		}
		rows = append(rows, b.String())
	}

	noun := " results"
	if len(hits) == 1 {
		noun = " result"
	}
	return strconv.Itoa(len(hits)) + noun, rows, true
}

// finish prints the answer, when there is one, and how the run ended.
// unread is what to say when the run could not be read for its answer.
func (t *transcript) finish(status, failure string, run *client.Run, unread string) error {
	t.block()

	switch client.RunStatus(status) {
	case client.Succeeded:
		if run != nil && run.Output != nil {
			if lines := answer(t.paint, *run.Output); len(lines) > 0 {
				t.line(bullet + " " + lines[0])
				for _, l := range lines[1:] {
					t.line(strings.TrimRight("  "+l, " "))
				}
				t.line("")
			}
		}
		if unread != "" {
			t.line(t.paint.muted(t.clip(unread)))
			t.line("")
		}
		t.line(t.paint.ok(t.clip("✓ succeeded" + t.stats(run))))
	case client.Failed:
		text := "✗ failed"
		if failure != "" {
			text += ": " + cli.Line(failure)
		}
		t.line(t.paint.err(t.clip(text)))
	case client.Cancelled:
		t.line(t.paint.warn("■ cancelled"))
	case client.Queued, client.Running:
		t.line(t.paint.warn("? the stream ended with the run still " + status))
	default:
		t.line(t.paint.warn(t.clip("? the run ended as " + cli.Line(status))))
	}
	return t.err
}

func (t *transcript) stats(run *client.Run) string {
	var parts []string
	switch {
	case t.steps == 1:
		parts = append(parts, "1 step")
	case t.steps > 1:
		parts = append(parts, strconv.Itoa(t.steps)+" steps")
	}

	tokens := t.tokens
	if run != nil && run.InputTokens+run.OutputTokens > 0 {
		tokens = run.InputTokens + run.OutputTokens
	}
	if tokens > 0 {
		parts = append(parts, grouped(tokens)+" tokens")
	}

	if run != nil && run.StartedAt != nil && run.FinishedAt != nil && !run.FinishedAt.Before(*run.StartedAt) {
		parts = append(parts, took(run.FinishedAt.Sub(*run.StartedAt)))
	}

	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// close leaves the screen clean when following stops before the end.
func (t *transcript) close() {
	t.settle()
}

func (t *transcript) clip(s string) string {
	return clipWidth(s, t.width)
}

// clipAfter cuts s to the columns left once prefix is on the line.
func (t *transcript) clipAfter(prefix, s string) string {
	return clipWidth(s, max(t.width-lipgloss.Width(prefix), 1))
}

// arguments is a tool call's JSON arguments as name: value pairs, in the
// order the model wrote them. Arguments that are not a JSON object — cut off
// by the brain, say — are shown as they are.
func arguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return ""
	}

	pairs, ok := inOrder(trimmed)
	if !ok {
		return clipRunes(cli.Line(trimmed), maxArg)
	}
	parts := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		parts = append(parts, clipRunes(cli.Line(kv[0]), maxArg)+": "+argument(json.RawMessage(kv[1])))
	}
	return strings.Join(parts, ", ")
}

func inOrder(s string) ([][2]string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}

	var pairs [][2]string
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		pairs = append(pairs, [2]string{key, string(value)})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return pairs, true
}

func argument(value json.RawMessage) string {
	var s string
	if json.Unmarshal(value, &s) == nil {
		return `"` + clipRunes(cli.Line(s), maxArg) + `"`
	}
	var compact bytes.Buffer
	if json.Compact(&compact, value) != nil {
		return clipRunes(cli.Line(string(value)), maxArg)
	}
	return clipRunes(cli.Line(compact.String()), maxArg)
}

// answer is the run's output as terminal lines: sanitised first, then light
// markdown — **bold**, `code`, # headings and fenced blocks. The order is the
// guard: markdown only ever wraps text the sanitiser has already passed, so a
// ** around an escape sequence styles what is left of it, never the escape.
func answer(p paint, text string) []string {
	lines := trimBlank(cli.Lines(text))
	out := make([]string, 0, len(lines))
	fenced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			fenced = !fenced
			out = append(out, p.muted(line))
		case fenced:
			out = append(out, p.code(line))
		default:
			if title, ok := heading(line); ok {
				out = append(out, p.strong(title))
				continue
			}
			out = append(out, inline(p, line))
		}
	}
	return out
}

// heading is a markdown heading's text, and whether the line is one.
func heading(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", false
	}
	level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
	if level == 0 || level > 6 {
		return "", false
	}
	rest := trimmed[level:]
	if rest != "" && rest[0] != ' ' {
		return "", false
	}
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#")), true
}

func inline(p paint, line string) string {
	var b strings.Builder
	for line != "" {
		switch {
		case strings.HasPrefix(line, "**"):
			if end := strings.Index(line[2:], "**"); end > 0 && hugs(line[2:2+end]) {
				b.WriteString(p.strong(line[2 : 2+end]))
				line = line[4+end:]
				continue
			}
			b.WriteString("**")
			line = line[2:]
		case line[0] == '`':
			if end := strings.IndexByte(line[1:], '`'); end > 0 {
				b.WriteString(p.code(line[1 : 1+end]))
				line = line[2+end:]
				continue
			}
			b.WriteByte('`')
			line = line[1:]
		default:
			next := strings.IndexAny(line[1:], "*`")
			if next < 0 {
				b.WriteString(line)
				return b.String()
			}
			b.WriteString(line[:1+next])
			line = line[1+next:]
		}
	}
	return b.String()
}

// hugs is whether emphasis wraps text tightly, as markdown requires: "** a **"
// is two pairs of asterisks, not bold.
func hugs(s string) bool {
	return strings.TrimSpace(s) == s
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// clipWidth cuts s to width columns, the last of them an ellipsis. s is plain
// text — it is measured before it is styled.
func clipWidth(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func took(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	default:
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
}

// grouped is n with thousands separated: 1,677.
func grouped(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return sign + b.String()
}
