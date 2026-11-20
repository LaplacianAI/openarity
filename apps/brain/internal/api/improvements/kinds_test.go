package improvements

import (
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
)

func errorsAs(err error, target any) bool { return err != nil && errors.As(err, target) }

func slicesContain(s []string, v string) bool { return slices.Contains(s, v) }

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func ptr[T any](v T) *T { return &v }

func base() AgentState {
	return AgentState{Agent: agents.Request{
		Name: "support-bot", Kind: agents.KindCustom, Instructions: "i", Description: "d",
		Model: agents.ModelRef{Name: "m", MaxTokens: 4000}, Pattern: "react", MaxSteps: 10,
		Delegation: ptr("none"), Web: ptr(false), Browser: ptr(false),
	}}
}

func pair(change func(*AgentState)) (Snapshot, Snapshot) {
	b, a := base(), base()
	change(&a)
	return Snapshot{Agent: &b}, Snapshot{Agent: &a}
}

// Each kind takes the change it names and refuses a neighbour's.
func TestCheckPerKind(t *testing.T) {
	server, skillID, delegate := uuid.New(), uuid.New(), uuid.New()
	cases := []struct {
		name   string
		kind   Kind
		change func(*AgentState)
		ok     bool
	}{
		{"instructions", KindInstructions, func(s *AgentState) { s.Agent.Instructions = "j" }, true},
		{"instructions changing pattern", KindInstructions, func(s *AgentState) { s.Agent.Pattern = "plan" }, false},
		{"description", KindDescription, func(s *AgentState) { s.Agent.Description = "e" }, true},
		{"pattern", KindPattern, func(s *AgentState) { s.Agent.Pattern = "plan" }, true},
		{"limits raise", KindLimits, func(s *AgentState) { s.Agent.MaxSteps = 20; s.Agent.Model.MaxTokens = 8000 }, true},
		{"limits touching the model", KindLimits, func(s *AgentState) { s.Agent.Model.Name = "big" }, false},
		{"skill grant", KindSkillGrant, func(s *AgentState) { s.Agent.Skills = []uuid.UUID{skillID} }, true},
		{"skill grant of two", KindSkillGrant, func(s *AgentState) { s.Agent.Skills = []uuid.UUID{skillID, uuid.New()} }, false},
		{"server grant", KindServerGrant, func(s *AgentState) { s.Agent.MCPServers = []agents.ServerGrant{{ID: server}} }, true},
		{"builtin web", KindBuiltinTool, func(s *AgentState) { s.Agent.Web = ptr(true) }, true},
		{"builtin both", KindBuiltinTool, func(s *AgentState) { s.Agent.Web = ptr(true); s.Agent.Browser = ptr(true) }, false},
		{"delegation listed", KindDelegation, func(s *AgentState) { s.Agent.Delegation = ptr("listed"); s.Agent.Delegates = []uuid.UUID{delegate} }, true},
		{"delegation listed with nobody", KindDelegation, func(s *AgentState) { s.Agent.Delegation = ptr("listed") }, false},
		{"unknown kind", Kind("model"), func(s *AgentState) { s.Agent.Model.Name = "x" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, a := pair(tc.change)
			err := Check(tc.kind, b, a)
			if (err == nil) != tc.ok {
				t.Errorf("Check = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// tools_narrow only removes tools, and leaves at least one.
func TestCheckNarrow(t *testing.T) {
	server := uuid.New()
	grant := func(allow ...string) func(*AgentState) {
		return func(s *AgentState) {
			g := agents.ServerGrant{ID: server}
			if allow != nil {
				g.Allow = allow
			}
			s.Agent.MCPServers = []agents.ServerGrant{g}
		}
	}
	for name, tc := range map[string]struct {
		before, after []string
		ok            bool
	}{
		"every tool to one":  {nil, []string{"a"}, true},
		"two to one":         {[]string{"a", "b"}, []string{"a"}, true},
		"adds a tool":        {[]string{"a"}, []string{"a", "b"}, false},
		"swaps a tool":       {[]string{"a", "b"}, []string{"a", "c"}, false},
		"three to a new one": {[]string{"a", "b", "c"}, []string{"d"}, false},
		"same":               {[]string{"a", "b"}, []string{"b", "a"}, false},
		"to every tool":      {[]string{"a"}, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			b, a := base(), base()
			grant(tc.before...)(&b)
			grant(tc.after...)(&a)
			if tc.before == nil {
				b.Agent.MCPServers[0].Allow = nil
			}
			if tc.after == nil {
				a.Agent.MCPServers[0].Allow = nil
			}
			err := Check(KindToolsNarrow, Snapshot{Agent: &b}, Snapshot{Agent: &a})
			if (err == nil) != tc.ok {
				t.Errorf("Check = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// Marks come from the change, whatever proposed it.
func TestMarks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   Kind
		change func(*AgentState)
		want   []string
	}{
		{"server grant widens access", KindServerGrant, func(s *AgentState) { s.Agent.MCPServers = []agents.ServerGrant{{ID: uuid.New()}} }, []string{MarkWidensAccess}},
		{"web on widens access", KindBuiltinTool, func(s *AgentState) { s.Agent.Web = ptr(true) }, []string{MarkWidensAccess}},
		{"web off does not", KindBuiltinTool, func(s *AgentState) {}, nil},
		{"team delegation widens reach", KindDelegation, func(s *AgentState) { s.Agent.Delegation = ptr("team") }, []string{MarkWidensReach}},
		{"raised steps raise cost", KindLimits, func(s *AgentState) { s.Agent.MaxSteps = 20 }, []string{MarkRaisesCost}},
		{"lowered steps do not", KindLimits, func(s *AgentState) { s.Agent.MaxSteps = 5 }, nil},
		{"unchanged limits", KindLimits, func(s *AgentState) {}, nil},
		{"description changes discovery", KindDescription, func(s *AgentState) { s.Agent.Description = "x" }, []string{MarkChangesDiscovery}},
		{"pattern changes behaviour", KindPattern, func(s *AgentState) { s.Agent.Pattern = "plan" }, []string{MarkChangesBehaviour}},
		{"instructions change behaviour", KindInstructions, func(s *AgentState) { s.Agent.Instructions = "j" }, []string{MarkChangesBehaviour}},
		{"a tool note changes behaviour", KindToolNote, func(s *AgentState) {}, []string{MarkChangesBehaviour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, a := pair(tc.change)
			if got := Marks(tc.kind, b, a); !slices.Equal(got, tc.want) {
				t.Errorf("Marks = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a skill body edit changes behaviour, a reference edit does not", func(t *testing.T) {
		before := SkillState{Name: "x", Body: "b", Files: []FileState{{Path: "references/a.md", SHA256: "1"}}}
		body, ref := before, before
		body.Body = "c"
		ref.Files = []FileState{{Path: "references/a.md", SHA256: "2"}}
		if got := Marks(KindSkillEdit, Snapshot{Skill: &before}, Snapshot{Skill: &body}); !slices.Equal(got, []string{MarkChangesBehaviour}) {
			t.Errorf("body edit marks = %v", got)
		}
		if got := Marks(KindSkillEdit, Snapshot{Skill: &before}, Snapshot{Skill: &ref}); got != nil {
			t.Errorf("reference edit marks = %v", got)
		}
	})

	t.Run("a snapshot without its agent marks nothing rather than panicking", func(t *testing.T) {
		if got := Marks(KindLimits, Snapshot{}, Snapshot{}); got != nil {
			t.Errorf("Marks = %v", got)
		}
	})

	t.Run("dropping a budget raises cost", func(t *testing.T) {
		b, a := base(), base()
		b.Agent.TokenBudget = ptr(int64(1000))
		if got := Marks(KindLimits, Snapshot{Agent: &b}, Snapshot{Agent: &a}); !slices.Equal(got, []string{MarkRaisesCost}) {
			t.Errorf("Marks = %v", got)
		}
	})
}

// A skill edit may touch only text files under references/ or scripts/,
// and keeps the old text of what it edits or removes.
func TestCheckSkillEditFiles(t *testing.T) {
	text := func(s string) *string { return &s }
	old := FileState{Path: "references/a.md", SHA256: hashText("old"), Text: text("old")}
	b := SkillState{Name: "x", Description: "d", Body: "b", Source: "upload", Files: []FileState{old}}

	for name, tc := range map[string]struct {
		files []FileState
		ok    bool
	}{
		"edit a reference": {[]FileState{{Path: "references/a.md", SHA256: hashText("new"), Text: text("new")}}, true},
		"remove it":        {nil, true},
		"add a root file":  {[]FileState{old, {Path: "README.md", SHA256: hashText("r"), Text: text("r")}}, false},
		"add without text": {[]FileState{old, {Path: "scripts/x.py", SHA256: hashText("p")}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			a := b
			a.Files = tc.files
			err := Check(KindSkillEdit, Snapshot{Skill: &b}, Snapshot{Skill: &a})
			if (err == nil) != tc.ok {
				t.Errorf("Check = %v, want ok=%v", err, tc.ok)
			}
		})
	}

	t.Run("edit without the old text", func(t *testing.T) {
		bb := b
		bb.Files = []FileState{{Path: "references/a.md", SHA256: hashText("old")}}
		a := b
		a.Files = []FileState{{Path: "references/a.md", SHA256: hashText("new"), Text: text("new")}}
		if err := Check(KindSkillEdit, Snapshot{Skill: &bb}, Snapshot{Skill: &a}); err == nil {
			t.Error("an edit without the old text, which a revert needs, was allowed")
		}
	})
}

// A note whose tool has since been taken away stays as it was, and does not
// block the next note; the one a proposal adds or changes is still held.
func TestCheckNoteLeavesAnOrphanAlone(t *testing.T) {
	server := uuid.New()
	withNotes := func(notes ...ToolNote) AgentState {
		s := base()
		s.Agent.MCPServers = []agents.ServerGrant{{ID: server, Allow: []string{"search", "fetch"}}}
		s.Notes = notes
		return s
	}
	orphan := ToolNote{Server: server, Tool: "gone", Note: "old advice"}
	fresh := ToolNote{Server: server, Tool: "search", Note: "quote the query"}

	b := withNotes(orphan)
	if err := Check(KindToolNote, Snapshot{Agent: &b}, Snapshot{Agent: ptr(withNotes(orphan, fresh))}); err != nil {
		t.Errorf("a new note beside an orphan was refused: %v", err)
	}
	edited := ToolNote{Server: server, Tool: "gone", Note: "new advice"}
	if err := Check(KindToolNote, Snapshot{Agent: &b}, Snapshot{Agent: ptr(withNotes(edited))}); err == nil {
		t.Error("editing a note on a tool the agent may not call was allowed")
	}
	two := ToolNote{Server: server, Tool: "fetch", Note: "send the full URL"}
	if err := Check(KindToolNote, Snapshot{Agent: &b}, Snapshot{Agent: ptr(withNotes(orphan, fresh, two))}); err == nil {
		t.Error("adding two notes was allowed")
	}
	again := ToolNote{Server: server, Tool: "search", Note: "and cite it"}
	if err := Check(KindToolNote, Snapshot{Agent: &b}, Snapshot{Agent: ptr(withNotes(orphan, fresh, again))}); err == nil {
		t.Error("two notes on one tool were allowed")
	}
}

// A server_drop removes the dropped server's notes and leaves every other
// note exactly as it was.
func TestServerDropKeepsTheOtherNotes(t *testing.T) {
	s1, s2 := uuid.New(), uuid.New()
	b := base()
	b.Agent.MCPServers = []agents.ServerGrant{{ID: s1}, {ID: s2}}
	b.Notes = []ToolNote{{Server: s2, Tool: "b", Note: "two"}, {Server: s1, Tool: "a", Note: "one"}}

	clean := base()
	clean.Agent.MCPServers = []agents.ServerGrant{{ID: s2}}
	clean.Notes = []ToolNote{{Server: s2, Tool: "b", Note: "two"}}
	if err := Check(KindServerDrop, Snapshot{Agent: &b}, Snapshot{Agent: &clean}); err != nil {
		t.Errorf("a clean drop was refused: %v", err)
	}

	sneaky := clean
	sneaky.Notes = []ToolNote{{Server: s2, Tool: "b", Note: "rewritten"}}
	if err := Check(KindServerDrop, Snapshot{Agent: &b}, Snapshot{Agent: &sneaky}); err == nil {
		t.Error("a drop that rewrote another server's note was allowed")
	}
}

// Comparing states leaves the caller's own slices in the order they were.
func TestComparingDoesNotReorderTheCallersState(t *testing.T) {
	x, y := uuid.MustParse("ffffffff-0000-0000-0000-000000000000"), uuid.MustParse("00000000-0000-0000-0000-00000000000f")
	s := base()
	s.Agent.Skills = []uuid.UUID{x, y}
	s.Agent.MCPServers = []agents.ServerGrant{{ID: x, Allow: []string{"b", "a"}}, {ID: y}}
	s.Notes = []ToolNote{{Server: x, Tool: "z", Note: "n"}, {Server: y, Tool: "a", Note: "n"}}
	other := base()

	agentDiff(s, other)
	sameAgent(s, other)

	if s.Agent.Skills[0] != x || s.Agent.MCPServers[0].ID != x || s.Agent.MCPServers[0].Allow[0] != "b" || s.Notes[0].Server != x {
		t.Errorf("the state was reordered: %+v", s)
	}
}

// Every field of an agent is either named by the kind rules or caught by
// the catch-all. A field added to agents.Request that this test does not
// see cleared makes it fail, so the rules are told about it.
func TestEveryAgentFieldIsChecked(t *testing.T) {
	var r agents.Request
	v := reflect.ValueOf(&r).Elem()
	fill(v)
	if got := unnamed(r); !reflect.DeepEqual(got, agents.Request{}) {
		t.Errorf("unnamed leaves fields the kinds never name: %+v", got)
	}

	// And a difference in a field no kind names is refused by every kind.
	b, a := base(), base()
	a.Agent.Model.Name = "other"
	if err := Check(KindInstructions, Snapshot{Agent: &b}, Snapshot{Agent: &a}); err == nil {
		t.Error("an instructions proposal changed the model")
	}
}

// fill sets every field of v to something other than its zero value.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Field(i).CanSet() {
				fill(v.Field(i))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i))
		}
	case reflect.Uint8, reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128, reflect.Chan,
		reflect.Func, reflect.Interface, reflect.UnsafePointer:
		// No agent field is of these kinds.
	}
}
