package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Envelope is an event where it sits in its conversation: its sequence
// number, when it happened, and the run it belongs to. A message belongs to
// no run.
type Envelope struct {
	Seq   int64
	At    time.Time
	RunID string
	Event Event
}

// head is the envelope's part of a frame's data, laid over the event's own
// fields. No event may use these names; a test holds every one to it.
type head struct {
	At    *time.Time `json:"at,omitempty"`
	RunID string     `json:"run_id,omitempty"`
}

// ErrNotObject is an event, or a frame's data, that is not a JSON object.
var ErrNotObject = errors.New("stream: an event's data must be a JSON object")

// Encode is an envelope as a frame's event and data: one flat JSON object, the
// envelope's at and run_id beside the event's own fields, as AG-UI's events
// are flat. The sequence number is the frame's id, not part of its data.
func Encode(env Envelope) (Kind, []byte, error) {
	if env.Event == nil {
		return "", nil, errors.New("stream: an envelope with no event")
	}
	if !validKind(env.Event.Kind()) {
		return "", nil, fmt.Errorf("stream: %q is not an event kind", env.Event.Kind())
	}
	if u, ok := env.Event.(Unknown); ok {
		if !isObject(u.Data) {
			return "", nil, ErrNotObject
		}
		// Kept as it came, but on one line: a newline between its tokens
		// would end the frame's data early.
		var flat bytes.Buffer
		if err := json.Compact(&flat, u.Data); err != nil {
			return "", nil, fmt.Errorf("stream: encoding a %s event: %w", u.Of, err)
		}
		return u.Of, flat.Bytes(), nil
	}
	body, err := json.Marshal(env.Event)
	if err != nil {
		return "", nil, fmt.Errorf("stream: encoding a %s event: %w", env.Event.Kind(), err)
	}
	h := head{RunID: env.RunID}
	if !env.At.IsZero() {
		at := env.At.UTC()
		h.At = &at
	}
	front, err := json.Marshal(h)
	if err != nil {
		return "", nil, fmt.Errorf("stream: encoding an envelope: %w", err)
	}
	return env.Event.Kind(), join(front, body), nil
}

// Decode is a frame's event and data as an envelope, with no sequence number:
// that is the frame's id. A kind this version does not know is an Unknown,
// not an error.
func Decode(kind Kind, data []byte) (Envelope, error) {
	if !isObject(data) {
		return Envelope{}, ErrNotObject
	}
	var h head
	if err := json.Unmarshal(data, &h); err != nil {
		return Envelope{}, fmt.Errorf("stream: decoding a %s event: %w", kind, err)
	}
	e, err := decodeEvent(kind, data)
	if err != nil {
		return Envelope{}, fmt.Errorf("stream: decoding a %s event: %w", kind, err)
	}
	env := Envelope{RunID: h.RunID, Event: e}
	if h.At != nil {
		env.At = *h.At
	}
	return env, nil
}

func decodeEvent(kind Kind, data []byte) (Event, error) {
	switch kind {
	case KindMessage:
		return as[Message](data)
	case KindMessageConsumed:
		return as[MessageConsumed](data)
	case KindRunStarted:
		return as[RunStarted](data)
	case KindStepStarted:
		return as[StepStarted](data)
	case KindTextMessageChunk:
		return as[TextMessageChunk](data)
	case KindReasoningChunk:
		return as[ReasoningChunk](data)
	case KindToolCallChunk:
		return as[ToolCallChunk](data)
	case KindToolCallResult:
		return as[ToolCallResult](data)
	case KindUsage:
		return as[Usage](data)
	case KindCompacted:
		return as[Compacted](data)
	case KindRunFinished:
		return as[RunFinished](data)
	case KindCustom:
		return as[Custom](data)
	}
	return Unknown{Of: kind, Data: append(json.RawMessage(nil), data...)}, nil
}

func as[T Event](data []byte) (Event, error) {
	var e T
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return e, nil
}

// validKind is a kind that fits in an SSE event field: lower-case letters,
// digits, underscores and dots, so no kind can end its line and start another.
func validKind(k Kind) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func isObject(data []byte) bool {
	t := bytes.TrimSpace(data)
	return len(t) >= 2 && t[0] == '{' && t[len(t)-1] == '}'
}

// join lays two JSON objects into one: {"a":1} and {"b":2} make {"a":1,"b":2}.
func join(a, b []byte) []byte {
	switch {
	case string(a) == "{}":
		return b
	case string(b) == "{}":
		return a
	}
	// Grown by append rather than sized up front: no length is computed from
	// the two, so none can overflow.
	out := append([]byte(nil), a[:len(a)-1]...)
	out = append(out, ',')
	return append(out, b[1:]...)
}
