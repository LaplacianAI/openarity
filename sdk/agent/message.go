package agent

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentText  ContentType = "text"
	ContentImage ContentType = "image"
	ContentFile  ContentType = "file"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    []Content  `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Compaction marks a summary a compacting run wrote into its transcript.
	// It is sent to a model as the user message it reads as.
	Compaction *CompactionSummary `json:"compaction,omitempty"`
	// Reasoning is what the model thought on its way to this turn, when the
	// provider says.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
}

// Reasoning is a model's thinking, kept as the provider sent it. A provider
// may need it back: DeepSeek refuses a request with tools that leaves it out,
// and a signed or encrypted block only means anything returned untouched.
type Reasoning struct {
	// Text is the readable part, for showing. It may be empty when the
	// provider only sent reasoning it encrypted.
	Text string `json:"text,omitempty"`
	// Model is the one that wrote it. Fields are sent back to that model
	// alone: another provider may refuse a field it does not know.
	Model string `json:"model,omitempty"`
	// Fields are the provider's reasoning fields, by their name on the wire,
	// as they arrived.
	Fields map[string]json.RawMessage `json:"fields,omitempty"`
}

// Size is how many bytes sending the reasoning back costs.
func (r *Reasoning) Size() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Fields {
		n += len(f)
	}
	return n
}

type Content struct {
	Type      ContentType `json:"type"`
	Text      string      `json:"text,omitempty"`
	Blob      *Blob       `json:"blob,omitempty"`
	Cacheable bool        `json:"cacheable,omitempty"`
}

type Blob struct {
	MediaType string `json:"media_type"`
	Name      string `json:"name,omitempty"`
	Data      []byte `json:"data,omitempty"`
	// Ref names bytes the caller keeps elsewhere, such as an encrypted
	// object store, so a saved transcript holds the name rather than the
	// bytes. The caller fills Data before each request, typically with a
	// ModelClient that wraps the real one; a model client refuses a blob that
	// still has no Data, so an unresolved file never reaches a model as an
	// empty one.
	Ref string `json:"ref,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (m Message) Text() string {
	var b []byte
	for _, c := range m.Content {
		if c.Type == ContentText {
			b = append(b, c.Text...)
		}
	}
	return string(b)
}
