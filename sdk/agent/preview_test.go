package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var pagePreview = Preview{Over: 32 << 10, Head: 24 << 10, Tail: 8 << 10}

func (r *compactRig) sendWith(t *testing.T, msgs []Message, tools ...Tool) []Message {
	t.Helper()
	if _, err := r.client.Complete(t.Context(), Request{Messages: msgs, Tools: tools}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	reqs := r.model.requests()
	return reqs[len(reqs)-1].Messages
}

func named(name string, p Preview) Tool { return Tool{Name: name, Preview: p} }

// ends is a preview without the line before it that says it is a part.
func ends(preview string) string {
	_, rest, _ := strings.Cut(preview, "\n")
	return rest
}

// two results of the same size, from two tools.
func twoResults(size int) []Message {
	big := "<page>" + strings.Repeat("x", size) + "</page>"
	return []Message{
		say(RoleUser, "task"),
		called("c1", "web_fetch"), result("c1", big),
		called("c2", "get_diff"), result("c2", big),
	}
}

func TestAToolsOwnPreviewAppliesToItsResultsAlone(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100})
	sent := r.sendWith(t, twoResults(40<<10), named("web_fetch", pagePreview), named("get_diff", Preview{}))

	page, diff := ends(text(sent[2])), text(sent[4])
	if !strings.Contains(page, "read the rest with read_output") {
		t.Fatalf("a 40 KiB page over web_fetch's 32 KiB was sent whole")
	}
	if !strings.HasPrefix(page, "<page>") || !strings.Contains(page, "</page>") {
		t.Fatalf("the page's preview lost an end of its envelope")
	}
	if head, _, _ := strings.Cut(page, "\n…\n"); len(head) != 24<<10 {
		t.Fatalf("the preview's head is %d bytes, want web_fetch's 24 KiB", len(head))
	}
	if diff != text(twoResults(40 << 10)[4]) {
		t.Fatalf("get_diff's 40 KiB result was previewed; it is under the default 48 KiB")
	}
}

func TestAToolThatSetsOnlyItsThresholdKeepsTheRunsHeadAndTail(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100, Preview: Preview{Head: 2 << 10, Tail: 1 << 10}})
	sent := r.sendWith(t, twoResults(40<<10), named("web_fetch", Preview{Over: 32 << 10}))

	page := ends(text(sent[2]))
	head, _, _ := strings.Cut(page, "\n…\n")
	if len(head) != 2<<10 {
		t.Fatalf("the head is %d bytes; want the run's 2 KiB under the tool's own threshold", len(head))
	}
}

func TestAResultExactlyAtItsThresholdIsSentWhole(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100})
	at := strings.Repeat("x", 32<<10)
	sent := r.sendWith(t, []Message{say(RoleUser, "task"), called("c1", "web_fetch"), result("c1", at)},
		named("web_fetch", pagePreview))

	if text(sent[2]) != at {
		t.Fatalf("a result of exactly 32 KiB was previewed; the threshold is a size it may reach")
	}
}

func TestAReadOutputResultIsNeverPreviewed(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100, Preview: Preview{Over: 16 << 10, Head: 4 << 10, Tail: 2 << 10}})
	read := strings.Repeat("1: a line\n", 5000) // about 50 KiB, over the run's 16 KiB
	sent := r.sendWith(t, []Message{say(RoleUser, "task"), called("c1", ReadOutputToolName), result("c1", read)},
		named(ReadOutputToolName, Preview{}))

	if text(sent[2]) != read {
		t.Fatalf("a read_output result was previewed, which would send the model back to read its own read")
	}
}

func TestAPreviewThatCannotStandInForAResultIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		run  Preview
		tool Preview
		want string
	}{
		"head and tail past the threshold":                {tool: Preview{Over: 10 << 10, Head: 8 << 10, Tail: 4 << 10}, want: `tool "web_fetch"`},
		"only a threshold, under the run's head and tail": {tool: Preview{Over: 4 << 10}, want: `tool "web_fetch"`},
		"a negative size":                                 {tool: Preview{Head: -1}, want: "negative"},
		"the run's own":                                   {run: Preview{Over: 4 << 10}, want: "Spec.Compaction.Preview"},
	} {
		err := checkCompaction(&Compaction{AfterMessages: 10, Preview: c.run}, []Tool{named("web_fetch", c.tool)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: checkCompaction = %v, want an error naming %s", name, err, c.want)
		}
	}
	if err := checkCompaction(&Compaction{AfterMessages: 10}, []Tool{named("web_fetch", pagePreview)}); err != nil {
		t.Errorf("web_fetch's 32/24/8 KiB was refused: %v", err)
	}
}

