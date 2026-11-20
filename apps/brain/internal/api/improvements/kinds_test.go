package improvements

import (
	"encoding/hex"
	"errors"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, a := pair(tc.change)
			if got := Marks(tc.kind, b, a); !slices.Equal(got, tc.want) {
				t.Errorf("Marks = %v, want %v", got, tc.want)
			}
		})
	}

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
