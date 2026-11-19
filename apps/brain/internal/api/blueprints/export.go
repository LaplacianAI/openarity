package blueprints

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// export is the team as a blueprint: what an edit starts from, so an agent
// changed in one field is still described whole. Planning it says nothing
// would change. Skills are left out, since a skill is its files and one a
// blueprint does not name is left alone; agents still grant them by name.
func (h *handler) export(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	t, err := load(r.Context(), h.store, teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the team", err)
		return
	}

	q := r.URL.Query()
	bp, err := exportOf(t, q["agent"], q["mcp_server"])
	var missing notFound
	switch {
	case errors.As(err, &missing):
		http.Error(w, missing.Error(), http.StatusNotFound)
		return
	case err != nil:
		api.Fail(w, h.logger, u, "failed to export the team", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, bp)
}

// notFound is a name asked for that the team does not hold as a custom item.
type notFound struct{ kind, name string }

func (e notFound) Error() string {
	return fmt.Sprintf("%q is not %s in this team that a blueprint describes", e.name, nounOf[Kind(e.kind)])
}

// exportOf is what the team holds, or only the agents and servers named when
// any are. A built-in agent is never in it: a blueprint describes custom ones.
func exportOf(t team, agentNames, serverNames []string) (Blueprint, error) {
	all := len(agentNames) == 0 && len(serverNames) == 0
	bp := Blueprint{}

	servers := serverNames
	if all {
		servers = slices.Sorted(maps.Keys(t.servers))
	}
	for _, name := range servers {
		row, ok := t.servers[name]
		if !ok {
			return Blueprint{}, notFound{string(KindMCPServer), name}
		}
		s, err := serverOf(t, row)
		if err != nil {
			return Blueprint{}, err
		}
		bp.MCPServers = append(bp.MCPServers, s)
	}

	names := agentNames
	if all {
		names = slices.Sorted(maps.Keys(t.agents))
	}
	for _, name := range names {
		row, ok := t.agents[name]
		if !ok || row.Kind != string(agents.KindCustom) {
			if all {
				continue
			}
			return Blueprint{}, notFound{string(KindAgent), name}
		}
		a, err := agentOf(t, row)
		if err != nil {
			return Blueprint{}, err
		}
		bp.Agents = append(bp.Agents, a)
	}
	bp.Approvals = rulesOf(t, all, servers)
	return bp, nil
}

// rulesOf is the team's approval rules a blueprint can carry: those that ask
// or deny, every one in a whole export, and in a partial one those for the
// servers exported and their tools. An allow is an admin's own, kept in the
// team's settings, and never in a file.
func rulesOf(t team, all bool, servers []string) *approvalsSpec {
	var out []ruleSpec
	for _, name := range slices.Sorted(maps.Keys(t.rules)) {
		r := t.rules[name]
		if r.Mode == "allow" || !all && !forServers(r, servers) {
			continue
		}
		spec := ruleSpec{Mode: r.Mode, RequireOther: r.RequireOther}
		if r.TargetKind == "server" {
			spec.Server = r.Target
		} else {
			spec.Tool = r.Target
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil
	}
	return &approvalsSpec{Rules: out}
}

func forServers(r db.ApprovalRule, servers []string) bool {
	for _, s := range servers {
		if strings.EqualFold(r.Target, s) || r.TargetKind == "tool" && strings.HasPrefix(strings.ToLower(r.Target), strings.ToLower(s)+"__") {
			return true
		}
	}
	return false
}

// serverOf says a server's references relative to the team, as a blueprint
// writes them, so the file serves another team too.
func serverOf(t team, row db.McpServer) (serverSpec, error) {
	root := secrets.TeamPath(t.id, secrets.KindMCP) + "/"
	s := serverSpec{Name: row.Name, URL: row.Url, Command: row.Command, Bare: row.Bare}

	if row.AuthSecretRef != nil {
		rel := strings.TrimPrefix(*row.AuthSecretRef, root)
		s.AuthSecret = &rel
	}
	if row.Env != nil {
		if err := json.Unmarshal(row.Env, &s.Env); err != nil {
			return serverSpec{}, err
		}
		for k, ref := range s.Env {
			s.Env[k] = strings.TrimPrefix(ref, root)
		}
	}
	if err := json.Unmarshal(orEmpty(row.Vars, "{}"), &s.Vars); err != nil {
		return serverSpec{}, err
	}
	if err := json.Unmarshal(orEmpty(row.Egress, "[]"), &s.Egress); err != nil {
		return serverSpec{}, err
	}
	return s, nil
}

func agentOf(t team, row db.Agent) (agentSpec, error) {
	req, err := agents.RequestOf(row)
	if err != nil {
		return agentSpec{}, err
	}
	// Empty rather than absent: the agent is described whole, holding none.
	a := agentSpec{Request: req, MCPServers: []serverRef{}, Skills: []string{}, Delegates: []string{}}

	g := t.grants[row.ID]
	for _, name := range slices.Sorted(maps.Keys(g.servers)) {
		a.MCPServers = append(a.MCPServers, serverRef{Name: name, Allow: g.servers[name]})
	}
	a.Skills = append(a.Skills, slices.Sorted(slices.Values(g.skills))...)
	a.Delegates = append(a.Delegates, slices.Sorted(slices.Values(g.delegates))...)
	return a, nil
}
