package runtime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func texts(t *testing.T, h heldRun) []string {
	t.Helper()
	var out []string
	for _, e := range h.events(t) {
		if e.Kind == "text" {
			out = append(out, payload[textPayload](t, e).Text)
		}
	}
	return out
}

// Text is gathered, and written before the call it led to, so a reader sees
// what the model said, then what it did.
func TestAStreamedReplyIsGatheredAndKeptInOrder(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	st := feedStreaming(t, h.store, h.id,
		agent.TextEvent{Delta: "Let me "}, agent.TextEvent{Delta: "look."},
		agent.ToolCallEvent{ID: "c1", Name: "team", Arguments: json.RawMessage(`{}`)},
		agent.ToolResultEvent{ID: "c1", Name: "team", Output: "{}"},
		agent.TextEvent{Delta: "Done"},
	)
	wantNoStop(t, st)

	if got, want := kinds(h.events(t)), []string{"text", "tool_call", "tool_result", "text"}; !slices.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
	if got := texts(t, h); !slices.Equal(got, []string{"Let me look.", "Done"}) {
		t.Errorf("texts = %q; want the deltas joined, the last flushed at the end", got)
	}
}

func TestALongReplyIsWrittenInPiecesThatAddUp(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	var deltas []agent.Event
	var want strings.Builder
	for i := range 300 {
		d := strings.Repeat(string(rune('a'+i%26)), 4)
		want.WriteString(d)
		deltas = append(deltas, agent.TextEvent{Delta: d})
	}
	wantNoStop(t, feedStreaming(t, h.store, h.id, deltas...))

	got := texts(t, h)
	if len(got) < 2 || strings.Join(got, "") != want.String() {
		t.Errorf("%d pieces adding up to %d bytes; want several adding up to %d", len(got), len(strings.Join(got, "")), want.Len())
	}
	for _, p := range got[:len(got)-1] {
		if len(p) < textFlushBytes {
			t.Errorf("a piece of %d bytes was written before %d were held", len(p), textFlushBytes)
		}
	}
}

// A model that pauses mid-sentence is still read as it writes.
func TestTextHeldTooLongIsWrittenWithoutWaitingForMore(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	st := &stops{}
	ch := make(chan agent.Event)
	done := record(t.Context(), h.store, h.id, worker, newSoftLines(maxTokens, 0), true, nil, ch, nil, st.stop)
	ch <- agent.TextEvent{Delta: "Thinking"}

	deadline := time.Now().Add(5 * time.Second)
	for len(texts(t, h)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the text was never written while the run went on")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(ch)
	<-done
	if got := texts(t, h); !slices.Equal(got, []string{"Thinking"}) {
		t.Errorf("texts = %q, want it once", got)
	}
}

func TestANoteIsWrittenAfterTheTextBeforeIt(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)

	st := &stops{}
	ch := make(chan agent.Event)
	notes := make(chan note)
	done := record(t.Context(), h.store, h.id, worker, newSoftLines(maxTokens, 0), true, nil, ch, notes, st.stop)
	ch <- agent.TextEvent{Delta: "Proposing"}
	n := note{kind: "proposed", payload: map[string]any{}, done: make(chan error, 1)}
	notes <- n
	if err := <-n.done; err != nil {
		t.Fatal(err)
	}
	close(ch)
	<-done

	if got := kinds(h.events(t)); !slices.Equal(got, []string{"text", "proposed"}) {
		t.Errorf("kinds = %v, want the text first", got)
	}
}

func TestALostRunWritesNoText(t *testing.T) {
	t.Parallel()
	h := newHeldRun(t)
	h.takenOver(t)
	c := &counting{EventStore: h.store}

	st := feedStreaming(t, c, h.id, agent.StepEvent{Step: 1}, agent.TextEvent{Delta: "after"})

	if got := st.all(); len(got) != 1 || !errors.Is(got[0], ErrLostRun) {
		t.Errorf("stops = %v, want one ErrLostRun", got)
	}
	if c.appends != 1 {
		t.Errorf("%d appends; once lost, the text is not written", c.appends)
	}
}
