// Package stream is one conversation as a stream of events: what people said,
// and what each run did in answer, in the order it happened. Every event has a
// sequence number, so a client that loses its connection asks for what came
// after the last one it saw and misses nothing.
//
// The event names follow AG-UI's (https://docs.ag-ui.com/concepts/events), so
// a client that speaks AG-UI needs a rename rather than a rewrite. Text and
// tool calls arrive as chunks, AG-UI's stateless form: a client that joins
// mid-answer needs no start event it never saw, and a run's finished event
// carries its whole answer.
//
// The package knows nothing about where events are kept. A Log is anything
// that can say what came after a sequence number; Memory is one, and a server
// with a database writes its own.
package stream

import (
	"encoding/json"
	"strconv"
)

// Kind names an event on the wire: an SSE frame's event field.
type Kind string

const (
	KindMessage          Kind = "message"
	KindMessageConsumed  Kind = "message_consumed"
	KindRunStarted       Kind = "run_started"
	KindStepStarted      Kind = "step_started"
	KindTextMessageChunk Kind = "text_message_chunk"
	KindReasoningChunk   Kind = "reasoning_message_chunk"
	KindToolCallChunk    Kind = "tool_call_chunk"
	KindToolCallResult   Kind = "tool_call_result"
	KindUsage            Kind = "usage"
	KindCompacted        Kind = "compacted"
	KindRunFinished      Kind = "run_finished"
	KindCustom           Kind = "custom"
)

// Kinds is every kind this version knows, in the order they are documented.
func Kinds() []Kind {
	return []Kind{
		KindMessage, KindMessageConsumed, KindRunStarted, KindStepStarted,
		KindTextMessageChunk, KindReasoningChunk, KindToolCallChunk, KindToolCallResult,
		KindUsage, KindCompacted, KindRunFinished, KindCustom,
	}
}

// Event is one thing that happened in a conversation.
type Event interface{ Kind() Kind }

// Message is something a person said. A message that asks for an answer is
// pending until a run takes it, which a MessageConsumed says.
type Message struct {
	ID     string `json:"id"`
	UserID string `json:"user_id,omitempty"`
	Parts  []Part `json:"parts"`
	State  State  `json:"state,omitempty"`
}

// State is where a message that asked for an answer stands.
type State string

// StatePending is a message no run has taken yet. A message that asks
// nothing, one said in a group without addressing the agent, has no state.
const StatePending State = "pending"

// Part is one piece of what a message carries: some text, or a file sent
// with it.
type Part struct {
	Type PartType `json:"type"`
	Text string   `json:"text,omitempty"`
	File *File    `json:"file,omitempty"`
}

// PartType says what a Part holds. A part of a type this version does not
// know is kept, with its type and nothing else.
type PartType string

const (
	PartText PartType = "text"
	PartFile PartType = "file"
)

// File is a file sent with a message, named rather than carried: a reader
// fetches its bytes from the server by its id.
type File struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// MessageConsumed is a run taking messages: the one it started from, or ones
// sent to it while it worked.
type MessageConsumed struct {
	MessageIDs []string `json:"message_ids"`
}

// RunStarted is a run beginning. A run another run handed work to names it as
// its parent.
type RunStarted struct {
	ParentRunID string `json:"parent_run_id,omitempty"`
	Agent       string `json:"agent"`
}

// StepStarted is a run making its next model call.
type StepStarted struct {
	Step int `json:"step"`
}

// TextMessageChunk is a piece of what a run is saying, in order. Pieces with
// the same MessageID are one message, a model call's text; the last of a run's
// is its answer, which RunFinished also carries whole.
type TextMessageChunk struct {
	MessageID string `json:"message_id,omitempty"`
	Delta     string `json:"delta"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReasoningChunk is a piece of what a run is thinking rather than saying: the
// model's own reasoning, or a pattern's working such as a plan or a critique.
// Pieces with the same MessageID are one reasoning message.
type ReasoningChunk struct {
	MessageID string `json:"message_id,omitempty"`
	Delta     string `json:"delta"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Which part of a model call a message holds, in a MessageID.
const (
	PartOfText      = "text"
	PartOfReasoning = "reasoning"
)

// MessageID names the message one model call of a run says or thinks:
// unique across a session, and the same whoever writes it.
func MessageID(runID string, step int, part string) string {
	return runID + ":" + strconv.Itoa(step) + ":" + part
}

// ToolCallChunk is a run calling a tool, with all of its arguments.
type ToolCallChunk struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ToolCallResult is what a tool call returned, or why it failed.
type ToolCallResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Usage is what one model call spent.
type Usage struct {
	Model             string `json:"model"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
}

// Compacted is a run shrinking its context to carry on.
type Compacted struct {
	Reason  string `json:"reason"`
	Cleared int    `json:"cleared"`
	Folded  int    `json:"folded"`
	Before  int    `json:"before"`
	After   int    `json:"after"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RunFinished is a run ending, with its whole answer when it has one.
type RunFinished struct {
	Status Status `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Status is how a run ended.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Custom is an event the catalog has no kind for: a server's own, named.
type Custom struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Unknown is an event of a kind this version does not know, kept as it came
// so a client built against an older catalog reads a newer stream. Data is
// the frame's whole data object, and is written back unchanged.
type Unknown struct {
	Of   Kind
	Data json.RawMessage
}

func (Message) Kind() Kind          { return KindMessage }
func (MessageConsumed) Kind() Kind  { return KindMessageConsumed }
func (RunStarted) Kind() Kind       { return KindRunStarted }
func (StepStarted) Kind() Kind      { return KindStepStarted }
func (TextMessageChunk) Kind() Kind { return KindTextMessageChunk }
func (ReasoningChunk) Kind() Kind   { return KindReasoningChunk }
func (ToolCallChunk) Kind() Kind    { return KindToolCallChunk }
func (ToolCallResult) Kind() Kind   { return KindToolCallResult }
func (Usage) Kind() Kind            { return KindUsage }
func (Compacted) Kind() Kind        { return KindCompacted }
func (RunFinished) Kind() Kind      { return KindRunFinished }
func (Custom) Kind() Kind           { return KindCustom }
func (u Unknown) Kind() Kind        { return u.Of }
