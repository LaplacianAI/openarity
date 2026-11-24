package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
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
	ListGrantedMCPTools(ctx context.Context, agentID uuid.UUID) ([]db.ListGrantedMCPToolsRow, error)
	ListGrantedSkills(ctx context.Context, agentID uuid.UUID) ([]db.Skill, error)
	ListAgentToolNotes(ctx context.Context, agentID uuid.UUID) ([]db.AgentToolNote, error)
	ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error)
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	CountOtherAgents(ctx context.Context, arg db.CountOtherAgentsParams) (int64, error)
	AttachmentStore
	HistoryStore
}

type Objects interface {
	Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error)
}

type MCPClients interface {
	// Reach is where a server is called and the client to call it with: its
	// URL, or its room's address when it is a command server.
	Reach(ctx context.Context, s db.McpServer) (string, *http.Client, error)
}

type Builder struct {
	store      BuildStore
	objects    Objects
	mcp        MCPClients
	graph      *GraphDeps
	search     *SearchDeps
	fetch      *FetchDeps
	browsers   *BrowserDeps
	workspaces *WorkspaceDeps
	delegates  *DelegateDeps
	workshop   Workshop
	registry   Registry
	approvals  ApprovalStore
	// rules tells a replay who the past run acted for and the team's
	// approval rules; a run reads both from its policy.
	rules          ReplayRules
	note           func(ctx context.Context, runID uuid.UUID, worker, kind string, payload any) error
	connectTimeout time.Duration
	// replay builds a past run again for a replay (replay.go): the tools a
	// run of the agent would hold, those that reach live state as stand-ins.
	replay bool
}

func NewBuilder(s BuildStore, o Objects, m MCPClients, g *GraphDeps, search *SearchDeps, fetch *FetchDeps) *Builder {
	return &Builder{store: s, objects: o, mcp: m, graph: g, search: search, fetch: fetch, connectTimeout: connectTimeout}
}

// WithRegistry lets builder runs look MCP servers up. Without one, they have
// no find_mcp_servers.
func (b *Builder) WithRegistry(r Registry) *Builder {
	b.registry = r
	return b
}

// WithWorkshop gives builder runs their reach. Without one, a builder run
// fails at its start rather than running with no way to propose.
func (b *Builder) WithWorkshop(ws Workshop) *Builder {
	b.workshop = ws
	return b
}

type Unavailable struct{ Name, Reason string }

type Built struct {
	Spec        agent.Spec
	Agent       string
	Unavailable []Unavailable
	servers     *runServers
	browser     *runBrowser
	// answerable is every tool a person could be asked about, by the name a
	// request records, as it is before the gate: what a resume makes once
	// the call is approved.
	answerable map[string]agent.Tool
	// live is whether a replay may make a read the team's rules would gate:
	// nil outside a replay.
	live func(at target) bool
	// reads is every tool, by its name in the run, that only reads: a
	// trusted server's read-only tool, the web for a person who is there,
	// the graph, and the conversation's history and files, which a replay's
	// store cuts to before the past run began. A replay runs these live and
	// no other.
	reads map[string]bool
}

// Close ends the sessions the run's calls opened. Safe to call twice, and on
// a Built that opened none.
func (b Built) Close() {
	if b.servers != nil {
		b.servers.close()
	}
	b.browser.close()
}

func Patterns() []agent.Pattern {
	return []agent.Pattern{patterns.ReAct(), patterns.Plan(), patterns.ReWOO(), patterns.Reflection(1)}
}

