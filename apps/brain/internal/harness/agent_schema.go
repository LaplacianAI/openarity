package harness

import (
	"encoding/json"

	"github.com/google/uuid"
)

type Kind string

const KindCustom Kind = "custom"

type ModelRef struct {
	Name        string   `json:"name"`
	MaxTokens   int32    `json:"max_tokens"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type ParserModel struct {
	Name        string   `json:"name"`
	MaxTokens   *int32   `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type OutputSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	JSON        json.RawMessage `json:"json"`
	Strict      bool            `json:"strict"`
}

type ServerGrant struct {
	ID    uuid.UUID `json:"id"`
	Allow []string  `json:"allow,omitempty"`
}

// ToolNote is a usage note on one tool for this agent, left by an approved
// improvement. Read-only here: a PUT neither sends nor clears notes.
type ToolNote struct {
	Server uuid.UUID `json:"server"`
	Tool   string    `json:"tool"`
	Note   string    `json:"note"`
}

type Request struct {
	Name               string        `json:"name"`
	Description        string        `json:"description"`
	Kind               Kind          `json:"kind"`
	Instructions       string        `json:"instructions"`
	Model              ModelRef      `json:"model"`
	Pattern            string        `json:"pattern"`
	MaxSteps           int32         `json:"max_steps"`
	SteerContinuations int32         `json:"steer_continuations"`
	OutputSchema       *OutputSchema `json:"output_schema"`
	Parser             bool          `json:"parser"`
	ParserModel        *ParserModel  `json:"parser_model"`
	MCPServers         []ServerGrant `json:"mcp_servers"`
	Skills             []uuid.UUID   `json:"skills"`
	TokenBudget        *int64        `json:"token_budget"`
	Delegation         *string       `json:"delegation"`
	Delegates          []uuid.UUID   `json:"delegates"`
	Web                *bool         `json:"web"`
	Browser            *bool         `json:"browser"`
	// Workspace is Bash, Read, Write, Edit, Glob and Grep in a room of the
	// session's, offered when the team has a sandbox.
	Workspace *bool `json:"workspace"`
}
