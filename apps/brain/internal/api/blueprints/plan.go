package blueprints

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/mcpservers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/skills"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// team is what a team holds, keyed the way a blueprint names it.
type team struct {
	id       uuid.UUID
	agents   map[string]db.Agent
	servers  map[string]db.McpServer
	skills   map[string]db.Skill
	files    map[uuid.UUID]map[string][]byte // a skill's files: path to SHA-256
	grants   map[uuid.UUID]held              // an agent's, by its id
	declared map[Kind]map[string]bool        // what the blueprint names, refused or not
}

// held is what an agent may reach, by name. A nil allow is every tool.
type held struct {
	servers   map[string][]string
	skills    []string
	delegates []string
}

// may is which of the three writes the caller holds.
type may map[Kind]bool

var permissionOf = map[Kind]string{
	KindMCPServer: "tool:write",
	KindSkill:     "skill:write",
	KindAgent:     "agent:write",
}

var nounOf = map[Kind]string{KindMCPServer: "an MCP server", KindSkill: "a skill", KindAgent: "an agent"}

// planned is a plan and the writes that applying it makes, in order.
type planned struct {
	Plan
	team    team // what it was planned against
	servers []serverStep
	skills  []skillStep
	agents  []agentStep
	refused map[Kind]bool  // a missing permission, said once per kind
	written []db.McpServer // the servers an apply wrote, as written
}

type serverStep struct {
	change int
	id     uuid.UUID
	fields mcpservers.Fields
}

type skillStep struct {
	change int
	id     uuid.UUID
	dir    skill.Directory
}

type agentStep struct {
	change int
	id     uuid.UUID
	params db.CreateAgentParams
	spec   agentSpec
}

func planOf(t team, bp Blueprint, loops []string, m may) planned {
	p := planned{Plan: Plan{Changes: []Change{}, Refusals: []Refusal{}}, refused: map[Kind]bool{}}
	t.declared = declared(bp)
	p.team = t

	p.planServers(t, bp.MCPServers, m)
	p.planSkills(t, bp.Skills, m)
	p.planAgents(t, bp.Agents, loops, m)

	p.Digest = digest(p.Changes, p.Refusals)
	return p
}

func (p *planned) refuse(k Kind, name, msg string) {
	p.Refusals = append(p.Refusals, Refusal{Kind: k, Name: name, Message: msg})
}

// note records a change and returns its index. A create or update the caller
// may not make is refused, once for each kind.
func (p *planned) note(k Kind, name string, fields []string, id *uuid.UUID, m may) int {
	action := ActionUnchanged
	switch {
	case id == nil:
		action = ActionCreate
	case len(fields) > 0:
		action = ActionUpdate
	}

	if action != ActionUnchanged && !m[k] && !p.refused[k] {
		p.refused[k] = true
		p.refuse(k, name, fmt.Sprintf("you need %s to create or change %s", permissionOf[k], nounOf[k]))
	}

	p.Changes = append(p.Changes, Change{Kind: k, Name: name, Action: action, Fields: fields, ID: id})
	return len(p.Changes) - 1
}

func (p *planned) planServers(t team, specs []serverSpec, m may) {
	seen := map[string]bool{}
	for _, s := range specs {
		if seen[s.Name] {
			p.refuse(KindMCPServer, s.Name, "named twice")
			continue
		}
		seen[s.Name] = true

		f, err := serverFields(t.id, s)
		if err != nil {
			p.refuse(KindMCPServer, s.Name, err.Error())
			continue
		}

		cur, exists := t.servers[s.Name]
		if !exists {
			p.servers = append(p.servers, serverStep{change: p.note(KindMCPServer, s.Name, nil, nil, m), fields: f})
			continue
		}
		fields := serverDiff(cur, f)
		i := p.note(KindMCPServer, s.Name, fields, &cur.ID, m)
		if len(fields) > 0 {
			p.servers = append(p.servers, serverStep{change: i, id: cur.ID, fields: f})
		}
	}
}