// StreamingPatterns are Patterns streaming the model's text as it writes,
// for the runs a person is waiting on.
func StreamingPatterns() []agent.Pattern {
	return []agent.Pattern{
		patterns.ReActStreaming(), patterns.PlanStreaming(), patterns.ReWOOStreaming(), patterns.ReflectionStreaming(1),
	}
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

	settings, err := b.teamSettings(ctx, run.TeamID)
	if err != nil {
		return Built{}, err
	}
	built := Built{Spec: specOf(a), Agent: a.Name, servers: &runServers{b: b}, answerable: map[string]agent.Tool{}, reads: map[string]bool{}}
	built.Spec.Session = session
	gates, err := b.policyFor(ctx, run, settings, built.Spec.Pattern, l)
	if err != nil {
		return Built{}, err
	}
	built.Spec.Compaction = compactionFor(settings, budgetOf(run.TokenBudget))
	// Who the run acts for: a call's class, and a replay's reads, turn on it.
	actor, err := b.actorFor(ctx, run, gates)
	if err != nil {
		return Built{}, err
	}
	if built.live, err = b.replayRules(ctx, run, settings, actor); err != nil {
		return Built{}, err
	}

	// The builder reads the web for servers' docs and a skill's source.
	web := run.Hub || a.Web || run.Builder
	// A browser runs a page's scripts and acts on it, so it is a grant of its
	// own. The builder is never the hub and has no agent, so it has none.
	browse := (run.Hub || a.Browser) && settings.BrowserProvider != noBrowserProvider
	// A workspace is the agent's own grant. A hub the team chose from its
	// agents holds it as any agent does, since a hub may act on its own as
	// well as hand work on; the built-in hub and the builder are no agent,
	// so neither has one.
	work := a.Workspace && (b.workspaces != nil || b.replay) &&
		settings.SandboxProvider != string(sandbox.None)
	var held catalogue
	if a.ID != uuid.Nil {
		held, err = b.tools(ctx, a.ID, l.Narrow, reservedFor{hub: run.Hub, web: web, browse: browse, work: work}, gates, &built)
		if err != nil {
			built.Close()
			return Built{}, err
		}
	}
	// The MCP tools are found with graph rather than sent, when the SDK
	// judges them too many to send. With no graph there is nothing to find
	// them with, so they are sent.
	if b.graph == nil {
		built.Unavailable = append(built.Unavailable, Unavailable{
			Name: GraphToolName, Reason: "no embedding model is configured, so the team's graph is not built",
		})
		built.Spec.Tools = append(built.Spec.Tools, held.tools...)
	} else {
		reach := b.reach(run, a)
		built.reads[GraphToolName] = true
		built.Spec.Tools = append(built.Spec.Tools, GraphTool(*b.graph, run.TeamID, reach, held))
		if len(held.tools) > 0 {
			built.Spec.Discovery = &agent.Discovery{
				Search: GraphToolName, Tools: held.tools, Groups: held.groups,
				Select: SelectTools(*b.graph, run.TeamID, reach, held),
			}
		}
	}

	delegate, ok, err := b.delegateFor(ctx, a, run, l)
	if err != nil {
		built.Close()
		return Built{}, err
	}
	if ok {
		built.Spec.Tools = append(built.Spec.Tools, delegate)
	}
	if run.Builder {
		built.Spec.Tools = append(built.Spec.Tools, b.builderTools(run, l)...)
		built.Spec.System = agent.System(BuilderPrompt)
	}
	if run.Hub {
		built.Spec.System = hubSystem(a, b.alone(ok))
		if b.note != nil {
			built.Spec.Tools = append(built.Spec.Tools, PlanTool(func(ctx context.Context, kind string, p any) error {
				return b.note(ctx, run.ID, l.Worker, kind, p)
			}))
		}
	}

	// Bound only where there is something to read. The builder too: the tools
	// read and write nothing.
	files, err := b.store.ListAttachmentsBySession(ctx, db.ListAttachmentsBySessionParams{SessionID: run.SessionID, PageSize: 1})
	if err != nil {
		built.Close()
		return Built{}, fmt.Errorf("reading whether the conversation holds files: %w", err)
	}
	if len(files) > 0 {
		for _, t := range attachmentTools(b.store, run.SessionID) {
			built.reads[t.Name] = true
			built.Spec.Tools = append(built.Spec.Tools, t)
		}
	}

	// The conversation is the top-level run's: a delegated agent is sent a
	// task. Only when there is more than the run's own message to read.
	if run.ParentRunID == nil {
		said, err := b.store.ListSessionHistory(ctx, db.ListSessionHistoryParams{
			SessionID: run.SessionID, Before: math.MaxInt64, PageSize: 2,
		})
		if err != nil {
			built.Close()
			return Built{}, fmt.Errorf("reading whether the conversation has history: %w", err)
		}
		if len(said) > 1 {
			h := historyTool(b.store, run.SessionID)
			built.reads[h.Name] = true
			built.Spec.Tools = append(built.Spec.Tools, h)
		}
	}

	if web {
		// Reaching the internet is a read for a person who is there, and a
		// write for a guest or a run nobody is present for.
		egress := approval.ClassFor(approval.Egress, actor)
		tools, unavailable := b.webTools(ctx, run.TeamID, settings)
		built.Unavailable = append(built.Unavailable, unavailable...)
		for _, t := range tools {
			built.answerable[t.Name] = t
			built.reads[t.Name] = egress == approval.Read && built.allows(target{name: t.Name, class: egress})
			gated, refused := gates.gate(t, target{name: t.Name, class: egress})
			if refused != nil {
				built.Unavailable = append(built.Unavailable, *refused)
				continue
			}
			built.Spec.Tools = append(built.Spec.Tools, gated)
		}
	}

	if browse && b.replay {
		// A page has moved on since: the replay's browser tools are stand-ins.
		built.Spec.Tools = append(built.Spec.Tools, browserStandIns()...)
	} else if browse {
		r, tools, unavailable := b.browserFor(ctx, run.TeamID, settings)
		built.browser = r
		built.Unavailable = append(built.Unavailable, unavailable...)
		for _, t := range tools {
			built.answerable[t.Name] = t
		}
		gated, refused, asks := gates.gateBrowser(tools)
		if r != nil {
			r.bound = asks
		}
		built.Unavailable = append(built.Unavailable, refused...)
		built.Spec.Tools = append(built.Spec.Tools, gated...)
	}

	switch {
	case work && b.replay:
		// The workspace has moved on since: the replay's are stand-ins.
		built.Spec.Tools = append(built.Spec.Tools, (&runWorkspace{}).tools()...)
	case work:
		ws := &runWorkspace{deps: *b.workspaces, team: run.TeamID, session: run.SessionID}
		for _, t := range ws.tools() {
			built.answerable[t.Name] = t
			gated, refused := gates.gate(t, target{name: t.Name, class: workspaceClass(t.Name)})
			if refused != nil {
				built.Unavailable = append(built.Unavailable, *refused)
				continue
			}
			built.Spec.Tools = append(built.Spec.Tools, gated)
		}
	case a.Workspace:
		reason := "the team has no sandbox, so its agents have no workspace: choose one in Settings"
		if b.workspaces == nil {
			reason = "this brain's secret store is read-only, so it keeps no sandbox room's token and its agents have no workspace"
		}
		for _, n := range WorkspaceToolNames {
			built.Unavailable = append(built.Unavailable, Unavailable{Name: n, Reason: reason})
		}
	}

	if a.ID != uuid.Nil {
		built.Spec.Skills, err = b.skills(ctx, run.TeamID, a.ID, l.Narrow)
		if err != nil {
			built.Close()
			return Built{}, err
		}
	}
	return built, nil
}

