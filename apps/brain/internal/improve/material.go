package improve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// material is what a pass reads: the message it starts from, and what its
// tools may reach.
type material struct {
	text   string
	agent  db.Agent
	state  proposals.AgentState
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

// baseMaterial is an agent as it is now, with the names its setup is shown
// in: what a pass and a prune both start from.
func (im *Improver) baseMaterial(ctx context.Context, agentID uuid.UUID) (material, error) {
	var m material
	var err error
	if m.agent, err = im.store.GetAgent(ctx, agentID); err != nil {
		return m, fmt.Errorf("reading agent %s: %w", agentID, err)
	}
	if err := im.store.InTx(ctx, func(q *db.Queries) error {
		m.state, err = proposals.AgentNow(ctx, q, m.agent)
		return err
	}); err != nil {
		return m, fmt.Errorf("reading agent %s's setup: %w", agentID, err)
	}
	return m, im.lookups(ctx, &m)
}

// writeFn writes one titled section of a pass's opening message.
type writeFn func(title string, lines ...string)

// drew marks a run whose text reaches the pass.
func (m *material) drew(id uuid.UUID) {
	if !slices.Contains(m.drewOn, id) {
		m.drewOn = append(m.drewOn, id)
	}
}

func (im *Improver) material(ctx context.Context, pass db.ImproverPass) (material, error) {
	m, err := im.baseMaterial(ctx, pass.AgentID)
	if err != nil {
		return m, err
	}
	m.runs, m.keys, m.prune, m.drewOn = map[uuid.UUID]bool{}, slices.Clone(pass.Signatures), pass.Pruned, []uuid.UUID{}
	for _, id := range pass.RunIds {
		m.runs[id] = true
		m.drew(id)
	}

	var b strings.Builder
	write := func(title string, lines ...string) { writeSection(&b, section{title, lines}) }
	write("Agent", m.agent.Name)
	write("Its description", m.state.Agent.Description)
	write("Its instructions", middle(m.state.Agent.Instructions, instructionsChars))
	write("Its setup", m.setup()...)
	if pass.Trigger == triggerEfficiency {
		if err := im.writeEfficiency(ctx, pass, &m, write); err != nil {
			return m, err
		}
	} else {
		if err := im.writePatterns(ctx, pass, &m, write); err != nil {
			return m, err
		}
		if err := im.writeContrast(ctx, pass, &m, write); err != nil {
			return m, err
		}
	}
	var rl []string
	for _, id := range pass.RunIds {
		rl = append(rl, id.String())
	}
	write("Runs to read", rl...)
	if err := im.writeRecord(ctx, pass, write); err != nil {
		return m, err
	}
	write("Prune candidates", pass.Pruned...)
	m.text = b.String()

	// Whatever reached the pass may steer it, not only what a proposal
	// cites: an outsider in any of it bars a widening change.
	if m.outsider, err = im.store.AnyDigestOutsider(ctx, db.AnyDigestOutsiderParams{AgentID: pass.AgentID, RunIds: m.drewOn}); err != nil {
		return m, fmt.Errorf("reading who spoke in agent %s's runs: %w", pass.AgentID, err)
	}
	return m, nil
}

// writePatterns is each pattern the pass was given: how many runs went
// wrong with it, each one's behaviour, and the first summaries.
func (im *Improver) writePatterns(ctx context.Context, pass db.ImproverPass, m *material, write writeFn) error {
	for _, s := range pass.Signatures {
		k, ok := parseKey(s)
		if !ok {
			continue
		}
		rows, err := im.store.ListPatternDigests(ctx, db.ListPatternDigestsParams{
			AgentID: pass.AgentID, Part: k.Part, Target: k.Target, Cause: k.Cause, PageSize: digestsPerKey,
		})
		if err != nil {
			return fmt.Errorf("reading agent %s's pattern %s: %w", pass.AgentID, s, err)
		}
		lines := []string{fmt.Sprintf("%d runs went wrong with this signature.", len(rows))}
		for i, r := range rows {
			m.drew(r.RunID)
			lines = append(lines, "behaviour: "+r.Behaviour)
			if i < summariesPerKey {
				lines = append(lines, "summary: "+r.Summary)
			}
		}
		write("Pattern "+s, lines...)
	}
	return nil
}

// writeContrast is what went well on the lead pattern's part, and, for a
// pass with no pattern, what went wrong lately.
func (im *Improver) writeContrast(ctx context.Context, pass db.ImproverPass, m *material, write writeFn) error {
	part, target := leadPart(pass.Signatures)
	well, err := im.store.ListWentWell(ctx, db.ListWentWellParams{AgentID: pass.AgentID, Part: part, Target: target, PageSize: wentWellShown})
	if err != nil {
		return fmt.Errorf("reading agent %s's successes: %w", pass.AgentID, err)
	}
	var wl []string
	for _, w := range well {
		m.drew(w.RunID)
		wl = append(wl, fmt.Sprintf("%s:%s: %s", w.Part, w.Target, w.Summary))
	}
	write("What went well", wl...)
	if len(pass.Signatures) > 0 {
		return nil
	}
	wrong, err := im.store.ListWentWrong(ctx, db.ListWentWrongParams{AgentID: pass.AgentID, PageSize: wentWrongShown})
	if err != nil {
		return fmt.Errorf("reading agent %s's failures: %w", pass.AgentID, err)
	}
	var lines []string
	for _, w := range wrong {
		m.drew(w.RunID)
		k := Key{Part: w.Part, Target: w.Target, Cause: w.Cause}.String()
		lines = append(lines, fmt.Sprintf("%s: %s", k, w.Summary))
		// A daily pass has no pattern of its own: what it was shown going
		// wrong is what its proposals may answer.
		if !slices.Contains(m.keys, k) {
			m.keys = append(m.keys, k)
		}
	}
	write("What went wrong lately", lines...)
	return nil
}

// writeRecord is the agent's own proposals, and what was applied on the
// team's other agents.
func (im *Improver) writeRecord(ctx context.Context, pass db.ImproverPass, write writeFn) error {
	record, err := im.store.ListAgentProposalRecord(ctx, db.ListAgentProposalRecordParams{AgentID: pass.AgentID, PageSize: recordShown})
	if err != nil {
		return fmt.Errorf("reading agent %s's record: %w", pass.AgentID, err)
	}
	var rec []string
	for _, r := range record {
		sig := ""
		if r.Signature != nil {
			sig = *r.Signature
		}
		rec = append(rec, fmt.Sprintf("%s %s (%s, %s%s%s): %s. Expected: %s", r.CreatedAt.UTC().Format(time.DateOnly), r.Kind, sig, r.Status, saving(r.Goal, r.Saves), watched(r), r.Why, r.Hypothesis))
	}
	write("Its record", rec...)
	elsewhere, err := im.store.ListTeamAppliedElsewhere(ctx, db.ListTeamAppliedElsewhereParams{
		TeamID: pass.TeamID, AgentID: pass.AgentID, PageSize: elsewhereShown,
	})
	if err != nil {
		return fmt.Errorf("reading team %s's applied changes: %w", pass.TeamID, err)
	}
	var el []string
	for _, e := range elsewhere {
		el = append(el, fmt.Sprintf("%s, %s: %s", e.Agent, e.Kind, e.Why))
	}
	write("Applied on the team's other agents", el...)
	return nil
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

// watched is how an applied change went, as its record says it: ", watched:
// clear, 1 of 10 after, 3 of 10 before"; nothing for one never applied.
func watched(r db.ListAgentProposalRecordRow) string {
	if r.Watch == nil {
		return ""
	}
	n := func(v *int16) int16 {
		if v == nil {
			return 0
		}
		return *v
	}
	counts := fmt.Sprintf("%d of %d after, %d of %d before", n(r.WatchAfterBad), n(r.WatchAfterRuns), n(r.WatchBeforeBad), n(r.WatchBeforeRuns))
	if r.Goal == proposals.GoalEfficiency {
		counts += savedLine(r.WatchBeforeTokens, r.WatchAfterTokens, r.WatchBeforeWorkMs, r.WatchAfterWorkMs)
	}
	flagged := ""
	if r.WatchFlag != nil {
		flagged = ", flagged: " + *r.WatchFlag + ", " + counts
	}
	switch *r.Watch {
	case watchWatching:
		return ", being watched: " + counts + " so far"
	case watchClear:
		return ", watched: clear, " + counts
	case watchFlagged:
		return flagged
	case watchCleared:
		return flagged + ", cleared by a person"
	case watchEnded:
		why := ""
		if r.WatchEnded != nil {
			why = *r.WatchEnded
		}
		if flagged != "" {
			return flagged + ", then the watch ended: " + why
		}
		return ", watch ended: " + why
	}
	return ""
}

// saving is what an efficiency change aimed at: ", efficiency, saves
// cost"; nothing for a quality change.
func saving(goal string, saves []string) string {
	if goal != proposals.GoalEfficiency {
		return ""
	}
	return ", efficiency, saves " + strings.Join(saves, " and ")
}

// savedLine is what an efficiency change's good runs came to either side,
// as far as the watch has counted: "; tokens a good run 18400 → 12900, work
// 41.0 s → 33.0 s".
func savedLine(beforeTokens, afterTokens, beforeWork, afterWork *int64) string {
	var parts []string
	if beforeTokens != nil && afterTokens != nil {
		parts = append(parts, fmt.Sprintf("tokens a good run %d → %d", *beforeTokens, *afterTokens))
	}
	if beforeWork != nil && afterWork != nil {
		parts = append(parts, fmt.Sprintf("work %s → %s", seconds(*beforeWork), seconds(*afterWork)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, ", ")
}
