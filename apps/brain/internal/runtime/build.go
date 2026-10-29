package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

const (
	connectTimeout  = 30 * time.Second
	toolCallTimeout = 5 * time.Minute
)

type BuildStore interface {
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	ListGrantedMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.ListGrantedMCPServersRow, error)
	ListGrantedSkills(ctx context.Context, agentID uuid.UUID) ([]db.Skill, error)
	ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error)
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	CountOtherAgents(ctx context.Context, arg db.CountOtherAgentsParams) (int64, error)
}

type Objects interface {
	Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error)
}

type MCPClients interface {
	Client(ctx context.Context, s db.McpServer) (*http.Client, error)
}

type Builder struct {
	store          BuildStore
	objects        Objects
	mcp            MCPClients
	graph          *GraphDeps
	delegates      *DelegateDeps
	note           func(ctx context.Context, runID uuid.UUID, worker, kind string, payload any) error
	connectTimeout time.Duration
}

func NewBuilder(s BuildStore, o Objects, m MCPClients, g *GraphDeps) *Builder {
	return &Builder{store: s, objects: o, mcp: m, graph: g, connectTimeout: connectTimeout}
}

type Unavailable struct{ Name, Reason string }

type Built struct {
	Spec        agent.Spec
	Unavailable []Unavailable
	sessions    []*mcp.Session
}

func (b Built) Close() {
	for _, s := range b.sessions {
		_ = s.Close()
	}
}

func Patterns() []agent.Pattern {
	return []agent.Pattern{patterns.ReAct(), patterns.Plan(), patterns.ReWOO(), patterns.Reflection(1)}
}

func Loops() []string {
	names := make([]string, 0, len(Patterns()))
	for _, p := range Patterns() {
		names = append(names, string(p.Name()))
	}
	slices.Sort(names)
	return names
}

func (b *Builder) Build(ctx context.Context, run db.Run, session agent.Session, l Lineage) (Built, error) {
	a, err := b.agentFor(ctx, run)
	if err != nil {
		return Built{}, err
	}

	built := Built{Spec: specOf(a)}
	built.Spec.Session = session

	if a.ID != uuid.Nil {
		built.Spec.Tools, err = b.tools(ctx, a.ID, &built)
		if err != nil {
			built.Close()
			return Built{}, err
		}
	}
	if b.graph == nil {
		built.Unavailable = append(built.Unavailable, Unavailable{
			Name: GraphToolName, Reason: "no embedding model is configured, so the team's graph is not built",
		})
	} else {
		built.Spec.Tools = append(built.Spec.Tools, GraphTool(*b.graph, run.TeamID, b.reach(run, a)))
	}

	delegate, ok, err := b.delegateFor(ctx, a, run, l)
	if err != nil {
		built.Close()
		return Built{}, err
	}
	if ok {
		built.Spec.Tools = append(built.Spec.Tools, delegate)
	}
	if run.Hub {
		built.Spec.System = hubSystem(a, !ok)
		if b.note != nil {
			built.Spec.Tools = append(built.Spec.Tools, PlanTool(func(ctx context.Context, kind string, p any) error {
				return b.note(ctx, run.ID, l.Worker, kind, p)
			}))
		}
	}

	if a.ID != uuid.Nil {
		built.Spec.Skills, err = b.skills(ctx, run.TeamID, a.ID)
		if err != nil {
			built.Close()
			return Built{}, err
		}
	}
	return built, nil
}

func (b *Builder) delegateTool(ctx context.Context, a db.Agent, run db.Run, l Lineage) (agent.Tool, bool, error) {
	if b.delegates == nil || run.Depth >= maxDepth {
		return agent.Tool{}, false, nil
	}

	switch a.Delegation {
	case "team":
	case "listed":
		ids, err := b.delegates.Store.ListAgentDelegates(ctx, a.ID)
		if err != nil {
			return agent.Tool{}, false, fmt.Errorf("reading whom %q may delegate to: %w", a.Name, err)
		}
		if len(ids) == 0 {
			return agent.Tool{}, false, nil
		}
	default:
		return agent.Tool{}, false, nil
	}
	return DelegateTool(*b.delegates, a, run, l), true, nil
}

func (b *Builder) agent(ctx context.Context, run db.Run) (db.Agent, error) {
	gone := fmt.Errorf("the agent %q was deleted before the run started", run.AgentName)
	if run.AgentID == nil {
		return db.Agent{}, gone
	}

	a, err := b.store.GetAgent(ctx, *run.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Agent{}, gone
	}
	if err != nil {
		return db.Agent{}, fmt.Errorf("reading the agent %q: %w", run.AgentName, err)
	}
	return a, nil
}

