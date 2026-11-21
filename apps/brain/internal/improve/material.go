package improve

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// material is what a pass reads: the message it starts from, and what its
// tools may reach.
type material struct {
	text   string
	agent  db.Agent
	state  improvements.AgentState
	skills map[string]db.Skill // the team's, by name
	// The team's servers by name, and every name by id.
	servers    map[string]db.ListTeamServersWithToolsRow
	skillNames map[uuid.UUID]string
	serverOf   map[uuid.UUID]string
	agents     map[string]uuid.UUID // its delegates, by name
	agentNames map[uuid.UUID]string
	runs       map[uuid.UUID]bool // the runs it may read in full
	keys       []string
	prune      []string
	// Every run whose text reached the pass, read in full or as a summary,
	// and whether someone outside the team spoke in any of them.
	drewOn   []uuid.UUID
	outsider bool
}

// agentState is an agent as it is now, with the names its setup is shown in.
func (im *Improver) agentState(ctx context.Context, agentID uuid.UUID) (material, error) {
	var m material
	var err error
	if m.agent, err = im.store.GetAgent(ctx, agentID); err != nil {
		return m, fmt.Errorf("reading agent %s: %w", agentID, err)
	}
	if err := im.store.InTx(ctx, func(q *db.Queries) error {
		m.state, err = improvements.AgentNow(ctx, q, m.agent)
		return err
	}); err != nil {
		return m, fmt.Errorf("reading agent %s's setup: %w", agentID, err)
	}
	return m, im.lookups(ctx, &m)
}

func (im *Improver) material(ctx context.Context, pass db.ImproverPass) (material, error) {
	m, err := im.agentState(ctx, pass.AgentID)
	if err != nil {
		return m, err
	}
	m.runs, m.keys, m.prune = map[uuid.UUID]bool{}, pass.Signatures, pass.Pruned
	seen := map[uuid.UUID]bool{}
	drew := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			m.drewOn = append(m.drewOn, id)
		}
	}
	for _, id := range pass.RunIds {
		m.runs[id] = true
		drew(id)
	}

	var b strings.Builder
	write := func(title string, lines ...string) {
		writeSection(&b, section{title, lines})
	}
	write("Agent", m.agent.Name)
	write("Its description", m.state.Agent.Description)
	write("Its instructions", middle(m.state.Agent.Instructions, instructionsChars))
	write("Its setup", m.setup()...)

	for _, s := range pass.Signatures {
		k, ok := parseKey(s)
		if !ok {
			continue
		}
		rows, err := im.store.ListPatternDigests(ctx, db.ListPatternDigestsParams{
			AgentID: pass.AgentID, Part: k.Part, Target: k.Target, Cause: k.Cause, PageSize: digestsPerKey,
		})
		if err != nil {
			return m, fmt.Errorf("reading agent %s's pattern %s: %w", pass.AgentID, s, err)
		}
		lines := []string{fmt.Sprintf("%d runs went wrong with this signature.", len(rows))}
		for i, r := range rows {
			drew(r.RunID)
			lines = append(lines, "behaviour: "+r.Behaviour)
			if i < summariesPerKey {
				lines = append(lines, "summary: "+r.Summary)
			}
		}
		write("Pattern "+s, lines...)
	}

	part, target := leadPart(pass.Signatures)
	well, err := im.store.ListWentWell(ctx, db.ListWentWellParams{AgentID: pass.AgentID, Part: part, Target: target, PageSize: wentWellShown})
	if err != nil {
		return m, fmt.Errorf("reading agent %s's successes: %w", pass.AgentID, err)
	}
	var wl []string
	for _, w := range well {
		drew(w.RunID)
		wl = append(wl, fmt.Sprintf("%s:%s: %s", w.Part, w.Target, w.Summary))
	}
	write("What went well", wl...)

	if len(pass.Signatures) == 0 {
		wrong, err := im.store.ListWentWrong(ctx, db.ListWentWrongParams{AgentID: pass.AgentID, PageSize: wentWellShown})
		if err != nil {
			return m, fmt.Errorf("reading agent %s's failures: %w", pass.AgentID, err)
		}
		var lines []string
		for _, w := range wrong {
			drew(w.RunID)
			lines = append(lines, fmt.Sprintf("%s: %s", Key{Part: w.Part, Target: w.Target, Cause: w.Cause}, w.Summary))
		}
		write("What went wrong lately", lines...)
	}

	var rl []string
	for _, id := range pass.RunIds {
		rl = append(rl, id.String())
	}
	write("Runs to read", rl...)

	record, err := im.store.ListAgentProposalRecord(ctx, db.ListAgentProposalRecordParams{AgentID: pass.AgentID, PageSize: recordShown})
	if err != nil {
		return m, fmt.Errorf("reading agent %s's record: %w", pass.AgentID, err)
	}
	var rec []string
	for _, r := range record {
		sig := ""
		if r.Signature != nil {
			sig = *r.Signature
		}
		rec = append(rec, fmt.Sprintf("%s %s (%s, %s): %s. Expected: %s", r.CreatedAt.UTC().Format(time.DateOnly), r.Kind, sig, r.Status, r.Why, r.Hypothesis))
	}
	write("Its record", rec...)

	elsewhere, err := im.store.ListTeamAppliedElsewhere(ctx, db.ListTeamAppliedElsewhereParams{
		TeamID: pass.TeamID, AgentID: pass.AgentID, PageSize: elsewhereShown,
	})
	if err != nil {
		return m, fmt.Errorf("reading team %s's applied changes: %w", pass.TeamID, err)
	}
	var el []string
	for _, e := range elsewhere {
		el = append(el, fmt.Sprintf("%s, %s: %s", e.Agent, e.Kind, e.Why))
	}
	write("Applied on the team's other agents", el...)
	write("Prune candidates", pass.Pruned...)
	m.text = b.String()

	// Whatever reached the pass may steer it, not only what a proposal
	// cites: an outsider in any of it bars a widening change.
	if m.drewOn == nil {
		m.drewOn = []uuid.UUID{}
	}
	if m.outsider, err = im.store.AnyDigestOutsider(ctx, db.AnyDigestOutsiderParams{AgentID: pass.AgentID, RunIds: m.drewOn}); err != nil {
		return m, fmt.Errorf("reading who spoke in agent %s's runs: %w", pass.AgentID, err)
	}
	return m, nil
}

