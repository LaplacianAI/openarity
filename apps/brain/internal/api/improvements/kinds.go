package improvements

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
)

// Kind is what part of a harness a proposal changes. Each kind may change
// only its own fields; Check refuses anything else.
type Kind string

const (
	KindInstructions Kind = "instructions"
	KindDescription  Kind = "description"
	KindSkillGrant   Kind = "skill_grant"
	KindSkillDrop    Kind = "skill_drop"
	KindSkillNew     Kind = "skill_new"
	KindSkillEdit    Kind = "skill_edit"
	KindServerGrant  Kind = "server_grant"
	KindServerDrop   Kind = "server_drop"
	KindToolsNarrow  Kind = "tools_narrow"
	KindBuiltinTool  Kind = "builtin_tool"
	KindToolNote     Kind = "tool_note"
	KindLimits       Kind = "limits"
	KindDelegation   Kind = "delegation"
	KindPattern      Kind = "pattern"
)

// Marks say what kind of change it is beyond its kind: computed from the
// states by the brain, never taken from whoever proposed it.
const (
	MarkWidensAccess     = "widens_access"
	MarkWidensReach      = "widens_reach"
	MarkRaisesCost       = "raises_cost"
	MarkChangesCode      = "changes_code"
	MarkChangesDiscovery = "changes_discovery"
	MarkChangesBehaviour = "changes_behaviour"
)

// ruleError is a change the rules refuse: said to the caller as it is.
type ruleError struct{ msg string }

func (e ruleError) Error() string { return e.msg }

var errChanged = errors.New("the target changed since this was proposed")

func refuse(format string, a ...any) error { return ruleError{fmt.Sprintf(format, a...)} }

// Refused is whether err is a change the rules refuse, said as it is, as
// opposed to a failure to read or write.
func Refused(err error) bool {
	var r ruleError
	return errors.As(err, &r)
}

// The fields an agent proposal can tell apart. Each kind names the ones it
// may change; the rest must be equal.
const (
	fName         = "name"
	fDescription  = "description"
	fKind         = "kind"
	fInstructions = "instructions"
	fModel        = "model"
	fMaxTokens    = "max_tokens"
	fPattern      = "pattern"
	fMaxSteps     = "max_steps"
	fSteer        = "steer_continuations"
	fOutput       = "output_schema"
	fParser       = "parser"
	fParserModel  = "parser_model"
	fServers      = "mcp_servers"
	fSkills       = "skills"
	fTokenBudget  = "token_budget"
	fDelegation   = "delegation"
	fDelegates    = "delegates"
	fWeb          = "web"
	fBrowser      = "browser"
	// fWorkspace is named so that a proposal from before the field existed
	// still compares equal, and is in no kind: an agent may not propose
	// itself a shell. A person grants it.
	fWorkspace = "workspace"
	fNotes     = "notes"
	// Any field of the agent this list does not name, which no kind may change.
	fOther = "other"
)

var agentKinds = map[Kind][]string{
	KindInstructions: {fInstructions},
	KindDescription:  {fDescription},
	KindSkillGrant:   {fSkills},
	KindSkillDrop:    {fSkills},
	KindSkillNew:     {}, // the agent changes only when it is applied, by the new skill's grant
	KindServerGrant:  {fServers},
	KindServerDrop:   {fServers, fNotes},
	KindToolsNarrow:  {fServers},
	KindBuiltinTool:  {fWeb, fBrowser},
	KindToolNote:     {fNotes},
	KindLimits:       {fMaxTokens, fMaxSteps, fSteer, fTokenBudget},
	KindDelegation:   {fDelegation, fDelegates},
	KindPattern:      {fPattern},
}

