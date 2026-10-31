package agents

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Kind string

const KindCustom Kind = "custom"

type modelRef struct {
	Name        string   `json:"name"`
	MaxTokens   int32    `json:"max_tokens"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type parserModel struct {
	Name        string   `json:"name"`
	MaxTokens   *int32   `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type outputSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	JSON        json.RawMessage `json:"json"`
	Strict      bool            `json:"strict"`
}

type serverGrant struct {
	ID    uuid.UUID `json:"id"`
	Allow []string  `json:"allow,omitempty"`
}

type summary struct {
	ID          uuid.UUID `json:"id"`
	TeamID      uuid.UUID `json:"team_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kind        Kind      `json:"kind"`
	Model       modelRef  `json:"model"`
	Pattern     string    `json:"pattern"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type detail struct {
	summary
	Instructions       string        `json:"instructions"`
	MaxSteps           int32         `json:"max_steps"`
	SteerContinuations int32         `json:"steer_continuations"`
	OutputSchema       *outputSchema `json:"output_schema,omitempty"`
	Parser             bool          `json:"parser"`
	ParserModel        *parserModel  `json:"parser_model,omitempty"`
	MCPServers         []serverGrant `json:"mcp_servers"`
	Skills             []uuid.UUID   `json:"skills"`
	TokenBudget        *int64        `json:"token_budget,omitempty"`
	Delegation         string        `json:"delegation"`
	Delegates          []uuid.UUID   `json:"delegates"`
	Web                bool          `json:"web"`
}

type agentRequest struct {
	Name               string        `json:"name"`
	Description        string        `json:"description"`
	Kind               Kind          `json:"kind"`
	Instructions       string        `json:"instructions"`
	Model              modelRef      `json:"model"`
	Pattern            string        `json:"pattern"`
	MaxSteps           int32         `json:"max_steps"`
	SteerContinuations int32         `json:"steer_continuations"`
	OutputSchema       *outputSchema `json:"output_schema"`
	Parser             bool          `json:"parser"`
	ParserModel        *parserModel  `json:"parser_model"`
	MCPServers         []serverGrant `json:"mcp_servers"`
	Skills             []uuid.UUID   `json:"skills"`
	TokenBudget        *int64        `json:"token_budget"`
	Delegation         *string       `json:"delegation"`
	Delegates          []uuid.UUID   `json:"delegates"`
	Web                *bool         `json:"web"`
}

type agentCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}
