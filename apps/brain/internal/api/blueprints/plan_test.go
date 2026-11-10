package blueprints

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func emptyTeam() team {
	return team{
		id: uuid.New(), agents: map[string]db.Agent{}, servers: map[string]db.McpServer{},
		skills: map[string]db.Skill{}, files: map[uuid.UUID]map[string][]byte{}, grants: map[uuid.UUID]held{},
	}
}

func all() may { return may{KindMCPServer: true, KindSkill: true, KindAgent: true} }

func refusedFor(p planned, name, words string) bool {
	for _, r := range p.Refusals {
		if r.Name == name && strings.Contains(r.Message, words) {
			return true
		}
	}
	return false
}

func TestAnItemNamedTwiceIsRefused(t *testing.T) {
	t.Parallel()

	url := "https://a.example/mcp"
	bp := Blueprint{
		MCPServers: []serverSpec{{Name: "a", URL: &url}, {Name: "a", URL: &url}},
		Skills:     []skillSpec{faqSkill("x"), faqSkill("y")},
		Agents:     []agentSpec{agentNamed("triage"), agentNamed("triage")},
	}
	p := planOf(emptyTeam(), bp, loops, all())
	for _, name := range []string{"a", "support-faq", "triage"} {
		if !refusedFor(p, name, "named twice") {
			t.Errorf("%s: no refusal for being named twice in %+v", name, p.Refusals)
		}
	}
}

func TestANameThatIsNowhereIsRefused(t *testing.T) {
	t.Parallel()

	server := agentNamed("triage")
	server.MCPServers = []serverRef{{Name: "helpdesk"}}
	delegate := agentNamed("writer")
	listed := "listed"
	delegate.Delegation, delegate.Delegates = &listed, []string{"editor"}

	p := planOf(emptyTeam(), Blueprint{Agents: []agentSpec{server, delegate}}, loops, all())
	if !refusedFor(p, "triage", `"helpdesk" is not an MCP server`) {
		t.Errorf("triage: %+v", p.Refusals)
	}
	if !refusedFor(p, "writer", `"editor" is not an agent`) {
		t.Errorf("writer: %+v", p.Refusals)
	}
}

func TestAGrantNamedTwiceIsRefusedByName(t *testing.T) {
	t.Parallel()

	a := agentNamed("triage")
	a.Skills = []string{"faq", "faq"}
	p := planOf(emptyTeam(), Blueprint{Agents: []agentSpec{a}}, loops, all())
	if !refusedFor(p, "triage", "skills names faq twice") {
		t.Errorf("refusals = %+v", p.Refusals)
	}
}

func TestAKindOtherThanCustomIsRefused(t *testing.T) {
	t.Parallel()

	a := agentNamed("triage")
	a.Kind = "memory"
	p := planOf(emptyTeam(), Blueprint{Agents: []agentSpec{a}}, loops, all())
	if !refusedFor(p, "triage", "kind must be custom") {
		t.Errorf("refusals = %+v", p.Refusals)
	}
}

func TestASkillWithNoFilesOrNoManifestIsRefusedByItsPlace(t *testing.T) {
	t.Parallel()

	noManifest := skillSpec{Files: []skillFile{{Path: "faq.md", Data: []byte("x")}}}
	p := planOf(emptyTeam(), Blueprint{Skills: []skillSpec{{}, noManifest}}, loops, all())
	if !refusedFor(p, "#1", "needs its files") || len(p.Refusals) != 2 || p.Refusals[1].Name != "#2" {
		t.Errorf("refusals = %+v, want one each for #1 and #2", p.Refusals)
	}
}

func TestAnEnvValueThatIsNotAReferenceIsRefused(t *testing.T) {
	t.Parallel()

	s := serverSpec{Name: "gh", Command: []string{"gh-mcp"}, Env: map[string]string{"TOKEN": "ghp_abc"}}
	p := planOf(emptyTeam(), Blueprint{MCPServers: []serverSpec{s}}, loops, all())
	if !refusedFor(p, "gh", "env TOKEN is a secret's name and key") {
		t.Errorf("refusals = %+v", p.Refusals)
	}
}

// The server API would refuse the expanded value too, but in terms of a path
// the person never wrote.
func TestASecretInsteadOfAReferenceIsSaidInTheBlueprintsTerms(t *testing.T) {
	t.Parallel()

	url, secret := "https://h.example/mcp", "sk-live-123"
	p := planOf(emptyTeam(), Blueprint{MCPServers: []serverSpec{{Name: "helpdesk", URL: &url, AuthSecret: &secret}}}, loops, all())
	if !refusedFor(p, "helpdesk", "auth_secret is a secret's name and key") {
		t.Errorf("refusals = %+v", p.Refusals)
	}
}

// The MCP server API and its form take a reference whole; written that way
// in a blueprint it is the same secret, never one nested under itself.
func TestAWholeReferenceIsTakenAsTheTeamsOwn(t *testing.T) {
	t.Parallel()

	tm := emptyTeam()
	root := "teams/" + tm.id.String() + "/mcp/"
	url := "https://h.example/mcp"
	whole, short := root+"helpdesk#token", "helpdesk#token"
	for _, ref := range []string{whole, short} {
		p := planOf(tm, Blueprint{MCPServers: []serverSpec{{Name: "helpdesk", URL: &url, AuthSecret: &ref}}}, loops, all())
		if len(p.Refusals) != 0 || len(p.servers) != 1 || *p.servers[0].fields.AuthSecretRef != whole {
			t.Errorf("%s: refusals %+v, stored %v; want %s", ref, p.Refusals, p.servers, whole)
		}
	}

	env := planOf(tm, Blueprint{MCPServers: []serverSpec{{
		Name: "gh", Command: []string{"gh-mcp"}, Env: map[string]string{"A": whole, "B": short},
	}}}, loops, all())
	if len(env.Refusals) != 0 || string(env.servers[0].fields.Env) != `{"A":"`+whole+`","B":"`+whole+`"}` {
		t.Errorf("env: refusals %+v, stored %s", env.Refusals, env.servers[0].fields.Env)
	}
}