func (b *Builder) delegateTool(ctx context.Context, a db.Agent, run db.Run, l Lineage) (agent.Tool, bool, error) {
	if b.replay {
		// A replay hands no work on: the tool is offered as the agent would
		// hold it, as a stand-in, and a call is matched or not made.
		if run.Depth < maxDepth && (a.Delegation == "team" || a.Delegation == "listed") {
			return DelegateTool(DelegateDeps{}, a, run, l), true, nil
		}
		return agent.Tool{}, false, nil
	}
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

// A catalogue is the MCP tools a run holds: the tools, grouped by server for
// the prompt, and the names the run calls them by, by their id in the graph.
type catalogue struct {
	tools    []agent.Tool
	groups   []agent.Group
	names    map[uuid.UUID]string
	byServer map[string][]uuid.UUID
}

func (b *Builder) tools(ctx context.Context, agentID uuid.UUID, n *Narrowing, r reservedFor, gates *policy, built *Built) (catalogue, error) {
	grants, err := b.store.ListGrantedMCPServers(ctx, agentID)
	if err != nil {
		return catalogue{}, fmt.Errorf("reading the agent's MCP servers: %w", err)
	}
	// The allow-list is applied by the query.
	rows, err := b.store.ListGrantedMCPTools(ctx, agentID)
	if err != nil {
		return catalogue{}, fmt.Errorf("reading the agent's MCP tools: %w", err)
	}
	// A note an approved improvement left on a tool is told with the tool,
	// to this agent only.
	notes, err := b.store.ListAgentToolNotes(ctx, agentID)
	if err != nil {
		return catalogue{}, fmt.Errorf("reading the agent's tool notes: %w", err)
	}
	noted := map[uuid.UUID]map[string]string{}
	for _, n := range notes {
		if noted[n.McpServerID] == nil {
			noted[n.McpServerID] = map[string]string{}
		}
		noted[n.McpServerID][n.Tool] = n.Note
	}

	offered := map[uuid.UUID][]db.ListGrantedMCPToolsRow{}
	for _, r := range rows {
		if note, ok := noted[r.McpServerID][r.Name]; ok {
			r.Description = withNote(r.Description, note)
		}
		offered[r.McpServerID] = append(offered[r.McpServerID], r)
	}

	out := catalogue{names: map[uuid.UUID]string{}, byServer: map[string][]uuid.UUID{}}
	owner := map[string]string{}
	for _, g := range grants {
		// A command server is offered like a URL server: its room in the
		// team's sandbox is started by the first call to one of its tools.
		s := g.McpServer
		// One that needs signing in again keeps its tools: a call to one
		// fails saying so, and the agent can tell the person why.
		if s.AuthKind == "oauth" && g.OauthStatus == nil {
			built.Unavailable = append(built.Unavailable, Unavailable{
				Name: s.Name, Reason: "it needs signing in on the dashboard",
			})
			continue
		}
		if len(offered[s.ID]) == 0 {
			built.Unavailable = append(built.Unavailable, Unavailable{
				Name: s.Name, Reason: "no tools of it have been discovered",
			})
			continue
		}

		server := built.servers.server(s)
		group := agent.Group{Name: s.Name}
		for _, row := range offered[s.ID] {
			name := row.Name
			if !s.Bare {
				name = s.Name + "__" + row.Name
			}
			if !toolNameShape.MatchString(name) {
				built.Unavailable = append(built.Unavailable, Unavailable{
					Name:   name,
					Reason: fmt.Sprintf("the MCP server %q offers %q, which is not a usable tool name", s.Name, row.Name),
				})
				continue
			}
			if !n.keepsTool(name) {
				continue
			}
			if reserved(name, r) {
				built.Unavailable = append(built.Unavailable, Unavailable{
					Name:   name,
					Reason: fmt.Sprintf("the MCP server %q offers a tool named %s, which is the brain's own", s.Name, name),
				})
				continue
			}
			if first, taken := owner[name]; taken {
				return catalogue{}, fmt.Errorf("the MCP servers %q and %q both offer %q", first, s.Name, name)
			}
			owner[name] = s.Name
			raw, at := limited(server.tool(name, row)), target{
				name: s.Name + "__" + row.Name, server: s.Name, class: approval.ClassOf(row.Annotations, s.TrustHints),
			}
			built.answerable[at.name] = raw
			built.reads[name] = at.class == approval.Read && built.allows(at)
			t, refused := gates.gate(raw, at)
			if refused != nil {
				built.Unavailable = append(built.Unavailable, *refused)
				continue
			}
			out.tools = append(out.tools, t)
			out.names[row.ID] = name
			out.byServer[s.Name] = append(out.byServer[s.Name], row.ID)
			group.Tools = append(group.Tools, name)
		}
		if len(group.Tools) > 0 {
			out.groups = append(out.groups, group)
		}
	}
	return out, nil
}

// toolNameShape is what a provider accepts as a tool's name, as the SDK's MCP
// client checks it.
var toolNameShape = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// reservedFor is which of the brain's own tools a run holds, whose names an
// MCP server's tool may not take.
type reservedFor struct{ hub, web, browse, work bool }

func reserved(name string, r reservedFor) bool {
	switch name {
	// Every run compacts, and the SDK refuses a second read_output.
	case GraphToolName, DelegateToolName, agent.ReadOutputToolName, AttachmentsToolName, ReadAttachmentToolName:
		return true
	// The names the SDK gives the tools that find, describe and run the rest,
	// and open skills: a server's tool under one would stop the run.
	case agent.DescribeToolsName, agent.CallToolName, agent.SkillToolName, agent.SkillResourceToolName:
		return true
	case PlanToolName:
		return r.hub
	case WebSearchToolName, WebFetchToolName:
		return r.web
	}
	if slices.Contains(BrowserToolNames, name) {
		return r.browse
	}
	if slices.Contains(WorkspaceToolNames, name) {
		return r.work
	}
	return false
}

func (b *Builder) connect(ctx context.Context, s db.McpServer) (*mcp.Session, error) {
	address, client, err := b.mcp.Reach(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q: %w", s.Name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, b.connectTimeout)
	defer cancel()

	live, err := mcp.Connect(ctx, mcp.Server{Name: s.Name, URL: address, HTTPClient: client, Bare: s.Bare})
	if err != nil {
		return nil, &serverDown{name: s.Name, err: err}
	}
	return live, nil
}

// A serverDown is an MCP server a call could not open. Its message names the
// server and leaves out the address, which can carry a key: the call's error
// is read by the model and by everyone in the session.
type serverDown struct {
	name string
	err  error
}

func (s *serverDown) Error() string {
	cause := s.err
	var dial *url.Error
	if errors.As(cause, &dial) {
		// The address can carry a key, and the run's error is read by the whole session.
		cause = dial.Err
	}
	return fmt.Sprintf("the MCP server %q could not be reached: %v", s.name, cause)
}

func (s *serverDown) Unwrap() error { return s.err }

// limited is t held to toolCallTimeout, through Call, so a tool that answers
// with files as well as text keeps them.
func limited(t agent.Tool) agent.Tool {
	call := t.Call
	t.Invoke = nil
	t.Result = func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
		defer cancel()
		r, callable, err := call(ctx, args)
		if !callable {
			return agent.ToolResult{}, fmt.Errorf("%s cannot be called", t.Name)
		}
		return r, err
	}
	return t
}

// withNote is a tool's description with this agent's note on it after. The
// note is model-written, so it is one line of printable text: it cannot open
// a section of its own inside the description.
func withNote(description, note string) string {
	// The same cleaning the improvements API applies before it stores a note.
	note = strings.Join(strings.FieldsFunc(note, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	}), " ")
	if note == "" {
		return description
	}
	if description == "" {
		return "Note for this agent: " + note
	}
	return description + "\n\nNote for this agent: " + note
}

func (b *Builder) skills(ctx context.Context, teamID, agentID uuid.UUID, n *Narrowing) ([]agent.Skill, error) {
	rows, err := b.store.ListGrantedSkills(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading the agent's skills: %w", err)
	}

	out := make([]agent.Skill, 0, len(rows))
	for _, row := range rows {
		if !n.keepsSkill(row.ID) {
			continue
		}

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
