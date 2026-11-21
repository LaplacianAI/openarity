package proposals

import (
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
)

// Change is one field a proposal changes, said with names rather than ids,
// so every client draws the same diff.
type Change struct {
	Field  string `json:"field"`
	Type   string `json:"type"` // text | list | value
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// Names resolves the ids a change mentions; an id it cannot name is shown as
// itself.
type Names struct {
	Agents, Skills, Servers map[uuid.UUID]string
}

// Agent, Skill and Server name an id, or show it as itself.
func (n Names) Agent(id uuid.UUID) string  { return n.of(n.Agents, id) }
func (n Names) Skill(id uuid.UUID) string  { return n.of(n.Skills, id) }
func (n Names) Server(id uuid.UUID) string { return n.of(n.Servers, id) }

func (n Names) of(m map[uuid.UUID]string, id uuid.UUID) string {
	if s, ok := m[id]; ok {
		return s
	}
	return id.String()
}

// IDs is every id the two sides mention, for names to resolve.
func IDs(p Proposal) (agentIDs, skillIDs, serverIDs []uuid.UUID) {
	for _, s := range []Snapshot{p.Before, p.After} {
		if s.Agent == nil {
			continue
		}
		agentIDs = append(agentIDs, s.Agent.Agent.Delegates...)
		skillIDs = append(skillIDs, s.Agent.Agent.Skills...)
		for _, g := range s.Agent.Agent.MCPServers {
			serverIDs = append(serverIDs, g.ID)
		}
		for _, n := range s.Agent.Notes {
			serverIDs = append(serverIDs, n.Server)
		}
	}
	return agentIDs, skillIDs, serverIDs
}

func Changes(p Proposal, n Names) []Change {
	if Kind(p.Row.Kind) == KindSkillEdit {
		return skillChanges(*p.Before.Skill, *p.After.Skill)
	}
	var out []Change
	if p.Before.Agent != nil && p.After.Agent != nil {
		out = agentChanges(*p.Before.Agent, *p.After.Agent, n)
	}
	if Kind(p.Row.Kind) == KindSkillNew && p.After.Skill != nil {
		s := *p.After.Skill
		out = append(out, Change{
			Field: "skills", Type: "list",
			Before: []string{}, After: []string{s.Name + " (new)"},
		})
		out = append(out, skillChanges(SkillState{}, s)...)
	}
	return out
}

func agentChanges(b, a AgentState, n Names) []Change {
	var out []Change
	text := func(field, x, y string) { out = append(out, Change{Field: field, Type: "text", Before: x, After: y}) }
	value := func(field, x, y string) { out = append(out, Change{Field: field, Type: "value", Before: x, After: y}) }
	list := func(field string, x, y []string) {
		out = append(out, Change{Field: field, Type: "list", Before: x, After: y})
	}

	for _, f := range agentDiff(b, a) {
		x, y := b.Agent, a.Agent
		switch f {
		case fInstructions:
			text(f, x.Instructions, y.Instructions)
		case fDescription:
			text(f, x.Description, y.Description)
		case fPattern:
			value(f, x.Pattern, y.Pattern)
		case fMaxTokens:
			value(f, itoa(int64(x.Model.MaxTokens)), itoa(int64(y.Model.MaxTokens)))
		case fMaxSteps:
			value(f, itoa(int64(x.MaxSteps)), itoa(int64(y.MaxSteps)))
		case fSteer:
			value(f, itoa(int64(x.SteerContinuations)), itoa(int64(y.SteerContinuations)))
		case fTokenBudget:
			value(f, budget(x.TokenBudget), budget(y.TokenBudget))
		case fDelegation:
			value(f, delegationOf(x), delegationOf(y))
		case fWeb:
			value(f, onOff(x.Web), onOff(y.Web))
		case fBrowser:
			value(f, onOff(x.Browser), onOff(y.Browser))
		case fSkills:
			list(f, named(x.Skills, n.Skills, n), named(y.Skills, n.Skills, n))
		case fDelegates:
			list(f, named(x.Delegates, n.Agents, n), named(y.Delegates, n.Agents, n))
		case fServers:
			list(f, grants(x.MCPServers, n), grants(y.MCPServers, n))
		case fNotes:
			list(f, notes(b.Notes, n), notes(a.Notes, n))
		default:
			// A field no kind may change never reaches a stored proposal.
			value(f, "", "")
		}
	}
	return out
}

func skillChanges(b, a SkillState) []Change {
	var out []Change
	if a.Description != b.Description {
		out = append(out, Change{Field: "description", Type: "text", Before: b.Description, After: a.Description})
	}
	if a.Body != b.Body {
		out = append(out, Change{Field: "body", Type: "text", Before: b.Body, After: a.Body})
	}

	old := map[string]FileState{}
	for _, f := range b.Files {
		old[f.Path] = f
	}
	seen := map[string]bool{}
	for _, f := range a.Files {
		seen[f.Path] = true
		prev, ok := old[f.Path]
		if ok && prev.SHA256 == f.SHA256 {
			continue
		}
		var before any
		if ok {
			before = textOf(prev)
		}
		out = append(out, Change{Field: "files/" + f.Path, Type: "text", Before: before, After: textOf(f)})
	}
	for _, f := range b.Files {
		if !seen[f.Path] {
			out = append(out, Change{Field: "files/" + f.Path, Type: "text", Before: textOf(f), After: nil})
		}
	}
	return out
}

func textOf(f FileState) any {
	if f.Text == nil {
		return nil
	}
	return *f.Text
}

func named(ids []uuid.UUID, m map[uuid.UUID]string, n Names) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = n.of(m, id)
	}
	slices.Sort(out)
	return out
}

func grants(gs []harness.ServerGrant, n Names) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		s := n.Server(g.ID)
		if g.Allow != nil {
			allow := slices.Clone(g.Allow)
			slices.Sort(allow)
			s += ": " + strings.Join(allow, ", ")
		}
		out[i] = s
	}
	slices.Sort(out)
	return out
}

func notes(ns []ToolNote, n Names) []string {
	out := make([]string, len(ns))
	for i, note := range ns {
		out[i] = n.Server(note.Server) + " / " + note.Tool + ": " + note.Note
	}
	slices.Sort(out)
	return out
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func budget(b *int64) string {
	if b == nil {
		return "none"
	}
	return itoa(*b)
}

func onOff(b *bool) string {
	if flag(b) {
		return "on"
	}
	return "off"
}
