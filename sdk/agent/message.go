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
