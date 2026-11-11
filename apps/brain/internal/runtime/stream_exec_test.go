package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// streaming is the scripted model, streaming each answer's text in two
// halves before its final response, as a provider does.
type streaming struct{ *scripted }

func (m streaming) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	resp, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	text := resp.Message.Text()
	half := len(text) / 2
	events := []agent.StreamEvent{{Text: text[:half]}, {Text: text[half:]}, {Final: &resp}}
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

// Every other run keeps the loop it had: nothing streams, nothing changes.
func TestAnAgentRunDoesNotStream(t *testing.T) {
	model := streaming{newScripted(answer("Hello.", 10, 5))}
	f := newExecFixture(t, model.scripted)
	f.streams(t, model)

	f.mustExecute(t)

	for _, e := range f.events(t) {
		if e.Kind == "text" {
			t.Fatalf("an agent run wrote a text event: %s", e.Payload)
		}
	}
}