// agentDiff names every field in which two agent states differ.
func agentDiff(a, b AgentState) []string {
	a, b = a.cloned(), b.cloned()
	a.canonical()
	b.canonical()
	x, y := a.Agent, b.Agent
	var out []string
	add := func(field string, same bool) {
		if !same {
			out = append(out, field)
		}
	}
	add(fName, x.Name == y.Name)
	add(fDescription, x.Description == y.Description)
	add(fKind, x.Kind == y.Kind)
	add(fInstructions, x.Instructions == y.Instructions)
	add(fModel, x.Model.Name == y.Model.Name && reflect.DeepEqual(x.Model.Temperature, y.Model.Temperature))
	add(fMaxTokens, x.Model.MaxTokens == y.Model.MaxTokens)
	add(fPattern, x.Pattern == y.Pattern)
	add(fMaxSteps, x.MaxSteps == y.MaxSteps)
	add(fSteer, x.SteerContinuations == y.SteerContinuations)
	add(fOutput, jsonEqual(x.OutputSchema, y.OutputSchema))
	add(fParser, x.Parser == y.Parser)
	add(fParserModel, jsonEqual(x.ParserModel, y.ParserModel))
	add(fServers, jsonEqual(x.MCPServers, y.MCPServers))
	add(fSkills, jsonEqual(x.Skills, y.Skills))
	add(fTokenBudget, reflect.DeepEqual(x.TokenBudget, y.TokenBudget))
	add(fDelegation, delegationOf(x) == delegationOf(y))
	add(fDelegates, jsonEqual(x.Delegates, y.Delegates))
	add(fWeb, flag(x.Web) == flag(y.Web))
	add(fBrowser, flag(x.Browser) == flag(y.Browser))
	add(fWorkspace, flag(x.Workspace) == flag(y.Workspace))
	add(fNotes, jsonEqual(a.Notes, b.Notes))
	// Every field named above blanked, what is left must match: a field added
	// to the agent later is refused by every kind until a kind names it.
	add(fOther, jsonEqual(unnamed(x), unnamed(y)))
	return out
}

// unnamed is an agent request with every field agentDiff names cleared.
func unnamed(r agents.Request) agents.Request {
	r.Name, r.Description, r.Kind, r.Instructions = "", "", "", ""
	r.Model.Name, r.Model.Temperature, r.Model.MaxTokens = "", nil, 0
	r.Pattern, r.MaxSteps, r.SteerContinuations = "", 0, 0
	r.OutputSchema, r.Parser, r.ParserModel = nil, false, nil
	r.MCPServers, r.Skills, r.TokenBudget = nil, nil, nil
	r.Delegation, r.Delegates, r.Web, r.Browser, r.Workspace = nil, nil, nil, nil, nil
	return r
}

func delegationOf(r agents.Request) string {
	if r.Delegation == nil {
		return "none"
	}
	return *r.Delegation
}

func flag(b *bool) bool { return b != nil && *b }

// Check is whether a proposal of this kind may turn before into after. It
// holds when a proposal is stored and again before it is applied.
func Check(kind Kind, before, after Snapshot) error {
	if kind == KindSkillEdit {
		return checkSkillEdit(before, after)
	}

	allowed, ok := agentKinds[kind]
	if !ok {
		return refuse("%q is not a kind of improvement", kind)
	}
	if before.Agent == nil || after.Agent == nil || before.Skill != nil {
		return refuse("a %s proposal is about an agent", kind)
	}
	if k := before.Agent.Agent.Kind; k != agents.KindCustom {
		return refuse("the agent is a built-in (%s) agent, which is code", k)
	}

	diff := agentDiff(*before.Agent, *after.Agent)
	for _, f := range diff {
		if !slices.Contains(allowed, f) {
			return refuse("a %s proposal cannot change the agent's %s", kind, f)
		}
	}

	if kind == KindSkillNew {
		if after.Skill == nil {
			return refuse("a skill_new proposal carries the skill it creates")
		}
		if err := checkNewSkill(*after.Skill); err != nil {
			return err
		}
		return nil
	}
	if after.Skill != nil {
		return refuse("only a skill_new proposal carries a skill beside the agent")
	}
	if len(diff) == 0 {
		return refuse("the proposal changes nothing")
	}
	return checkAgentKind(kind, *before.Agent, *after.Agent)
}

