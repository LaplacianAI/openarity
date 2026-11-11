package blueprints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
)

func (w *world) export(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/teams/"+w.team.String()+"/blueprint"+query, nil)
	return w.serve(t, req)
}

func (w *world) exported(t *testing.T, query string) Blueprint {
	t.Helper()
	rec := w.export(t, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	var bp Blueprint
	if err := json.Unmarshal(rec.Body.Bytes(), &bp); err != nil {
		t.Fatalf("export: %v", err)
	}
	return bp
}

// richTeam sets every field an agent and a server can hold away from its
// default, so a field export forgets shows up as a change.
func richTeam() Blueprint {
	bp := supportTeam()
	env := map[string]string{"HELPDESK_KEY": "helpdesk#key"}
	bp.MCPServers = append(bp.MCPServers, serverSpec{Name: "local", Command: []string{"helpdesk-mcp", "--stdio"}, Env: env, Bare: true})

	temp, budget, web := 0.3, int64(50000), true
	parserTokens := int32(512)
	triage := &bp.Agents[0]
	triage.Description = "Sorts support email."
	triage.Model.Temperature = &temp
	triage.SteerContinuations = 2
	triage.TokenBudget = &budget
	triage.Web = &web
	triage.OutputSchema = &agents.OutputSchema{Name: "verdict", JSON: json.RawMessage(`{"type":"object"}`), Strict: true}
	triage.Parser = true
	triage.ParserModel = &agents.ParserModel{Name: "claude-haiku-5-5", MaxTokens: &parserTokens}
	triage.MCPServers = append(triage.MCPServers, serverRef{Name: "local"})

	team := "team"
	bp.Agents[1].Delegation = &team
	return bp
}

// The promise export makes: what it says, planned, changes nothing.
func TestAnExportedTeamPlansAsUnchanged(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, richTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}

	bp := w.exported(t, "")
	if len(bp.MCPServers) != 2 || len(bp.Agents) != 2 || bp.Skills != nil {
		t.Fatalf("export = %d servers, %d agents, %d skills; want 2, 2 and none", len(bp.MCPServers), len(bp.Agents), len(bp.Skills))
	}
	p := w.plan(t, bp)
	if len(p.Refusals) > 0 {
		t.Fatalf("refusals: %+v", p.Refusals)
	}
	for name, a := range actions(p.Changes) {
		if a != ActionUnchanged {
			t.Errorf("%s: %s, want unchanged", name, a)
		}
	}
}

func TestAnExportSaysReferencesRelativeAndGrantsByName(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, richTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}

	raw := w.export(t, "").Body.String()
	if strings.Contains(raw, "teams/") {
		t.Errorf("export names a whole secret path; it should be relative:\n%s", raw)
	}
	bp := w.exported(t, "?agent=triage")
	if len(bp.Agents) != 1 || len(bp.MCPServers) != 0 {
		t.Fatalf("export of triage = %+v, want triage alone", bp)
	}
	a := bp.Agents[0]
	if !equalNames(a.Skills, "support-faq") || !equalNames(a.Delegates, "writer") ||
		len(a.MCPServers) != 2 || a.MCPServers[0].Name != "helpdesk" || !equalNames(a.MCPServers[0].Allow, "create_ticket") {
		t.Errorf("triage's grants = %+v %v %v, want helpdesk (create_ticket), local, support-faq, writer", a.MCPServers, a.Skills, a.Delegates)
	}
}

// An agent that holds nothing is said to hold nothing, so the file reads
// whole and an edit of it does not take a grant away by leaving it out.
func TestAnAgentHoldingNothingIsExportedHoldingNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, Blueprint{Agents: []agentSpec{agentNamed("writer")}}); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	raw := w.export(t, "?agent=writer").Body.String()
	for _, want := range []string{`"mcp_servers":[]`, `"skills":[]`, `"delegates":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("export lacks %s:\n%s", want, raw)
		}
	}
}

func TestABuiltInAgentIsNeverExported(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, kind, instructions, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'recall', 'memory', '', 'm', 1, 'react', 1)`, w.team); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if bp := w.exported(t, ""); len(bp.Agents) != 0 {
		t.Errorf("export = %+v, want no agents", bp.Agents)
	}
	if rec := w.export(t, "?agent=recall"); rec.Code != http.StatusNotFound {
		t.Errorf("export of recall: %d, want 404", rec.Code)
	}
}

func TestAnExportOfANameTheTeamLacksIsNotFound(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for _, q := range []string{"?agent=nobody", "?mcp_server=nowhere"} {
		rec := w.export(t, q)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "is not") {
			t.Errorf("%s: %d %s, want 404 naming it", q, rec.Code, rec.Body)
		}
	}
}