func specOf(a db.Agent) agent.Spec {
	spec := agent.Spec{
		Model:              agent.ModelRef{Name: a.ModelName, MaxTokens: int(a.MaxTokens), Temperature: a.Temperature},
		Pattern:            agent.PatternName(a.Pattern),
		MaxSteps:           int(a.MaxSteps),
		SteerContinuations: int(a.SteerContinuations),
		Parser:             a.Parser,
	}
	if a.Instructions != "" {
		spec.System = agent.System(a.Instructions)
	}
	if a.OutputSchema != nil {
		spec.OutputSchema = &agent.OutputSchema{Name: "output", JSON: a.OutputSchema}
	}
	if a.ParserModelName != nil {
		spec.ParserModel = agent.ModelRef{
			Name: *a.ParserModelName, MaxTokens: int(a.MaxTokens), Temperature: a.ParserTemperature,
		}
		if a.ParserMaxTokens != nil {
			spec.ParserModel.MaxTokens = int(*a.ParserMaxTokens)
		}
	}
	return spec
}

func (b *Builder) tools(ctx context.Context, agentID uuid.UUID, built *Built) ([]agent.Tool, error) {
	grants, err := b.store.ListGrantedMCPServers(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading the agent's MCP servers: %w", err)
	}

	var out []agent.Tool
	owner := map[string]string{}
	for _, g := range grants {
		s := g.McpServer
		if s.Url == nil {
			built.Unavailable = append(built.Unavailable, Unavailable{
				Name: s.Name, Reason: "a command server runs in the workspace, which slice 3 adds",
			})
			continue
		}

		live, err := b.connect(ctx, s)
		if err != nil {
			return nil, err
		}
		built.sessions = append(built.sessions, live)

		for _, t := range live.Tools() {
			if !allowed(s, g.Allow, t.Name) {
				continue
			}
			if t.Name == GraphToolName || t.Name == DelegateToolName || t.Name == PlanToolName {
				built.Unavailable = append(built.Unavailable, Unavailable{
					Name:   t.Name,
					Reason: fmt.Sprintf("the MCP server %q offers a tool named %s, which is the brain's own", s.Name, t.Name),
				})
				continue
			}
			if first, taken := owner[t.Name]; taken {
				return nil, fmt.Errorf("the MCP servers %q and %q both offer %q", first, s.Name, t.Name)
			}
			owner[t.Name] = s.Name
			out = append(out, limited(t))
		}
	}
	return out, nil
}

func (b *Builder) connect(ctx context.Context, s db.McpServer) (*mcp.Session, error) {
	client, err := b.mcp.Client(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q: %w", s.Name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, b.connectTimeout)
	defer cancel()

	live, err := mcp.Connect(ctx, mcp.Server{Name: s.Name, URL: *s.Url, HTTPClient: client, Bare: s.Bare})
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q could not be reached: %w", s.Name, err)
	}
	return live, nil
}

func allowed(s db.McpServer, allow []string, name string) bool {
	if allow == nil {
		return true
	}
	if !s.Bare {
		name = strings.TrimPrefix(name, s.Name+"__")
	}
	return slices.Contains(allow, name)
}

func limited(t agent.Tool) agent.Tool {
	invoke := t.Invoke
	t.Invoke = func(ctx context.Context, args json.RawMessage) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
		defer cancel()
		return invoke(ctx, args)
	}
	return t
}

func (b *Builder) skills(ctx context.Context, teamID, agentID uuid.UUID) ([]agent.Skill, error) {
	rows, err := b.store.ListGrantedSkills(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading the agent's skills: %w", err)
	}

	out := make([]agent.Skill, 0, len(rows))
	for _, row := range rows {
		files, err := b.store.ListSkillFiles(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("reading the files of skill %q: %w", row.Name, err)
		}

		body := row.Body
		skill := agent.Skill{
			Name:        row.Name,
			Description: row.Description,
			Body:        func(context.Context) (string, error) { return body, nil },
		}
		for _, f := range files {
			skill.Resources = append(skill.Resources, b.resource(teamID, f))
		}
		out = append(out, skill)
	}
	return out, nil
}

func (b *Builder) resource(teamID uuid.UUID, f db.SkillFile) agent.Resource {
	return agent.Resource{
		Name: f.Path,
		Read: func(ctx context.Context) (string, error) {
			if !strings.HasPrefix(f.MediaType, "text/") {
				return "", fmt.Errorf("%s is %s, which cannot be read as text", f.Path, f.MediaType)
			}
			data, err := b.objects.Get(ctx, teamID, f.ObjectKey)
			if err != nil {
				return "", fmt.Errorf("reading %s: %w", f.Path, err)
			}
			return string(data), nil
		},
	}
}