// serverFields is the server as the MCP server API would store it, each
// relative reference placed under the team's own secrets.
func serverFields(teamID uuid.UUID, s serverSpec) (mcpservers.Fields, error) {
	root := secrets.TeamPath(teamID, secrets.KindMCP) + "/"
	req := mcpservers.Request{Name: s.Name, URL: s.URL, Command: s.Command, Bare: s.Bare}

	if s.AuthSecret != nil {
		ref, err := teamRef(root, "auth_secret", *s.AuthSecret)
		if err != nil {
			return mcpservers.Fields{}, err
		}
		req.AuthSecretRef = &ref
	}

	if s.Env != nil {
		req.Env = make(map[string]string, len(s.Env))
		for _, k := range slices.Sorted(maps.Keys(s.Env)) {
			ref, err := teamRef(root, "env "+k, s.Env[k])
			if err != nil {
				return mcpservers.Fields{}, err
			}
			req.Env[k] = ref
		}
	}

	return mcpservers.Parse(teamID, req)
}

// teamRef places a reference among the team's MCP secrets. It may be written
// relative (name#key) or whole, as the MCP server API and its form take it;
// either way it is the team's own. A whole one under another team is refused
// rather than nested under this one.
func teamRef(root, field, ref string) (string, error) {
	rel, whole := strings.CutPrefix(ref, root)
	if !whole && strings.HasPrefix(ref, "teams/") {
		return "", fmt.Errorf("%s names another team's secret: a blueprint reaches only its own team's", field)
	}
	if !mcpservers.RefPattern.MatchString(rel) {
		return "", fmt.Errorf("%s is a secret's name and key, name#key, among the team's MCP secrets", field)
	}
	return root + rel, nil
}

func serverDiff(cur db.McpServer, f mcpservers.Fields) []string {
	var fields []string
	add := func(changed bool, name string) {
		if changed {
			fields = append(fields, name)
		}
	}
	add(!equalPtr(cur.Url, f.URL), "url")
	add(!slices.Equal(cur.Command, f.Command), "command")
	add(!jsonEqual(cur.Env, f.Env), "env")
	add(!equalPtr(cur.AuthSecretRef, f.AuthSecretRef), "auth_secret")
	add(cur.Bare != f.Bare, "bare")
	return fields
}

func (p *planned) planSkills(t team, specs []skillSpec, m may) {
	seen := map[string]bool{}
	for i, s := range specs {
		dir, err := assemble(s)
		if err != nil {
			p.refuse(KindSkill, fmt.Sprintf("#%d", i+1), err.Error())
			continue
		}

		name := dir.Manifest.Name
		if seen[name] {
			p.refuse(KindSkill, name, "named twice")
			continue
		}
		seen[name] = true

		cur, exists := t.skills[name]
		if !exists {
			p.skills = append(p.skills, skillStep{change: p.note(KindSkill, name, nil, nil, m), dir: dir})
			continue
		}
		fields := skillDiff(cur, t.files[cur.ID], dir)
		c := p.note(KindSkill, name, fields, &cur.ID, m)
		if len(fields) > 0 {
			p.skills = append(p.skills, skillStep{change: c, id: cur.ID, dir: dir})
		}
	}
}

func assemble(s skillSpec) (skill.Directory, error) {
	if len(s.Files) == 0 {
		return skill.Directory{}, errors.New("a skill needs its files, SKILL.md among them")
	}
	entries := make([]skill.Entry, len(s.Files))
	for i, f := range s.Files {
		entries[i] = skill.Entry{Path: f.Path, Data: f.Data}
	}
	return skill.Assemble(entries)
}

// skillDiff compares what SKILL.md says, which is kept on the skill's row,
// and the other files, by hash. A skill imported from elsewhere becomes an
// upload once a blueprint writes it.
func skillDiff(cur db.Skill, files map[string][]byte, dir skill.Directory) []string {
	m := dir.Manifest
	var fields []string
	add := func(changed bool, name string) {
		if changed {
			fields = append(fields, name)
		}
	}
	add(cur.Description != m.Description, "description")
	add(!equalPtr(cur.License, m.License), "license")
	add(!equalPtr(cur.Compatibility, m.Compatibility), "compatibility")
	add(!jsonEqual(cur.Metadata, skills.Metadata(m)), "metadata")
	add(!equalPtr(cur.AllowedTools, m.AllowedTools), "allowed_tools")
	add(cur.Body != m.Body, "body")
	add(!sameFiles(files, dir.Files), "files")
	add(cur.Source != uploaded.Source, "source")
	return fields
}