func TestReadOutputNamesTheToolWhoseOutputItReads(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{say(RoleUser, "task"), called("c1", "web_fetch"), result("c1", "one\ntwo\nthree")})

	out, err := readOutputTool(h).Invoke(context.Background(), json.RawMessage(`{"call":"c1","from_line":2,"lines":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "c1 (web_fetch):") || !strings.Contains(out, "2: two") {
		t.Fatalf("read_output said %q; want it to name web_fetch before the lines", out)
	}
}

func TestASkillIsNeverPreviewed(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100, Preview: Preview{Over: 16 << 10, Head: 4 << 10, Tail: 2 << 10}})
	body := strings.Repeat("Always write the commit subject in the imperative.\n", 1000) // about 50 KiB
	sent := r.sendWith(t, []Message{say(RoleUser, "task"), called("c1", SkillToolName), result("c1", body)},
		named(SkillToolName, Preview{}))

	if text(sent[2]) != body {
		t.Fatalf("a skill's body was previewed; a skill is kept word for word")
	}
}

// A result is sent whole up to the default 48 KiB, and past it as its ends,
// under a line that says it is only a part.
func TestPast48KiBAResultIsShownAsItsEnds(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100})
	at, over := strings.Repeat("a", 48<<10), strings.Repeat("b", 48<<10+1)
	sent := r.sendWith(t, []Message{
		say(RoleUser, "task"),
		called("c1", "get_diff"), result("c1", at),
		say(RoleAssistant, "next"),
		called("c2", "get_diff"), result("c2", over),
	}, named("get_diff", Preview{}))

	if text(sent[2]) != at {
		t.Errorf("a result of exactly 48 KiB was previewed")
	}
	shown := text(sent[5])
	if !strings.HasPrefix(shown, "[only part of the output of c2 (get_diff) is shown: its first 4 KiB and last 2 KiB of 48 KiB, 13 lines. The rest is not shown]\n") {
		t.Fatalf("the preview does not open by saying it is a part:\n%.200s", shown)
	}
	if head, _, _ := strings.Cut(ends(shown), "\n…\n"); len(head) != 4<<10 {
		t.Errorf("the head is %d bytes, want 4 KiB", len(head))
	}
}

// One turn's results are held to TurnResults in all, the largest previewed
// first, and the same transcript is shown the same way every time, so the
// cached prefix holds. A turn under the limit is left alone.
func TestATurnsResultsAreHeldToTheirLimit(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100})
	sizes := []int{45 << 10, 47 << 10, 40 << 10, 46 << 10, 47 << 10, 30 << 10, 3 << 10}
	msgs := []Message{say(RoleUser, "task")}
	calls := Message{Role: RoleAssistant}
	for i := range sizes {
		calls.ToolCalls = append(calls.ToolCalls, ToolCall{ID: fmt.Sprintf("t%d", i), Name: "get_diff"})
	}
	msgs = append(msgs, calls)
	for i, n := range sizes {
		msgs = append(msgs, result(fmt.Sprintf("t%d", i), strings.Repeat("x", n)))
	}
	msgs = append(msgs, called("lone", "get_diff"), result("lone", strings.Repeat("y", 47<<10)))

	sent := r.sendWith(t, msgs, named("get_diff", Preview{}))
	total, cut := 0, []int{}
	for i := range sizes {
		m := text(sent[2+i])
		total += len(m)
		if m != text(msgs[2+i]) {
			cut = append(cut, i)
		}
	}
	if total > defaultTurnResults {
		t.Errorf("the turn's results were sent as %d bytes, over %d", total, defaultTurnResults)
	}
	if len(cut) != 2 || cut[0] != 1 || cut[1] != 4 {
		t.Errorf("previewed %v, want the two largest, 1 and 4, and no more than fit", cut)
	}
	if lone := text(sent[len(sent)-1]); lone != text(msgs[len(msgs)-1]) {
		t.Errorf("a turn under the limit was previewed")
	}
	again := r.sendWith(t, msgs, named("get_diff", Preview{}))
	for i := range sent {
		if text(sent[i]) != text(again[i]) {
			t.Fatalf("message %d was shown differently the second time; the cached prefix would break", i)
		}
	}
}

// read_output results and skills are never previewed to fit a turn: a read
// is already capped, and a skill is kept word for word.
func TestATurnOfReadsIsNotPreviewed(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100})
	calls := Message{Role: RoleAssistant}
	msgs := []Message{say(RoleUser, "task"), calls}
	for i := range 5 {
		id := fmt.Sprintf("r%d", i)
		msgs[1].ToolCalls = append(msgs[1].ToolCalls, ToolCall{ID: id, Name: ReadOutputToolName})
		msgs = append(msgs, result(id, strings.Repeat("r", 45<<10)))
	}
	sent := r.sendWith(t, msgs, named(ReadOutputToolName, Preview{}))
	for i := 2; i < len(msgs); i++ {
		if text(sent[i]) != text(msgs[i]) {
			t.Errorf("read %d was previewed to fit the turn", i-2)
		}
	}
}

func TestANegativeTurnLimitIsRefused(t *testing.T) {
	if err := checkCompaction(&Compaction{AfterMessages: 10, TurnResults: -1}, nil); err == nil {
		t.Error("a negative TurnResults was accepted")
	}
}

// A match broader than one read says how many lines matched and how many it
// shows, so the model narrows it instead of reading on blind.
func TestABroadMatchSaysHowMuchItLeftOut(t *testing.T) {
	h := &compactHandle{}
	long := strings.Repeat("match this line with some words in it\n", 2000)
	h.setFull([]Message{say(RoleUser, "task"), called("c1", "get_diff"), result("c1", long)})

	out, err := readOutputTool(h).Invoke(context.Background(), json.RawMessage(`{"call":"c1","match":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > readBytes+1024 || !strings.Contains(out, "of the 2000 matching lines shown") {
		t.Fatalf("a broad match returned %d bytes, ending %q", len(out), out[max(0, len(out)-200):])
	}
}

// A head and tail cut back to a rune are still named by the sizes asked for.
func TestAPreviewOfMultibyteTextNamesItsSizes(t *testing.T) {
	got := text(rig(Compaction{AfterMessages: 10}).client.shown(result("c", strings.Repeat("é", 30_000)), "t"))
	if !strings.Contains(got, "its first 4 KiB and last 2 KiB of 59 KiB") || !utf8.ValidString(got) {
		t.Fatalf("the header reads %.160q", got)
	}
}

// turnOf is one turn's results, each named by the tool that made it.
func turnOf(results ...[2]string) []Message {
	calls := Message{Role: RoleAssistant}
	msgs := []Message{say(RoleUser, "task"), calls}
	for i, r := range results {
		id := fmt.Sprintf("t%d", i)
		msgs[1].ToolCalls = append(msgs[1].ToolCalls, ToolCall{ID: id, Name: r[0]})
		msgs = append(msgs, result(id, r[1]))
	}
	return msgs
}

func sameResults(n int, tool string, size int) [][2]string {
	out := make([][2]string, n)
	for i := range out {
		out[i] = [2]string{tool, strings.Repeat("x", size)}
	}
	return out
}

func TestATurnIsHeldAsNearItsLimitAsPreviewsCan(t *testing.T) {
	page := Tool{Name: "page", Preview: Preview{Over: 100 << 10, Head: 1 << 10, Tail: 1 << 10}}
	for name, c := range map[string]struct {
		cfg     Compaction
		results [][2]string
		cleared int
		tools   []Tool
		cut     []int // indexes into the results previewed
		check   func(t *testing.T, sent []Message)
	}{
		"a limit of the run's own": {
			cfg: Compaction{AfterMessages: 100, TurnResults: 64 << 10}, results: sameResults(3, "get_diff", 30<<10), cut: []int{0, 1},
		},
		"a tool's own preview, in a turn": {
			cfg:     Compaction{AfterMessages: 100},
			results: append(sameResults(4, "get_diff", 45<<10), [2]string{"page", strings.Repeat("p", 45<<10)}),
			tools:   []Tool{named("get_diff", Preview{}), page},
			cut:     []int{4},
			check: func(t *testing.T, sent []Message) {
				if head, _, _ := strings.Cut(ends(text(sent[6])), "\n…\n"); len(head) != 1<<10 {
					t.Errorf("the page's head is %d bytes, want its own 1 KiB", len(head))
				}
			},
		},
		"a cleared result beside large ones": {
			cfg:     Compaction{AfterMessages: 100},
			results: append([][2]string{{"get_diff", strings.Repeat("c", 100<<10)}}, sameResults(5, "get_diff", 45<<10)...),
			cleared: 3, cut: []int{0, 1},
			check: func(t *testing.T, sent []Message) {
				if !strings.Contains(text(sent[2]), "cleared to save room") {
					t.Errorf("the cleared result was shown as %.80q, not its handle", text(sent[2]))
				}
			},
		},
		"a cleared result in a turn that cannot fit": {
			cfg:     Compaction{AfterMessages: 100},
			results: append([][2]string{{"get_diff", strings.Repeat("c", 100<<10)}}, sameResults(40, "get_diff", 6<<10)...),
			cleared: 3, cut: []int{0},
			check: func(t *testing.T, sent []Message) {
				if !strings.Contains(text(sent[2]), "cleared to save room") {
					t.Errorf("the cleared result was swapped for a longer preview: %.80q", text(sent[2]))
				}
			},
		},
		"exactly at the limit": {
			cfg: Compaction{AfterMessages: 100}, results: sameResults(4, "get_diff", 48<<10),
		},
		"a byte over the limit": {
			cfg:     Compaction{AfterMessages: 100},
			results: append(sameResults(4, "get_diff", 48<<10), [2]string{"get_diff", "x"}),
			cut:     []int{0},
		},
		"a limit of one byte": {
			cfg: Compaction{AfterMessages: 100, TurnResults: 1}, results: sameResults(3, "get_diff", 45<<10), cut: []int{0, 1, 2},
		},
		"skills, which are kept word for word": {
			cfg: Compaction{AfterMessages: 100}, results: sameResults(5, SkillToolName, 45<<10),
		},
		"reads, which are never previewed": {
			cfg: Compaction{AfterMessages: 100}, results: sameResults(7, ReadOutputToolName, 32<<10),
		},
		"results too small to preview": {
			cfg: Compaction{AfterMessages: 100}, results: sameResults(40, "get_diff", 6<<10),
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := rig(c.cfg)
			msgs := turnOf(c.results...)
			// As prepare would: each tool's own preview, and what is cleared.
			r.client.previews = previewsOf(c.tools, c.cfg.Preview.or(defaultPreview))
			r.client.state.cleared = c.cleared
			sent := r.client.view(msgs)
			var cut []int
			for i := range c.results {
				if text(sent[2+i]) != text(msgs[2+i]) {
					cut = append(cut, i)
				}
			}
			if fmt.Sprint(cut) != fmt.Sprint(c.cut) {
				t.Errorf("changed %v, want %v", cut, c.cut)
			}
			if c.check != nil {
				c.check(t, sent)
			}
		})
	}
}

// A match that fills the read exactly is shown whole; one line more is not,
// and says so.
func TestAMatchAtTheReadsLimitIsShownWhole(t *testing.T) {
	var lines []string
	total := 0
	for total < readBytes {
		n := len(lines) + 1
		line := "m" + strings.Repeat("x", 40)
		if out := len(fmt.Sprintf("%d: %s\n", n, line)); total+out > readBytes {
			line = "m" + strings.Repeat("x", readBytes-total-len(fmt.Sprintf("%d: m\n", n)))
		}
		total += len(fmt.Sprintf("%d: %s\n", n, line))
		lines = append(lines, line)
	}
	read := func(lines []string) string {
		h := &compactHandle{}
		h.setFull([]Message{say(RoleUser, "task"), called("c1", "get_diff"), result("c1", strings.Join(lines, "\n"))})
		out, err := readOutputTool(h).Invoke(context.Background(), json.RawMessage(`{"call":"c1","match":"^m"}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if total != readBytes {
		t.Fatalf("built %d bytes of matches, want exactly %d", total, readBytes)
	}
	if out := read(lines); strings.Contains(out, "[stopped") {
		t.Errorf("a match of exactly %d bytes was cut", readBytes)
	}
	more := append(slices.Clone(lines), "m one more")
	if out := read(more); !strings.Contains(out, fmt.Sprintf("%d of the %d matching lines shown", len(lines), len(more))) {
		t.Errorf("one line past the limit, the read ends %q", out[max(0, len(out)-160):])
	}
}

// A run reads only its own results: a call id it never made is refused.
func TestAReadOfAnotherRunsCallIsRefused(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{say(RoleUser, "task"), called("c1", "get_diff"), result("c1", "mine")})
	_, err := readOutputTool(h).Invoke(context.Background(), json.RawMessage(`{"call":"parent_call_9"}`))
	if err == nil || !strings.Contains(err.Error(), "no tool result in this run") {
		t.Fatalf("a call id from outside the run: %v, want it refused", err)
	}
}

func TestAReadThatCannotBeMadeSaysWhy(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{say(RoleUser, "task"), called("c1", "get_diff"), result("c1", "one\ntwo")})
	read := readOutputTool(h).Invoke
	if _, err := read(context.Background(), json.RawMessage(`{"call":"c1","match":"("}`)); err == nil || !strings.Contains(err.Error(), "not a regular expression") {
		t.Errorf("a bad expression: %v, want it named", err)
	}
	if out, err := read(context.Background(), json.RawMessage(`{"call":"c1","from_line":9}`)); err != nil || !strings.Contains(out, "It has no line 9.") {
		t.Errorf("a line past the end: %q, %v; want it said, not an error", out, err)
	}
}
