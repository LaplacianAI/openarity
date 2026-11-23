package proposals

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
)

func agentAt(pattern string, maxSteps int32) Snapshot {
	return Snapshot{Agent: &AgentState{Agent: harness.Request{Kind: harness.KindCustom, Pattern: pattern, MaxSteps: maxSteps}}}
}

// An efficiency change does less: a kind that can widen is refused, a raised
// limit is refused, a loop change is taken only from a costlier loop to the
// plainest, and a change read from the agent is refused without it.
func TestAnEfficiencyChangeDoesLess(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kind          Kind
		before, after Snapshot
		refused       string
	}{
		{"a server grant", KindServerGrant, Snapshot{}, Snapshot{}, "an efficiency change is only"},
		{"a new skill", KindSkillNew, Snapshot{}, Snapshot{}, "an efficiency change is only"},
		{"a raised limit", KindLimits, agentAt("react", 5), agentAt("react", 9), "raises cost"},
		{"a costlier loop", KindPattern, agentAt("react", 5), agentAt("plan", 5), "only to the react loop"},
		{"a lowered limit", KindLimits, agentAt("react", 9), agentAt("react", 5), ""},
		{"a plainer loop", KindPattern, agentAt("plan", 5), agentAt("react", 5), ""},
		{"from reflection", KindPattern, agentAt("reflection", 5), agentAt("react", 5), ""},
		{"from rewoo", KindPattern, agentAt("rewoo", 5), agentAt("react", 5), "only from plan or reflection"},
		{"react to react", KindPattern, agentAt("react", 5), agentAt("react", 5), "only from plan or reflection"},
		{"a loop with no agent after", KindPattern, agentAt("plan", 5), Snapshot{}, "needs the agent"},
		{"a loop with no agent before", KindPattern, Snapshot{}, agentAt("react", 5), "needs the agent"},
		{"a limit with no agent", KindLimits, Snapshot{}, Snapshot{}, "needs the agent"},
		{"a delegation with no agent", KindDelegation, agentAt("react", 5), Snapshot{}, "needs the agent"},
	} {
		err := CheckEfficiency(tc.kind, tc.before, tc.after)
		switch {
		case tc.refused == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.refused != "" && (err == nil || !Refused(err) || !strings.Contains(err.Error(), tc.refused)):
			t.Errorf("%s: %v, want a refusal: %s", tc.name, err, tc.refused)
		}
	}
}

// Create holds an efficiency change to doing less whoever calls it, before
// anything is written: here with no database at all.
func TestCreateRefusesAnEfficiencyChangeThatDoesMore(t *testing.T) {
	_, err := Create(t.Context(), nil, Proposed{
		Kind: KindPattern, Scope: "domain", Why: "w", Hypothesis: "h",
		Before: agentAt("react", 5), After: agentAt("plan", 5),
		Goal: GoalEfficiency, Saves: []string{SavesCost},
	})
	if err == nil || !Refused(err) || !strings.Contains(err.Error(), "react loop") {
		t.Errorf("Create = %v, want the loop refused", err)
	}
}