func sameFiles(stored map[string][]byte, files []skill.File) bool {
	if len(stored) != len(files) {
		return false
	}
	for _, f := range files {
		if !bytes.Equal(stored[f.Path], f.SHA256[:]) {
			return false
		}
	}
	return true
}

func (p *planned) planAgents(t team, specs []agentSpec, loops []string, m may) {
	seen := map[string]bool{}
	for _, a := range specs {
		if seen[a.Name] {
			p.refuse(KindAgent, a.Name, "named twice")
			continue
		}
		seen[a.Name] = true

		cur, exists := t.agents[a.Name]
		if exists && cur.Kind != string(agents.KindCustom) {
			p.refuse(KindAgent, a.Name, "is an agent the brain provides; a blueprint describes custom agents")
			continue
		}

		params, err := t.agentColumns(a, loops)
		if err != nil {
			p.refuse(KindAgent, a.Name, err.Error())
			continue
		}

		if !exists {
			p.agents = append(p.agents, agentStep{change: p.note(KindAgent, a.Name, nil, nil, m), params: params, spec: a})
			continue
		}
		fields := append(agentDiff(cur, params), grantDiff(t.grants[cur.ID], a)...)
		i := p.note(KindAgent, a.Name, fields, &cur.ID, m)
		if len(fields) > 0 {
			p.agents = append(p.agents, agentStep{change: i, id: cur.ID, params: params, spec: a})
		}
	}
}

// agentColumns checks an agent the way the agents API does, with a stand-in
// id for each name the blueprint is yet to create.
func (t team) agentColumns(a agentSpec, loops []string) (db.CreateAgentParams, error) {
	if a.Kind != "" && a.Kind != agents.KindCustom {
		return db.CreateAgentParams{}, errors.New("kind must be custom: the other kinds are agents the brain provides")
	}
	if slices.Contains(a.Delegates, a.Name) {
		return db.CreateAgentParams{}, errors.New("an agent cannot list itself")
	}
	if err := twice(a); err != nil {
		return db.CreateAgentParams{}, err
	}

	req, err := resolve(a, t.standIn)
	if err != nil {
		return db.CreateAgentParams{}, err
	}
	if err := agents.Validate(req, loops); err != nil {
		return db.CreateAgentParams{}, err
	}

	params, err := agents.Columns(req)
	if err != nil {
		return db.CreateAgentParams{}, err
	}
	params.TeamID, params.Kind = t.id, string(agents.KindCustom)
	return params, nil
}

// twice refuses a name granted twice here, where it is still a name: the
// agents API would say so by id.
func twice(a agentSpec) error {
	servers := make([]string, len(a.MCPServers))
	for i, s := range a.MCPServers {
		servers[i] = s.Name
	}
	fields := []struct {
		name  string
		names []string
	}{{"mcp_servers", servers}, {"skills", a.Skills}, {"delegates", a.Delegates}}

	for _, f := range fields {
		sorted := slices.Sorted(slices.Values(f.names))
		for i := 1; i < len(sorted); i++ {
			if sorted[i] == sorted[i-1] {
				return fmt.Errorf("%s names %s twice", f.name, sorted[i])
			}
		}
	}
	return nil
}

// existing is the id of what the team already holds under that name.
func (t team) existing(k Kind, name string) (uuid.UUID, bool) {
	switch k {
	case KindMCPServer:
		s, ok := t.servers[name]
		return s.ID, ok
	case KindSkill:
		s, ok := t.skills[name]
		return s.ID, ok
	case KindAgent:
		a, ok := t.agents[name]
		return a.ID, ok
	default:
		return uuid.Nil, false
	}
}

// standIn is the id a name has, or will have once the blueprint creates it.
func (t team) standIn(k Kind, name string) (uuid.UUID, bool) {
	if id, ok := t.existing(k, name); ok {
		return id, true
	}
	if t.declared[k][name] {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(k)+"/"+name)), true
	}
	return uuid.Nil, false
}