// lookups reads the team's skills, servers and agents by name: what an
// edit names and the setup is shown in.
func (im *Improver) lookups(ctx context.Context, m *material) error {
	skills, err := im.store.ListTeamSkills(ctx, m.agent.TeamID)
	if err != nil {
		return fmt.Errorf("reading team %s's skills: %w", m.agent.TeamID, err)
	}
	m.skills, m.skillNames = map[string]db.Skill{}, map[uuid.UUID]string{}
	for _, s := range skills {
		m.skills[s.Name], m.skillNames[s.ID] = s, s.Name
	}
	servers, err := im.store.ListTeamServersWithTools(ctx, m.agent.TeamID)
	if err != nil {
		return fmt.Errorf("reading team %s's servers: %w", m.agent.TeamID, err)
	}
	m.servers, m.serverOf = map[string]db.ListTeamServersWithToolsRow{}, map[uuid.UUID]string{}
	for _, s := range servers {
		m.servers[s.Name], m.serverOf[s.ID] = s, s.Name
	}
	agents, err := im.store.ListTeamAgentNames(ctx, db.ListTeamAgentNamesParams{TeamID: m.agent.TeamID, Ids: m.state.Agent.Delegates})
	if err != nil {
		return fmt.Errorf("reading agent %s's delegates: %w", m.agent.ID, err)
	}
	m.agents, m.agentNames = map[string]uuid.UUID{}, map[uuid.UUID]string{}
	for _, a := range agents {
		m.agents[a.Name], m.agentNames[a.ID] = a.ID, a.Name
	}
	return nil
}

func nameOr(names map[uuid.UUID]string, id uuid.UUID) string {
	if n, ok := names[id]; ok {
		return n
	}
	return id.String()
}

func (m material) skillName(id uuid.UUID) string  { return nameOr(m.skillNames, id) }
func (m material) serverName(id uuid.UUID) string { return nameOr(m.serverOf, id) }
func (m material) agentName(id uuid.UUID) string  { return nameOr(m.agentNames, id) }

// setup is the agent's harness beyond its text, a line a part.
func (m material) setup() []string {
	a := m.state.Agent
	lines := []string{
		fmt.Sprintf("pattern %s, at most %d steps and %d steer continuations, max tokens %d", a.Pattern, a.MaxSteps, a.SteerContinuations, a.Model.MaxTokens),
	}
	if a.TokenBudget != nil {
		lines = append(lines, fmt.Sprintf("token budget %d", *a.TokenBudget))
	}
	for _, id := range a.Skills {
		s := m.skills[m.skillName(id)]
		lines = append(lines, fmt.Sprintf("skill %s (%s): %s", m.skillName(id), s.Source, s.Description))
	}
	for _, g := range a.MCPServers {
		tools := "every tool"
		if g.Allow != nil {
			tools = strings.Join(g.Allow, ", ")
		}
		lines = append(lines, fmt.Sprintf("server %s: %s", m.serverName(g.ID), tools))
	}
	for _, n := range m.state.Notes {
		lines = append(lines, fmt.Sprintf("note on %s %s: %s", m.serverName(n.Server), n.Tool, n.Note))
	}
	del := "none"
	if a.Delegation != nil {
		del = *a.Delegation
	}
	var delegates []string
	for _, id := range a.Delegates {
		delegates = append(delegates, m.agentName(id))
	}
	lines = append(lines, fmt.Sprintf("delegation %s %s", del, strings.Join(delegates, ", ")))
	lines = append(lines, fmt.Sprintf("web %v, browser %v", flagOf(a.Web), flagOf(a.Browser)))
	return lines
}

func flagOf(b *bool) bool { return b != nil && *b }

// middle keeps the head and tail of a long text, saying how much is left
// out between.
func middle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	half := n / 2
	return string(r[:half]) + fmt.Sprintf("\n… %d characters left out …\n", len(r)-n) + string(r[len(r)-half:])
}