func checkAgentKind(kind Kind, b, a AgentState) error {
	switch kind {
	case KindSkillGrant:
		added, removed := setDiff(b.Agent.Skills, a.Agent.Skills)
		if len(added) != 1 || len(removed) != 0 {
			return refuse("a skill_grant grants exactly one skill and drops none")
		}
	case KindSkillDrop:
		added, removed := setDiff(b.Agent.Skills, a.Agent.Skills)
		if len(added) != 0 || len(removed) != 1 {
			return refuse("a skill_drop drops exactly one skill and grants none")
		}
	case KindServerGrant:
		added, removed := setDiff(serverIDs(b), serverIDs(a))
		if len(added) != 1 || len(removed) != 0 || changedGrants(b, a, added) {
			return refuse("a server_grant grants exactly one server and changes no other grant")
		}
	case KindServerDrop:
		added, removed := setDiff(serverIDs(b), serverIDs(a))
		if len(added) != 0 || len(removed) != 1 || changedGrants(b, a, nil) {
			return refuse("a server_drop drops exactly one server and changes no other grant")
		}
		for _, n := range a.Notes {
			if n.Server == removed[0] {
				return refuse("a server_drop removes the notes on that server's tools")
			}
		}
		kept := slices.DeleteFunc(slices.Clone(b.Notes), func(n ToolNote) bool { return n.Server == removed[0] })
		if !sameNotes(kept, a.Notes) {
			return refuse("a server_drop changes only the dropped server's notes")
		}
	case KindToolsNarrow:
		return checkNarrow(b, a)
	case KindBuiltinTool:
		if flag(b.Agent.Web) != flag(a.Agent.Web) && flag(b.Agent.Browser) != flag(a.Agent.Browser) {
			return refuse("a builtin_tool proposal turns one built-in tool on or off")
		}
	case KindToolNote:
		return checkNote(b, a)
	case KindDelegation:
		if delegationOf(a.Agent) == "listed" && len(a.Agent.Delegates) == 0 {
			return refuse("delegation listed names at least one delegate")
		}
	case KindInstructions, KindDescription, KindLimits, KindPattern:
		// Their one column is all they may change, and agents.Validate holds
		// it to the same bounds a person's edit meets.
	case KindSkillNew, KindSkillEdit:
		// Checked before this, on the skill they carry.
	}
	return nil
}

