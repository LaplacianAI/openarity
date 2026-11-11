package blueprints

import (
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
)

// Blueprint is what a team should hold. Everything in it is named, and it
// refers to its own items and the team's by name, never by id.
type Blueprint struct {
	MCPServers []serverSpec `json:"mcp_servers,omitempty"`
	Skills     []skillSpec  `json:"skills,omitempty"`
	Agents     []agentSpec  `json:"agents,omitempty"`
}

// serverSpec is an MCP server as the API takes it, with each secret reference
// relative to the team (name#key), so one file serves every team.
type serverSpec struct {
	Name       string            `json:"name"`
	URL        *string           `json:"url"`
	Command    []string          `json:"command"`
	Env        map[string]string `json:"env"`
	AuthSecret *string           `json:"auth_secret"`
	Bare       bool              `json:"bare"`
}

// skillSpec is a skill's files; its SKILL.md names it.
type skillSpec struct {
	Files []skillFile `json:"files"`
}

type skillFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

// agentSpec is an agent as the API takes it, except that what it is granted
// is named. These fields are shallower than the ones in agents.Request, so
// they are the ones JSON reads.
type agentSpec struct {
	agents.Request
	MCPServers []serverRef `json:"mcp_servers"`
	Skills     []string    `json:"skills"`
	Delegates  []string    `json:"delegates"`
}

type serverRef struct {
	Name  string   `json:"name"`
	Allow []string `json:"allow,omitempty"`
}

type applyRequest struct {
	Blueprint  Blueprint `json:"blueprint"`
	PlanDigest string    `json:"plan_digest"`
}

type Kind string

const (
	KindMCPServer Kind = "mcp_server"
	KindSkill     Kind = "skill"
	KindAgent     Kind = "agent"
)

type Action string

const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
)

// Change is one item's part in a plan. Fields name what an update changes,
// never what it changes to.
type Change struct {
	Kind   Kind       `json:"kind"`
	Name   string     `json:"name"`
	Action Action     `json:"action"`
	Fields []string   `json:"fields,omitempty"`
	ID     *uuid.UUID `json:"id,omitempty"`
}

// Refusal is why a plan cannot be applied. Any one refuses the whole plan.
type Refusal struct {
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

type Plan struct {
	Changes  []Change  `json:"changes"`
	Refusals []Refusal `json:"refusals"`
	Digest   string    `json:"digest"`
}

type applied struct {
	Changes []Change `json:"changes"`
}
