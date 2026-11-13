package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// streaming is the scripted model, streaming each answer's reasoning, then
// its text in two halves, before its final response, as a provider does.
type streaming struct{ *scripted }

func (m streaming) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	resp, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	var events []agent.StreamEvent
	if r := resp.Message.Reasoning; r != nil {
		events = append(events, agent.StreamEvent{Reasoning: r.Text})
	}
	text := resp.Message.Text()
	half := len(text) / 2
	events = append(events, agent.StreamEvent{Text: text[:half]}, agent.StreamEvent{Text: text[half:]}, agent.StreamEvent{Final: &resp})
	return &replayed{events: events}, nil
}

type replayed struct {
	events []agent.StreamEvent
	cur    agent.StreamEvent
}

func (s *replayed) Next() bool {
	if len(s.events) == 0 {
		return false
	}
	s.cur, s.events = s.events[0], s.events[1:]
	return true
}

func (s *replayed) Event() agent.StreamEvent { return s.cur }
func (*replayed) Err() error                 { return nil }
func (*replayed) Close() error               { return nil }

func (f *execFixture) streams(t *testing.T, model streaming) {
	t.Helper()
	r, err := agent.New(func(agent.Endpoint) (agent.ModelClient, error) { return model, nil }, StreamingPatterns()...)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	f.exec.WithStreaming(r)
}

// A builder run's reply reaches the person as it is written, in text events
// that add up to the output it ends with.
func TestABuilderRunStreamsItsReply(t *testing.T) {
	model := streaming{newScripted(callTool("c1", TeamToolName, `{}`), answer("I propose a writer agent.", 10, 5))}
	f := newExecFixture(t, model.scripted)
	f.builderRun(t)
	f.exec.builder.WithWorkshop(realWorkshop(f))
	f.streams(t, model)

	f.mustExecute(t)

	var text []string
	for _, e := range f.events(t) {
		if e.Kind == "text" {
			text = append(text, payload[textPayload](t, e).Text)
		}
	}
	if got := strings.Join(text, ""); got != "I propose a writer agent." {
		t.Errorf("streamed %q, want the reply", got)
	}
	if out := f.row(t).Output; out == nil || *out != "I propose a writer agent." {
		t.Errorf("output = %v, want the reply as well", out)
	}
}

// A top-level run streams its answer too: the hub's, or an agent's asked
// directly, so whoever asked reads it as it is written.
func TestATopLevelRunStreamsItsAnswer(t *testing.T) {
	model := streaming{newScripted(answer("Hello there.", 10, 5))}
	f := newExecFixture(t, model.scripted)
	f.streams(t, model)

	f.mustExecute(t)

	var text strings.Builder
	for _, e := range f.events(t) {
		if e.Kind == "text" {
			var p struct{ Text string }
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			text.WriteString(p.Text)
		}
	}
	if text.String() != "Hello there." {
		t.Fatalf("streamed %q, want the answer", text.String())
	}
}

func TestWhichRunsStreamTheirText(t *testing.T) {
	parent := uuid.New()
	for name, c := range map[string]struct {
		row  db.Run
		want bool
	}{
		"a top-level run":   {db.Run{}, true},
		"a delegated run":   {db.Run{ParentRunID: &parent}, false},
		"the builder":       {db.Run{Builder: true}, true},
		"a builder's child": {db.Run{Builder: true, ParentRunID: &parent}, true},
	} {
		if got := streamsText(c.row); got != c.want {
			t.Errorf("%s: streams %v, want %v", name, got, c.want)
		}
	}
}