func TestAnotherTeamsReferenceIsRefused(t *testing.T) {
	t.Parallel()

	url := "https://h.example/mcp"
	theirs := "teams/" + uuid.NewString() + "/mcp/helpdesk#token"
	p := planOf(emptyTeam(), Blueprint{MCPServers: []serverSpec{{Name: "helpdesk", URL: &url, AuthSecret: &theirs}}}, loops, all())
	if !refusedFor(p, "helpdesk", "another team's secret") {
		t.Errorf("refusals = %+v", p.Refusals)
	}
}

func TestAServerIsComparedFieldByField(t *testing.T) {
	t.Parallel()

	tm := emptyTeam()
	old := "https://old.example/mcp"
	tm.servers["helpdesk"] = db.McpServer{ID: uuid.New(), Name: "helpdesk", Url: &old, Env: []byte(`{}`)}

	url := "https://new.example/mcp"
	p := planOf(tm, Blueprint{MCPServers: []serverSpec{{Name: "helpdesk", URL: &url, Bare: true}}}, loops, all())
	c := p.Changes[0]
	if c.Action != ActionUpdate || !equalNames(c.Fields, "url", "bare") || len(p.servers) != 1 {
		t.Errorf("change = %+v, steps %d; want an update of url and bare", c, len(p.servers))
	}
}

func TestAChangedAllowIsAChangeOfMCPServers(t *testing.T) {
	t.Parallel()

	tm := emptyTeam()
	url := "https://h.example/mcp"
	server := db.McpServer{ID: uuid.New(), Name: "helpdesk", Url: &url, Env: []byte(`{}`)}
	tm.servers["helpdesk"] = server

	a := agentNamed("triage")
	a.MCPServers = []serverRef{{Name: "helpdesk"}}
	p := planOf(tm, Blueprint{Agents: []agentSpec{a}}, loops, all())
	row := db.Agent{ID: uuid.New(), Name: "triage", Kind: "custom"}
	params := p.agents[0].params
	row.Instructions, row.ModelName, row.MaxTokens, row.Pattern, row.MaxSteps, row.Delegation = params.Instructions, params.ModelName, params.MaxTokens, params.Pattern, params.MaxSteps, params.Delegation
	tm.agents["triage"] = row
	tm.grants[row.ID] = held{servers: map[string][]string{"helpdesk": {"create_ticket"}}}

	p = planOf(tm, Blueprint{Agents: []agentSpec{a}}, loops, all())
	if c := p.Changes[0]; c.Action != ActionUpdate || !equalNames(c.Fields, "mcp_servers") {
		t.Errorf("change = %+v; every tool where one was allowed is a change", c)
	}
}

// A permission is said once per kind, not once per item.
func TestAMissingPermissionIsOneRefusal(t *testing.T) {
	t.Parallel()

	bp := Blueprint{Agents: []agentSpec{agentNamed("a"), agentNamed("b")}}
	p := planOf(emptyTeam(), bp, loops, may{})
	if len(p.Refusals) != 1 || !strings.Contains(p.Refusals[0].Message, "agent:write") {
		t.Errorf("refusals = %+v, want one naming agent:write", p.Refusals)
	}
}

// Nothing to write needs no permission: a plan of unchanged items is fine
// for someone who can only read.
func TestAnUnchangedItemNeedsNoPermission(t *testing.T) {
	t.Parallel()

	tm := emptyTeam()
	url := "https://h.example/mcp"
	tm.servers["helpdesk"] = db.McpServer{ID: uuid.New(), Name: "helpdesk", Url: &url, Env: []byte(`{}`)}
	p := planOf(tm, Blueprint{MCPServers: []serverSpec{{Name: "helpdesk", URL: &url}}}, loops, may{})
	if len(p.Refusals) != 0 || p.Changes[0].Action != ActionUnchanged {
		t.Errorf("plan = %+v", p.Plan)
	}
}

func TestTheDigestFollowsThePlan(t *testing.T) {
	t.Parallel()

	tm := emptyTeam()
	a := planOf(tm, Blueprint{Agents: []agentSpec{agentNamed("a")}}, loops, all())
	again := planOf(tm, Blueprint{Agents: []agentSpec{agentNamed("a")}}, loops, all())
	other := planOf(tm, Blueprint{Agents: []agentSpec{agentNamed("b")}}, loops, all())
	if a.Digest != again.Digest || a.Digest == other.Digest {
		t.Errorf("digests %s %s %s: the same plan must name itself the same, and another differently", a.Digest, again.Digest, other.Digest)
	}
}

func TestJSONIsComparedByValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`{"a": 1, "b": 2}`, `{"b":2,"a":1}`, true},
		{`{"a": 1}`, `{"a": 2}`, false},
		{``, ``, true},
		{``, `{}`, false},
		{`not json`, `not json`, true},
	} {
		if got := jsonEqual([]byte(tc.a), []byte(tc.b)); got != tc.want {
			t.Errorf("jsonEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
