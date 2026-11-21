package improve

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A prune candidate is a skill or a tool, named as a proposal names it.
func skillCandidate(name string) string { return "skill:" + name }

func toolCandidate(server, tool string) string { return "tool:" + server + "/" + tool }

// pruneFor is an agent's prune candidates not looked at lately: what it
// held in each of its last runs and never used.
func (im *Improver) pruneFor(ctx context.Context, agentID uuid.UUID) ([]string, error) {
	runs, err := im.store.ListAgentRecentRuns(ctx, db.ListAgentRecentRunsParams{AgentID: &agentID, PageSize: pruneWindow})
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's runs: %w", agentID, err)
	}
	if len(runs) < pruneWindow {
		return nil, nil
	}
	use, err := im.store.ListRunsUse(ctx, db.ListRunsUseParams{RunIds: runs, NamingTools: []string{skillTool, callTool}})
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's use of its tools: %w", agentID, err)
	}
	m, err := im.agentState(ctx, agentID)
	if err != nil {
		return nil, err
	}
	var skills []string
	for _, id := range m.state.Agent.Skills {
		skills = append(skills, m.skillName(id))
	}
	var tools []grantedTool
	for _, g := range m.state.Agent.MCPServers {
		s, ok := m.servers[m.serverName(g.ID)]
		if !ok {
			continue
		}
		allowed := g.Allow
		if allowed == nil {
			allowed = s.Tools
		}
		for _, t := range allowed {
			run := s.Name + "__" + t
			if s.Bare {
				run = t
			}
			tools = append(tools, grantedTool{runName: run, server: s.Name, tool: t})
		}
	}
	looked, err := im.store.ListRecentPruneLooks(ctx, db.ListRecentPruneLooksParams{AgentID: agentID, Since: im.now().Add(-pruneQuiet)})
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's recent prunes: %w", agentID, err)
	}
	return slices.DeleteFunc(pruneCandidates(runs, use, skills, tools), func(c string) bool {
		return slices.Contains(looked, c)
	}), nil
}

// grantedTool is one tool an agent may call, by the name its runs know it
// by and the name a proposal gives it.
type grantedTool struct{ runName, server, tool string }

// pruneCandidates are the skills and tools an agent held in every one of
// its last pruneWindow runs and never used: a skill no run opened, a tool
// no run called, directly or through call_tool. Fewer runs than the window
// say nothing.
func pruneCandidates(runs []uuid.UUID, use []db.ListRunsUseRow, skills []string, tools []grantedTool) []string {
	if len(runs) < pruneWindow {
		return nil
	}
	heldSkills, heldTools := map[string]int{}, map[string]int{}
	opened, called := map[string]bool{}, map[string]bool{}
	for _, u := range use {
		switch u.Kind {
		case "started":
			for _, s := range unique(u.Skills) {
				heldSkills[s]++
			}
			for _, t := range unique(u.Tools) {
				heldTools[t]++
			}
		case "tool_call":
			called[u.Name] = true
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(u.Arguments), &args) // arguments that are not JSON name nothing
			switch u.Name {
			case skillTool:
				opened[args.Name] = true
			case callTool:
				called[args.Name] = true
			}
		}
	}

	var out []string
	for _, s := range skills {
		if heldSkills[s] == len(runs) && !opened[s] {
			out = append(out, skillCandidate(s))
		}
	}
	for _, t := range tools {
		if heldTools[t.runName] == len(runs) && !called[t.runName] {
			out = append(out, toolCandidate(t.server, t.tool))
		}
	}
	slices.Sort(out)
	return out
}

// pruneAnswers holds a proposal on a prune candidate to that candidate: the
// skill dropped, or the one tool removed, and nothing else.
func pruneAnswers(candidate, kind string, edit json.RawMessage) error {
	var e struct {
		Skill  string   `json:"skill"`
		Server string   `json:"server"`
		Remove []string `json:"remove"`
	}
	_ = json.Unmarshal(edit, &e) // an edit not in the kind's shape is refused when it is built
	switch {
	case kind == "skill_drop" && candidate == skillCandidate(strings.TrimSpace(e.Skill)):
		return nil
	case kind == "tools_narrow" && len(e.Remove) == 1 && candidate == toolCandidate(strings.TrimSpace(e.Server), e.Remove[0]):
		return nil
	}
	return refused("prune candidate %s is answered by dropping that skill (skill_drop) or removing that one tool (tools_narrow), and nothing else", candidate)
}
