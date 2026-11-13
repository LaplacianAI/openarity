package patterns

import (
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// heard is what a run said and thought, in order: "t:" for text, "r:" for
// reasoning.
func heard(t *testing.T, p agent.Pattern, model *fakeModel, s agent.Spec) []string {
	t.Helper()
	events := make(chan agent.Event, 256)
	if _, err := p.Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model, Events: events}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)
	var got []string
	for e := range events {
		switch e := e.(type) {
		case agent.TextEvent:
			got = append(got, "t:"+e.Delta)
		case agent.ReasoningEvent:
			got = append(got, "r:"+e.Delta)
		}
	}
	return got
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func thinking(r turn, thought []string, said ...string) turn {
	r.thinking, r.saying = thought, said
	return r
}

func reasoned(msg agent.Message, thought string) agent.Message {
	msg.Reasoning = &agent.Reasoning{Text: thought, Model: "probe"}
	return msg
}

// What a model thinks streams as reasoning, ahead of what it says.
func TestAStreamedTurnsReasoningIsNotText(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{thinking(answered(text("It timed out.")), []string{"Logs ", "say so."}, "It timed out.")}}
	got := heard(t, ReActStreaming(), model, spec())
	if want := []string{"r:Logs ", "r:say so.", "t:It timed out."}; !same(got, want) {
		t.Errorf("heard %q, want %q", got, want)
	}
}

func TestACompletedTurnsReasoningIsNotText(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(reasoned(text("It timed out."), "Logs say so."))}}
	got := heard(t, ReAct(), model, spec())
	if want := []string{"r:Logs say so.", "t:It timed out."}; !same(got, want) {
		t.Errorf("heard %q, want %q", got, want)
	}
}

// The turn keeps its reasoning in the transcript, so the next call sends it
// back: DeepSeek refuses a run with tools whose history leaves it out.
func TestTheNextCallSeesTheTurnsReasoning(t *testing.T) {
	t.Parallel()

	var ran bool
	first := reasoned(calling("lookup", `{}`), "Need the record.")
	model := &fakeModel{turns: []turn{answered(first), answered(text("done"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(tool("lookup", "x", nil, &ran)), Messages: ask("go"), Model: model}); err != nil {
		t.Fatal(err)
	}
	second := model.calls[1].Messages
	if r := second[1].Reasoning; r == nil || r.Text != "Need the record." {
		t.Fatalf("the second call's history holds %+v", second[1])
	}
}

// Each loop's own calls are its working, and only its answer is said.
func TestALoopsWorkingIsReasoning(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		p     agent.Pattern
		s     agent.Spec
		turns []turn
		want  []string
	}{
		"plan's checklist": {
			PlanStreaming(), planSpec(),
			[]turn{
				thinking(answered(text("1. Look.")), nil, "1. ", "Look."),
				thinking(answered(text("Done.")), nil, "Done."),
			},
			[]string{"r:1. ", "r:Look.", "t:Done."},
		},
		"rewoo's plan": {
			ReWOOStreaming(), rewooSpec(tool("count", "3", nil, nil)),
			[]turn{
				answered(func() agent.Message {
					m := submitted(`[{"tool":"count","args":{},"why":"a"}]`).resp.Message
					m.Content = []agent.Content{{Type: agent.ContentText, Text: "I will count."}}
					return m
				}()),
				thinking(answered(text("Three.")), nil, "Three."),
			},
			[]string{"r:I will count.", "t:Three."},
		},
		"reflection's critique": {
			ReflectionStreaming(1), reflectSpec(),
			[]turn{
				thinking(answered(text("Draft.")), nil, "Draft."),
				func() turn {
					c := critiqued(true, "be exact")
					c.resp.Message.Content = []agent.Content{{Type: agent.ContentText, Text: "Too vague."}}
					return c
				}(),
				thinking(answered(text("Exact.")), nil, "Exact."),
			},
			[]string{"t:Draft.", "r:Too vague.", "t:Exact."},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := heard(t, c.p, &fakeModel{turns: c.turns}, c.s)
			if !same(got, c.want) {
				t.Errorf("heard %q, want %q", got, c.want)
			}
		})
	}
}
