package stream

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReaderReadsWhatFrameWrites(t *testing.T) {
	var b strings.Builder
	b.WriteString(": following\n\n")
	for i, e := range samples() {
		frame, err := Frame(Envelope{Seq: int64(i) - 3, RunID: "r", Event: e})
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(frame)
		b.WriteString(": keep-alive\n\n")
	}
	r := NewReader(strings.NewReader(b.String()))
	for i, want := range samples() {
		env, err := r.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if env.Seq != int64(i)-3 || env.Event.Kind() != want.Kind() {
			t.Fatalf("event %d: %+v", i, env)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
	if last, ok := r.Last(); !ok || last != int64(len(samples()))-4 {
		t.Fatalf("Last = %d, %v", last, ok)
	}
}

func TestReaderJoinsDataLinesAndSkipsWhatIsNotAnEvent(t *testing.T) {
	in := "data: {\"orphan\":true}\n\n" + // no event field
		"retry: 100\n" +
		"id:4\nevent:step_started\ndata: {\"step\":\ndata: 9}\n\n"
	r := NewReader(strings.NewReader(in))
	env, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if step, ok := env.Event.(StepStarted); !ok || env.Seq != 4 || step.Step != 9 {
		t.Fatalf("got %+v", env)
	}
}

// Data split over lines is one value joined by newlines, as the SSE
// specification says: an Unknown keeps it byte for byte.
func TestReaderJoinsDataLinesWithNewlines(t *testing.T) {
	env, err := NewReader(strings.NewReader("id: 1\nevent: chart\ndata: {\"a\":1,\ndata: \"b\":2}\n\n")).Next()
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := env.Event.(Unknown); !ok || string(u.Data) != "{\"a\":1,\n\"b\":2}" {
		t.Fatalf("got %#v", env.Event)
	}
}

func TestReaderBeforeAnyEventHasNoLast(t *testing.T) {
	if _, ok := NewReader(strings.NewReader("")).Last(); ok {
		t.Fatal("a reader that read nothing has a last event")
	}
}

func TestReaderRefuses(t *testing.T) {
	cases := map[string]string{
		"an id that is not a number": "id: x\nevent: step_started\ndata: {}\n\n",
		"no id":                      "event: step_started\ndata: {}\n\n",
		"data that is not an object": "id: 1\nevent: step_started\ndata: 3\n\n",
		"a line too long":            "id: 1\nevent: custom\ndata: " + strings.Repeat("x", maxFrame+1) + "\n\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewReader(strings.NewReader(in)).Next(); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("got %v, want an error", err)
			}
		})
	}
}
