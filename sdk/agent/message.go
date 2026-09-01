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
	Role       Role
	Content    []Content
	ToolCalls  []ToolCall
	ToolCallID string
}

type Content struct {
	Type      ContentType
	Text      string
	Blob      *Blob
	Cacheable bool
}

type Blob struct {
	MediaType string
	Name      string
	Data      []byte
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
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
