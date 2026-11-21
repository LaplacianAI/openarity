package harness

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// AgentQueries is every write an agent and its grants take, in one transaction.
type AgentQueries interface {
	CreateAgent(ctx context.Context, arg db.CreateAgentParams) (db.Agent, error)
	UpdateAgent(ctx context.Context, arg db.UpdateAgentParams) (db.Agent, error)
	AddAgentMCPServer(ctx context.Context, arg db.AddAgentMCPServerParams) error
	ClearAgentMCPServers(ctx context.Context, agentID uuid.UUID) error
	AddAgentSkills(ctx context.Context, arg db.AddAgentSkillsParams) error
	ClearAgentSkills(ctx context.Context, agentID uuid.UUID) error
	AddAgentDelegates(ctx context.Context, arg db.AddAgentDelegatesParams) error
	ClearAgentDelegates(ctx context.Context, agentID uuid.UUID) error
}

// Columns is the agent row req asks for, grants aside: the db.CreateAgentParams
// an insert takes, with a nil delegation stored as none and a nil switch as off.
func Columns(req Request) (db.CreateAgentParams, error) {
	p := db.CreateAgentParams{
		Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		ModelName: req.Model.Name, MaxTokens: req.Model.MaxTokens, Temperature: req.Model.Temperature,
		Pattern: req.Pattern, MaxSteps: req.MaxSteps, SteerContinuations: req.SteerContinuations,
		Parser: req.Parser, TokenBudget: req.TokenBudget, Delegation: delegationOf(req),
		Web: req.Web != nil && *req.Web, Browser: req.Browser != nil && *req.Browser,
		Workspace: req.Workspace != nil && *req.Workspace,
	}

	if req.OutputSchema != nil {
		raw, err := json.Marshal(req.OutputSchema)
		if err != nil {
			return db.CreateAgentParams{}, err
		}
		p.OutputSchema = raw
	}

	if pm := req.ParserModel; pm != nil {
		p.ParserModelName, p.ParserMaxTokens, p.ParserTemperature = &pm.Name, pm.MaxTokens, pm.Temperature
	}

	return p, nil
}

// RequestOf is the request Columns would turn into this row, grants aside:
// what an agent is, said the way it is asked for.
func RequestOf(row db.Agent) (Request, error) {
	delegation, web, browser, workspace := row.Delegation, row.Web, row.Browser, row.Workspace
	req := Request{
		Name: row.Name, Description: row.Description, Kind: Kind(row.Kind), Instructions: row.Instructions,
		Model:   ModelRef{Name: row.ModelName, MaxTokens: row.MaxTokens, Temperature: row.Temperature},
		Pattern: row.Pattern, MaxSteps: row.MaxSteps, SteerContinuations: row.SteerContinuations,
		Parser: row.Parser, TokenBudget: row.TokenBudget, Delegation: &delegation, Web: &web,
		Browser: &browser, Workspace: &workspace,
	}

	if row.OutputSchema != nil {
		var o OutputSchema
		if err := json.Unmarshal(row.OutputSchema, &o); err != nil {
			return Request{}, err
		}
		req.OutputSchema = &o
	}

	if row.ParserModelName != nil {
		req.ParserModel = &ParserModel{Name: *row.ParserModelName, MaxTokens: row.ParserMaxTokens, Temperature: row.ParserTemperature}
	}

	return req, nil
}

// UpdateParams is the update that writes the row Columns made over agent id.
func UpdateParams(id uuid.UUID, c db.CreateAgentParams) db.UpdateAgentParams {
	return db.UpdateAgentParams{
		ID: id, Name: c.Name, Description: c.Description, Instructions: c.Instructions,
		ModelName: c.ModelName, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
		Pattern: c.Pattern, MaxSteps: c.MaxSteps, SteerContinuations: c.SteerContinuations,
		OutputSchema: c.OutputSchema, Parser: c.Parser,
		ParserModelName: c.ParserModelName, ParserMaxTokens: c.ParserMaxTokens, ParserTemperature: c.ParserTemperature,
		TokenBudget: c.TokenBudget, Delegation: c.Delegation, Web: c.Web, Browser: c.Browser,
		Workspace: c.Workspace,
	}
}

// Regrant replaces what an agent may reach with what req grants it.
func Regrant(ctx context.Context, q AgentQueries, row db.Agent, req Request) error {
	if err := q.ClearAgentMCPServers(ctx, row.ID); err != nil {
		return err
	}
	if err := q.ClearAgentSkills(ctx, row.ID); err != nil {
		return err
	}
	if err := q.ClearAgentDelegates(ctx, row.ID); err != nil {
		return err
	}
	return Grant(ctx, q, row, req)
}

// Grant adds what req grants an agent: its servers, skills and delegates. It
// assumes the agent holds none of them yet; Regrant clears them first.
func Grant(ctx context.Context, q AgentQueries, row db.Agent, req Request) error {
	for _, g := range req.MCPServers {
		err := q.AddAgentMCPServer(ctx, db.AddAgentMCPServerParams{
			AgentID: row.ID, McpServerID: g.ID, TeamID: row.TeamID, Allow: g.Allow,
		})
		if err != nil {
			return err
		}
	}

	if len(req.Skills) > 0 {
		if err := q.AddAgentSkills(ctx, db.AddAgentSkillsParams{AgentID: row.ID, SkillIds: req.Skills, TeamID: row.TeamID}); err != nil {
			return err
		}
	}
	if len(req.Delegates) == 0 {
		return nil
	}
	return q.AddAgentDelegates(ctx, db.AddAgentDelegatesParams{AgentID: row.ID, DelegateIds: req.Delegates, TeamID: row.TeamID})
}
