package runtime

import (
	"slices"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

func thought(tr turn, text string) turn {
	tr.resp.Message.Reasoning = &agent.Reasoning{Text: text, Model: "test/model"}
	return tr
}

// written is a run's recorded writing of one kind, joined, and the steps it
// was recorded under.
func written(t *testing.T, events []db.RunEvent, kind string) (string, []int) {
	t.Helper()
	var b strings.Builder
	var steps []int
	for _, e := range events {
		if e.Kind == kind {
			p := payload[textPayload](t, e)
			b.WriteString(p.Text)
			steps = append(steps, p.Step)
		}
	}
	return b.String(), steps
}

// What a model thinks is recorded apart from what it says, each under the
// step whose call wrote it, and never in the same row.
func TestReasoningIsRecordedApartFromText(t *testing.T) {
	model := streaming{newScripted(
		thought(callTool("c1", "nothing", `{}`), "Look first."),
		thought(answer("It timed out.", 10, 5), "The tool said so."),
	)}
	f := newExecFixture(t, model.scripted)
	f.streams(t, model)

	f.mustExecute(t)

	events := f.events(t)
	reasoning, rsteps := written(t, events, kindReasoning)
	text, tsteps := written(t, events, kindText)
	if reasoning != "Look first.The tool said so." || !slices.Equal(rsteps, []int{1, 2}) {
		t.Errorf("reasoning %q under steps %v", reasoning, rsteps)
	}
	if text != "It timed out." || !slices.Equal(tsteps, []int{2}) {
		t.Errorf("text %q under steps %v", text, tsteps)
	}
	// Each row of writing follows the step it is under, and the call's
	// reasoning comes before its text.
	got := kinds(events)
	at := slices.Index(got, kindReasoning)
	if at < 1 || got[at-1] != "step" {
		t.Errorf("events %v: reasoning does not follow its step", got)
	}
	last := slices.Index(got, kindText)
	if last < 0 || got[last-1] != kindReasoning {
		t.Errorf("events %v: the answer's text does not follow its reasoning", got)
	}
}

// What the model thought goes back with its turn on the next call, which
// DeepSeek requires of a run with tools.
func TestTheNextCallCarriesTheTurnsReasoning(t *testing.T) {
	model := streaming{newScripted(
		thought(callTool("c1", "nothing", `{}`), "Look first."),
		answer("done", 1, 1),
	)}
	f := newExecFixture(t, model.scripted)
	f.streams(t, model)

	f.mustExecute(t)

	reqs := model.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d calls", len(reqs))
	}
	var carried bool
	for _, m := range reqs[1].Messages {
		carried = carried || (m.Reasoning != nil && m.Reasoning.Text == "Look first.")
	}
	if !carried {
		t.Error("the second call's history lost the first turn's reasoning")
	}
	saved, err := f.sessions.Messages(t.Context(), transcriptOf(f.row(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(saved, func(m agent.Message) bool { return m.Reasoning != nil && m.Reasoning.Text == "Look first." }) {
		t.Error("the transcript kept no reasoning, so the session's next run would send its turn without it")
	}
}

// Every loop streams a top-level run's answer, and a loop's own working is
// recorded as reasoning, not as what the run said.
func TestEveryLoopStreamsItsAnswer(t *testing.T) {
	cases := map[string]struct {
		turns     []turn
		text      string
		reasoning string
	}{
		"plan": {
			[]turn{answer("1. Answer.", 1, 1), answer("Done.", 1, 1)},
			"Done.", "1. Answer.",
		},
		"rewoo": {
			[]turn{
				callTool("p1", patterns.PlanToolName, `{"steps":[{"tool":"nothing","args":{},"why":"w"}]}`),
				answer("Done.", 1, 1),
			},
			"Done.", "",
		},
		"reflection": {
			[]turn{
				answer("Draft.", 1, 1),
				callTool("c1", patterns.CritiqueToolName, `{"needs_refinement":true,"critique":"be exact"}`),
				answer("Exact.", 1, 1),
			},
			"Draft.Exact.", "",
		},
	}
	for loop, c := range cases {
		t.Run(loop, func(t *testing.T) {
			model := streaming{newScripted(c.turns...)}
			f := newExecFixture(t, model.scripted)
			f.mustExec(t, `UPDATE agents SET pattern = $1 WHERE id = $2`, loop, f.agent)
			f.streams(t, model)

			f.mustExecute(t)

			wantStatus(t, f.row(t), "succeeded", "")
			events := f.events(t)
			if text, _ := written(t, events, kindText); text != c.text {
				t.Errorf("streamed %q, want %q", text, c.text)
			}
			if reasoning, _ := written(t, events, kindReasoning); reasoning != c.reasoning {
				t.Errorf("reasoning %q, want %q", reasoning, c.reasoning)
			}
		})
	}
}
