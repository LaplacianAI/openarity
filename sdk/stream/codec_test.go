package stream

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// samples has one event of every kind, each field set, so a round trip that
// drops a field fails.
func samples() []Event {
	return []Event{
		Message{ID: "m1", UserID: "u1", Parts: []Part{{Type: PartText, Text: "hi"}}, State: StatePending},
		MessageConsumed{MessageIDs: []string{"m1", "m2"}},
		RunStarted{ParentRunID: "r0", Agent: "billing"},
		StepStarted{Step: 2},
		TextMessageChunk{Delta: "Hel", Truncated: true},
		ToolCallChunk{ToolCallID: "c1", Name: "search", Arguments: `{"q":"x"}`, Truncated: true},
		ToolCallResult{ToolCallID: "c1", Name: "search", Content: "3 hits", Error: "slow", Truncated: true, DurationMS: 40},
		Usage{Model: "m", InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3},
		Compacted{Reason: "tokens", Cleared: 1, Folded: 2, Before: 3, After: 4, Summary: "s", Error: "e"},
		RunFinished{Status: StatusFailed, Output: "o", Error: "e"},
		Custom{Name: "wrap_up", Value: json.RawMessage(`{"left":40}`)},
	}
}

func TestEveryKindHasASample(t *testing.T) {
	var got []Kind
	for _, e := range samples() {
		got = append(got, e.Kind())
	}
	if !reflect.DeepEqual(got, Kinds()) {
		t.Fatalf("samples cover %v, Kinds is %v", got, Kinds())
	}
}

func TestEveryEventRoundTrips(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, e := range samples() {
		t.Run(string(e.Kind()), func(t *testing.T) {
			kind, data, err := Encode(Envelope{Seq: 7, At: at, RunID: "r1", Event: e})
			if err != nil {
				t.Fatal(err)
			}
			if kind != e.Kind() {
				t.Fatalf("kind %q, want %q", kind, e.Kind())
			}
			env, err := Decode(kind, data)
			if err != nil {
				t.Fatal(err)
			}
			want := Envelope{At: at, RunID: "r1", Event: e}
			if !reflect.DeepEqual(env, want) {
				t.Fatalf("got %+v, want %+v", env, want)
			}
		})
	}
}

func TestAFrameIsOneFlatObject(t *testing.T) {
	_, data, err := Encode(Envelope{
		At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600)), RunID: "r1",
		Event: StepStarted{Step: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"at":"2026-01-02T02:04:05Z","run_id":"r1","step":3}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestAMessageHasNoRunAndNoTime(t *testing.T) {
	_, data, err := Encode(Envelope{Event: MessageConsumed{MessageIDs: []string{"m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"message_ids":["m"]}` {
		t.Fatalf("got %s", got)
	}
	_, data, err = Encode(Envelope{RunID: "r", Event: Custom{Name: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"run_id":"r","name":"x"}` {
		t.Fatalf("got %s", got)
	}
}

// The envelope's fields are laid over the event's, so an event with a field
// of the same name would be shadowed on the wire and decoded wrongly.
func TestNoEventUsesTheEnvelopesNames(t *testing.T) {
	for _, e := range samples() {
		for _, name := range jsonNames(reflect.TypeOf(e)) {
			if name == "at" || name == "run_id" {
				t.Errorf("%s has a field named %q, which the envelope owns", e.Kind(), name)
			}
		}
	}
}

func TestAnUnknownKindIsKeptAndWrittenBackOnOneLine(t *testing.T) {
	data := []byte("{\n  \"at\": \"2026-11-13T00:00:00Z\",\n  \"shape\": \"bar\"\n}")
	env, err := Decode("chart", data)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := env.Event.(Unknown)
	if !ok || u.Of != "chart" {
		t.Fatalf("got %#v, want an Unknown chart", env.Event)
	}
	if env.At.IsZero() {
		t.Fatal("the envelope's time was not read from an unknown event")
	}
	kind, out, err := Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "chart" || string(out) != `{"at":"2026-11-13T00:00:00Z","shape":"bar"}` {
		t.Fatalf("got %s %s", kind, out)
	}
}

func TestAPartOfANewTypeKeepsItsType(t *testing.T) {
	env, err := Decode(KindMessage, []byte(`{"id":"m","parts":[{"type":"image","url":"/x.png"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := env.Event.(Message)
	if !ok || m.Parts[0].Type != "image" {
		t.Fatalf("got %#v, want a message whose part is an image", env.Event)
	}
}

func TestEncodeRefuses(t *testing.T) {
	cases := map[string]Envelope{
		"no event":                {},
		"a kind with a newline":   {Event: Unknown{Of: "x\nevent: run_finished", Data: json.RawMessage(`{}`)}},
		"a kind in capitals":      {Event: Unknown{Of: "Chart", Data: json.RawMessage(`{}`)}},
		"an empty kind":           {Event: Unknown{Data: json.RawMessage(`{}`)}},
		"a kind too long":         {Event: Unknown{Of: Kind(strings.Repeat("a", 65)), Data: json.RawMessage(`{}`)}},
		"unknown data not object": {Event: Unknown{Of: "x", Data: json.RawMessage(`[1]`)}},
		"unknown data malformed":  {Event: Unknown{Of: "x", Data: json.RawMessage(`{"a":}`)}},
		"custom value malformed":  {Event: Custom{Name: "x", Value: json.RawMessage(`{`)}},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Encode(env); err == nil {
				t.Fatal("encoded")
			}
		})
	}
}

func TestDecodeRefuses(t *testing.T) {
	cases := map[string]struct {
		kind Kind
		data string
		is   error
	}{
		"not an object":         {KindStepStarted, `[1]`, ErrNotObject},
		"empty":                 {KindStepStarted, ``, ErrNotObject},
		"a string":              {"chart", `"x"`, ErrNotObject},
		"malformed":             {KindStepStarted, `{"step":}`, nil},
		"a field of wrong type": {KindStepStarted, `{"step":"two"}`, nil},
		"a time that is not":    {KindStepStarted, `{"at":"yesterday"}`, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(c.kind, []byte(c.data))
			if err == nil {
				t.Fatal("decoded")
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Fatalf("got %v, want %v", err, c.is)
			}
		})
	}
}

func jsonNames(t reflect.Type) []string {
	var names []string
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}
	return names
}
