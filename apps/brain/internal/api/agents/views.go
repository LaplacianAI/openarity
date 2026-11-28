package agents

import (
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
)

type summary struct {
	ID          uuid.UUID        `json:"id"`
	TeamID      uuid.UUID        `json:"team_id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Kind        harness.Kind     `json:"kind"`
	Model       harness.ModelRef `json:"model"`
	Pattern     string           `json:"pattern"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// listed is a row of the agents list: the summary, and what the agent may
// reach, as counts. The grants themselves are the detail's.
type listed struct {
	summary
	Delegation    string `json:"delegation"`
	Web           bool   `json:"web"`
	Browser       bool   `json:"browser"`
	Workspace     bool   `json:"workspace"`
	Schedule      bool   `json:"schedule"`
	ServerCount   int64  `json:"mcp_server_count"`
	SkillCount    int64  `json:"skill_count"`
	DelegateCount int64  `json:"delegate_count"`
}

type detail struct {
	summary
	Instructions       string                `json:"instructions"`
	MaxSteps           int32                 `json:"max_steps"`
	SteerContinuations int32                 `json:"steer_continuations"`
	OutputSchema       *harness.OutputSchema `json:"output_schema,omitempty"`
	Parser             bool                  `json:"parser"`
	ParserModel        *harness.ParserModel  `json:"parser_model,omitempty"`
	MCPServers         []harness.ServerGrant `json:"mcp_servers"`
	Skills             []uuid.UUID           `json:"skills"`
	TokenBudget        *int64                `json:"token_budget,omitempty"`
	Delegation         string                `json:"delegation"`
	Delegates          []uuid.UUID           `json:"delegates"`
	Web                bool                  `json:"web"`
	Browser            bool                  `json:"browser"`
	Workspace          bool                  `json:"workspace"`
	Schedule           bool                  `json:"schedule"`
	ToolNotes          []harness.ToolNote    `json:"tool_notes"`
}

type agentCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}
