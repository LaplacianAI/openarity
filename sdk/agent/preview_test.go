package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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

	page, diff := text(sent[2]), text(sent[4])
	if !strings.Contains(page, "Read the rest with read_output") {
		t.Fatalf("a 40 KiB page over web_fetch's 32 KiB was sent whole")
	}
	if !strings.HasPrefix(page, "<page>") || !strings.Contains(page, "</page>") {
		t.Fatalf("the page's preview lost an end of its envelope")
	}
	if head, _, _ := strings.Cut(page, "\n…\n"); len(head) != 24<<10 {
		t.Fatalf("the preview's head is %d bytes, want web_fetch's 24 KiB", len(head))
	}
	if diff != text(twoResults(40 << 10)[4]) {
		t.Fatalf("get_diff's 40 KiB result was previewed; it is under the run's 100 KiB")
	}
}

func TestAToolThatSetsOnlyItsThresholdKeepsTheRunsHeadAndTail(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100, Preview: Preview{Head: 2 << 10, Tail: 1 << 10}})
	sent := r.sendWith(t, twoResults(40<<10), named("web_fetch", Preview{Over: 32 << 10}))

	page := text(sent[2])
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
		"only a threshold, under the run's head and tail": {tool: Preview{Over: 8 << 10}, want: `tool "web_fetch"`},
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
