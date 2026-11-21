package improve

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// Over budget, the oldest context goes first, then the trace's middle; the
// request, answer and ending stay.
func TestRenderKeepsWhatMattersWithinBudget(t *testing.T) {
	ev := evidence{
		request: "find the order", answer: "closed it", ending: "status succeeded",
		next: []string{"person: no, the other one"},
	}
	for i := range 20 {
		ev.context = append(ev.context, fmt.Sprintf("person: context %02d %s", i, strings.Repeat("x", 200)))
	}
	for i := range 400 {
		ev.trace = append(ev.trace, fmt.Sprintf("  step %03d %s", i, strings.Repeat("y", 50)))
	}

	whole := ev.render(1 << 20)
	if !strings.Contains(whole, "context 00") || strings.Contains(whole, "left out") {
		t.Fatal("within budget, nothing should be cut")
	}

	small := ev.render(8000)
	if n := utf8.RuneCountInString(small); n > 8000 {
		t.Fatalf("rendered %d characters over an 8000 budget", n)
	}
	for _, keep := range []string{"find the order", "closed it", "status succeeded", "no, the other one", "step 000", "step 399"} {
		if !strings.Contains(small, keep) {
			t.Errorf("cut %q, which must stay", keep)
		}
	}
	if strings.Contains(small, "context 00") {
		t.Error("the oldest context was kept while the trace was cut")
	}
	if !strings.Contains(small, "lines left out") {
		t.Error("the trace was cut without saying so")
	}
}

func TestCut(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e", "f"}
	got := cut(lines, 4)
	if len(got) != 5 || got[0] != "a" || got[1] != "b" || got[3] != "e" || got[4] != "f" || !strings.Contains(got[2], "2 lines") {
		t.Errorf("cut = %q", got)
	}
	if len(cut(lines, 10)) != 6 {
		t.Error("cut shortened lines within keep")
	}
}