// resolve turns an agent's names into the request the agents API takes.
func resolve(a agentSpec, idOf func(Kind, string) (uuid.UUID, bool)) (agents.Request, error) {
	req := a.Request
	req.Kind = agents.KindCustom
	req.MCPServers, req.Skills, req.Delegates = nil, nil, nil

	for _, s := range a.MCPServers {
		id, ok := idOf(KindMCPServer, s.Name)
		if !ok {
			return agents.Request{}, fmt.Errorf("mcp_servers: %q is not an MCP server in this team or this blueprint", s.Name)
		}
		req.MCPServers = append(req.MCPServers, agents.ServerGrant{ID: id, Allow: s.Allow})
	}
	for _, name := range a.Skills {
		id, ok := idOf(KindSkill, name)
		if !ok {
			return agents.Request{}, fmt.Errorf("skills: %q is not a skill in this team or this blueprint", name)
		}
		req.Skills = append(req.Skills, id)
	}
	for _, name := range a.Delegates {
		id, ok := idOf(KindAgent, name)
		if !ok {
			return agents.Request{}, fmt.Errorf("delegates: %q is not an agent in this team or this blueprint", name)
		}
		req.Delegates = append(req.Delegates, id)
	}
	return req, nil
}

func agentDiff(cur db.Agent, p db.CreateAgentParams) []string {
	var fields []string
	add := func(changed bool, name string) {
		if changed {
			fields = append(fields, name)
		}
	}
	add(cur.Description != p.Description, "description")
	add(cur.Instructions != p.Instructions, "instructions")
	add(cur.ModelName != p.ModelName || cur.MaxTokens != p.MaxTokens || !equalPtr(cur.Temperature, p.Temperature), "model")
	add(cur.Pattern != p.Pattern, "pattern")
	add(cur.MaxSteps != p.MaxSteps, "max_steps")
	add(cur.SteerContinuations != p.SteerContinuations, "steer_continuations")
	add(!jsonEqual(cur.OutputSchema, p.OutputSchema), "output_schema")
	add(cur.Parser != p.Parser, "parser")
	add(!equalPtr(cur.ParserModelName, p.ParserModelName) || !equalPtr(cur.ParserMaxTokens, p.ParserMaxTokens) ||
		!equalPtr(cur.ParserTemperature, p.ParserTemperature), "parser_model")
	add(!equalPtr(cur.TokenBudget, p.TokenBudget), "token_budget")
	add(cur.Delegation != p.Delegation, "delegation")
	add(cur.Web != p.Web, "web")
	return fields
}

func grantDiff(h held, a agentSpec) []string {
	var fields []string

	servers := make(map[string][]string, len(a.MCPServers))
	for _, s := range a.MCPServers {
		servers[s.Name] = s.Allow
	}
	if !maps.EqualFunc(h.servers, servers, sameSet) {
		fields = append(fields, "mcp_servers")
	}
	if !sameSet(h.skills, a.Skills) {
		fields = append(fields, "skills")
	}
	if !sameSet(h.delegates, a.Delegates) {
		fields = append(fields, "delegates")
	}
	return fields
}

// sameSet compares names in any order. A nil allow (every tool) never meets
// an empty one, which the agents API refuses, so length tells them apart.
func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func declared(bp Blueprint) map[Kind]map[string]bool {
	d := map[Kind]map[string]bool{KindMCPServer: {}, KindSkill: {}, KindAgent: {}}
	for _, s := range bp.MCPServers {
		d[KindMCPServer][s.Name] = true
	}
	for _, s := range bp.Skills {
		if dir, err := assemble(s); err == nil {
			d[KindSkill][dir.Manifest.Name] = true
		}
	}
	for _, a := range bp.Agents {
		d[KindAgent][a.Name] = true
	}
	return d
}

func equalPtr[T comparable](a, b *T) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// jsonEqual compares two JSON documents by value: Postgres does not keep
// jsonb's spacing or key order.
func jsonEqual(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

// digest names a plan, so apply can tell it is still the one that was shown.
func digest(changes []Change, refusals []Refusal) string {
	b, _ := json.Marshal(struct {
		Changes  []Change  `json:"changes"`
		Refusals []Refusal `json:"refusals"`
	}{changes, refusals})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
