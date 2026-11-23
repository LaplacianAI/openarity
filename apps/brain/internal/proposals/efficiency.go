package proposals

import (
	"slices"
	"strings"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// EfficiencyKinds are the kinds an efficiency change may be: those that can
// make a run do less. A limit only lowered, a delegation only narrowed, a
// skill edit without a script and a loop only made plainer are held to that
// by CheckEfficiency.
var EfficiencyKinds = []Kind{
	KindInstructions, KindToolNote, KindToolsNarrow, KindSkillDrop,
	KindLimits, KindDelegation, KindPattern, KindSkillEdit,
}

// PlainestLoop is the one loop an efficiency change may move an agent to:
// one model call a step, with no planning or review call beside it. Any
// other loop may make more calls than the one it replaces. A stored agent
// always names its loop (harness.Validate refuses an empty one), so a move
// to it is a real change.
const PlainestLoop = string(agent.PatternReAct)

// The marks an efficiency change may not carry: it makes a run do less,
// never more, and never reaches further.
var efficiencyBars = []string{MarkWidensAccess, MarkWidensReach, MarkRaisesCost, MarkChangesCode}

// CheckEfficiency holds an efficiency change to doing less: one of
// EfficiencyKinds, no mark that widens, raises cost or changes code, and a
// loop changed only to PlainestLoop.
func CheckEfficiency(kind Kind, before, after Snapshot) error {
	if !slices.Contains(EfficiencyKinds, kind) {
		names := make([]string, len(EfficiencyKinds))
		for i, k := range EfficiencyKinds {
			names[i] = string(k)
		}
		return refuse("an efficiency change is only %s: %s could widen what the agent does", strings.Join(names, ", "), kind)
	}
	for _, m := range Marks(kind, before, after) {
		if slices.Contains(efficiencyBars, m) {
			return refuse("an efficiency change makes a run do less, and this one %s", strings.ReplaceAll(m, "_", " "))
		}
	}
	if kind == KindPattern && after.Agent != nil && after.Agent.Agent.Pattern != PlainestLoop {
		return refuse("an efficiency change moves an agent only to the %s loop: %s may make more model calls than the loop it replaces",
			PlainestLoop, after.Agent.Agent.Pattern)
	}
	return nil
}