func setDiff(before, after []uuid.UUID) (added, removed []uuid.UUID) {
	for _, id := range after {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	for _, id := range before {
		if !slices.Contains(after, id) {
			removed = append(removed, id)
		}
	}
	return added, removed
}

func serverIDs(s AgentState) []uuid.UUID {
	ids := make([]uuid.UUID, len(s.Agent.MCPServers))
	for i, g := range s.Agent.MCPServers {
		ids[i] = g.ID
	}
	return ids
}

func grantOf(s AgentState, id uuid.UUID) (agents.ServerGrant, bool) {
	i := slices.IndexFunc(s.Agent.MCPServers, func(g agents.ServerGrant) bool { return g.ID == id })
	if i < 0 {
		return agents.ServerGrant{}, false
	}
	return s.Agent.MCPServers[i], true
}

// changedGrants is whether a grant present on both sides differs, other than
// those in skip.
func changedGrants(b, a AgentState, skip []uuid.UUID) bool {
	for _, g := range b.Agent.MCPServers {
		if slices.Contains(skip, g.ID) {
			continue
		}
		if other, ok := grantOf(a, g.ID); ok && !jsonEqual(g.Allow, other.Allow) {
			return true
		}
	}
	return false
}

// sameNotes is whether two sets of notes are the same, in any order.
func sameNotes(x, y []ToolNote) bool {
	a, b := AgentState{Notes: x}, AgentState{Notes: y}
	return jsonEqual(a.cloned().sortedNotes(), b.cloned().sortedNotes())
}

// checkNarrow holds a tools_narrow to one grant's allow-list, cut: the same
// servers, one list shorter and inside what it was. A grant of every tool
// (no list) narrows to any non-empty list.
func checkNarrow(b, a AgentState) error {
	if !jsonEqual(serverIDs(b), serverIDs(a)) {
		return refuse("a tools_narrow keeps the same servers")
	}
	changed := 0
	for _, g := range b.Agent.MCPServers {
		other, _ := grantOf(a, g.ID)
		if jsonEqual(g.Allow, other.Allow) {
			continue
		}
		changed++
		if len(other.Allow) == 0 {
			return refuse("a tools_narrow leaves the server at least one tool; drop the server instead")
		}
		if g.Allow != nil {
			for _, t := range other.Allow {
				if !slices.Contains(g.Allow, t) {
					return refuse("a tools_narrow only removes tools; %q was not allowed", t)
				}
			}
			if len(other.Allow) >= len(g.Allow) {
				return refuse("a tools_narrow removes at least one tool")
			}
		}
	}
	if changed != 1 {
		return refuse("a tools_narrow changes exactly one server's tools")
	}
	return nil
}

// checkNote holds a tool_note to one note added, changed or removed. The note
// it adds or changes is on a server the agent holds and a tool the grant
// allows; the others are left as they are, even one whose tool a person has
// since taken away, which the run never tells and must not block the next.
func checkNote(b, a AgentState) error {
	key := func(n ToolNote) string { return n.Server.String() + "\x00" + n.Tool }
	before := map[string]string{}
	for _, n := range b.Notes {
		before[key(n)] = n.Note
	}
	after := map[string]ToolNote{}
	for _, n := range a.Notes {
		if _, twice := after[key(n)]; twice {
			return refuse("a tool has one note; %q has two", n.Tool)
		}
		after[key(n)] = n
	}

	changed := 0
	for k, n := range after {
		if old, had := before[k]; had && old == n.Note {
			continue
		}
		changed++
		if oneLine(n.Note) == "" || utf8.RuneCountInString(n.Note) > 1000 || n.Tool == "" {
			return refuse("a tool note is 1-1000 characters on a named tool")
		}
		g, ok := grantOf(a, n.Server)
		if !ok {
			return refuse("a tool note is on a server the agent holds")
		}
		if g.Allow != nil && !slices.Contains(g.Allow, n.Tool) {
			return refuse("a tool note is on a tool the agent may call; %q is not allowed", n.Tool)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			changed++
		}
	}
	if changed != 1 {
		return refuse("a tool_note adds, changes or removes exactly one note")
	}
	return nil
}

// A skill a proposal creates or edits is the team's own, and its changed
// files are text under references/ or scripts/.
func checkNewSkill(s SkillState) error {
	if s.Source != "upload" {
		return refuse("a proposed skill is the team's own, never an import")
	}
	for _, f := range s.Files {
		if err := checkProposedFile(f); err != nil {
			return err
		}
		if f.Text == nil {
			return refuse("a new skill's file %q carries its text", f.Path)
		}
	}
	return nil
}

func checkProposedFile(f FileState) error {
	if !strings.HasPrefix(f.Path, "references/") && !strings.HasPrefix(f.Path, "scripts/") {
		return refuse("a proposal changes files under references/ or scripts/, not %q", f.Path)
	}
	if f.Text != nil && (!utf8.ValidString(*f.Text) || strings.ContainsRune(*f.Text, 0)) {
		return refuse("%q is not text", f.Path)
	}
	return nil
}

func checkSkillEdit(before, after Snapshot) error {
	if before.Skill == nil || after.Skill == nil || before.Agent != nil || after.Agent != nil {
		return refuse("a skill_edit proposal is about a skill")
	}
	b, a := *before.Skill, *after.Skill
	if b.Source != "upload" {
		return refuse("the skill is imported from %s; an imported skill is never edited", b.Source)
	}
	if a.Name != b.Name || a.Source != b.Source || !reflect.DeepEqual(a.License, b.License) ||
		!reflect.DeepEqual(a.Compatibility, b.Compatibility) || !reflect.DeepEqual(a.AllowedTools, b.AllowedTools) ||
		!jsonEqual(normMeta(a.Metadata), normMeta(b.Metadata)) {
		return refuse("a skill_edit changes the description, the body and files, nothing else")
	}

	old := map[string]FileState{}
	for _, f := range b.Files {
		old[f.Path] = f
	}
	changed := a.Description != b.Description || a.Body != b.Body
	// A replace leaves the old files to the reaper, so the before side keeps
	// the text of every file the change edits or removes: a revert writes it.
	for _, f := range a.Files {
		prev, ok := old[f.Path]
		delete(old, f.Path)
		if ok && prev.SHA256 == f.SHA256 {
			continue
		}
		changed = true
		if err := checkProposedFile(f); err != nil {
			return err
		}
		if f.Text == nil {
			return refuse("a changed file %q carries its text", f.Path)
		}
		if ok && prev.Text == nil {
			return refuse("an edited file %q keeps its old text, for a revert", f.Path)
		}
	}
	for _, f := range old {
		changed = true
		if err := checkProposedFile(f); err != nil {
			return err
		}
		if f.Text == nil {
			return refuse("a removed file %q keeps its old text, for a revert", f.Path)
		}
	}
	if !changed {
		return refuse("the proposal changes nothing")
	}
	return nil
}

func normMeta(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// Marks are what the page warns of. A pure function of the change.
func Marks(kind Kind, before, after Snapshot) []string {
	var out []string
	if needsAgent(kind) && (before.Agent == nil || after.Agent == nil) {
		return out
	}
	switch kind {
	case KindServerGrant:
		out = append(out, MarkWidensAccess)
	case KindBuiltinTool:
		if a := after.Agent; a != nil && (flag(a.Agent.Web) && !flag(before.Agent.Agent.Web) ||
			flag(a.Agent.Browser) && !flag(before.Agent.Agent.Browser)) {
			out = append(out, MarkWidensAccess)
		}
	case KindDelegation:
		if widens(before.Agent.Agent, after.Agent.Agent) {
			out = append(out, MarkWidensReach)
		}
	case KindLimits:
		if raises(before.Agent.Agent, after.Agent.Agent) {
			out = append(out, MarkRaisesCost)
		}
	case KindDescription:
		out = append(out, MarkChangesDiscovery)
	case KindPattern, KindInstructions, KindToolNote:
		// What the agent is told, and so what it does.
		out = append(out, MarkChangesBehaviour)
	case KindSkillNew:
		// A new skill's body is more the agent will be told.
		out = append(out, MarkChangesBehaviour)
		if after.Skill != nil && after.Skill.AllowedTools != nil {
			out = append(out, MarkWidensAccess)
		}
		if after.Skill != nil && hasScripts(nil, after.Skill.Files) {
			out = append(out, MarkChangesCode)
		}
	case KindSkillEdit:
		if before.Skill == nil || after.Skill == nil {
			return out
		}
		if before.Skill.Body != after.Skill.Body {
			out = append(out, MarkChangesBehaviour)
		}
		if hasScripts(before.Skill.Files, after.Skill.Files) {
			out = append(out, MarkChangesCode)
		}
	case KindSkillGrant, KindSkillDrop, KindServerDrop, KindToolsNarrow:
		// They narrow what the agent holds, or grant a skill the team has.
	}
	return out
}

// needsAgent is whether a kind's marks read the agent on both sides.
func needsAgent(kind Kind) bool {
	return kind == KindBuiltinTool || kind == KindDelegation || kind == KindLimits
}

func widens(b, a agents.Request) bool {
	rank := map[string]int{"none": 0, "listed": 1, "team": 2}
	if rank[delegationOf(a)] > rank[delegationOf(b)] {
		return true
	}
	added, _ := setDiff(b.Delegates, a.Delegates)
	return delegationOf(a) == "listed" && len(added) > 0
}

func raises(b, a agents.Request) bool {
	if a.Model.MaxTokens > b.Model.MaxTokens || a.MaxSteps > b.MaxSteps || a.SteerContinuations > b.SteerContinuations {
		return true
	}
	switch {
	case a.TokenBudget == nil && b.TokenBudget != nil:
		return true // no budget is no limit
	case a.TokenBudget != nil && b.TokenBudget != nil:
		return *a.TokenBudget > *b.TokenBudget
	}
	return false
}

// hasScripts is whether a file under scripts/ is added or changed.
func hasScripts(before, after []FileState) bool {
	for _, f := range after {
		if !strings.HasPrefix(f.Path, "scripts/") {
			continue
		}
		i := slices.IndexFunc(before, func(b FileState) bool { return b.Path == f.Path })
		if i < 0 || before[i].SHA256 != f.SHA256 {
			return true
		}
	}
	return false
}

// oneLine is a note as the model is told it: its words on one line, with
// control and formatting characters (bidi overrides among them) gone. A note
// that is nothing once this is done says nothing, and is refused.
func oneLine(note string) string {
	return strings.Join(strings.FieldsFunc(note, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	}), " ")
}
