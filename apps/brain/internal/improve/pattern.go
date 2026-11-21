package improve

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// A signature becomes a pattern in this many runs.
	patternBar = 3
	// A pattern pass is given at most this many of an agent's patterns.
	keysPerPass = 3
	// Runs a pass may read in full: failures, then successes to contrast.
	failuresToRead  = 6
	successesToRead = 4
	// Pruning reads this many of an agent's newest runs.
	pruneWindow = 50
	// The signature of a proposal that prunes, before its candidate.
	prunePrefix = "prune:"
)

// Key is a digest's signature as patterns count it: part, target, cause.
type Key struct{ Part, Target, Cause string }

func (k Key) String() string { return k.Part + ":" + k.Target + ":" + k.Cause }

// parseKey reads part:target:cause. The part and cause are from closed sets
// with no colon, so a target holding one still reads back whole.
func parseKey(s string) (Key, bool) {
	part, rest, ok := strings.Cut(s, ":")
	if !ok {
		return Key{}, false
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return Key{}, false
	}
	k := Key{Part: part, Target: rest[:i], Cause: rest[i+1:]}
	if !oneOf(k.Part, parts) || !oneOf(k.Cause, causes) {
		return Key{}, false
	}
	return k, true
}

// plannedPass is the work one pass is given: an agent, why, and what for.
type plannedPass struct {
	TeamID  uuid.UUID `json:"team_id"`
	AgentID uuid.UUID `json:"agent_id"`
	Trigger string    `json:"trigger"`
	Keys    []string  `json:"keys,omitempty"`
	Prune   []string  `json:"prune,omitempty"`
}

func (p plannedPass) encode() string {
	b, _ := json.Marshal(p) // a struct of strings and ids always encodes
	return string(b)
}

func decodePlan(s string) (plannedPass, error) {
	var p plannedPass
	err := json.Unmarshal([]byte(s), &p)
	return p, err
}

// patternPasses groups the patterns over the bar by agent, each agent's
// largest first and at most keysPerPass of them.
func patternPasses(rows []db.ListPatternsRow) []plannedPass {
	var out []plannedPass
	for _, r := range rows {
		k := Key{Part: r.Part, Target: r.Target, Cause: r.Cause}.String()
		if n := len(out); n > 0 && out[n-1].AgentID == r.AgentID {
			if len(out[n-1].Keys) < keysPerPass {
				out[n-1].Keys = append(out[n-1].Keys, k)
			}
			continue
		}
		out = append(out, plannedPass{TeamID: r.TeamID, AgentID: r.AgentID, Trigger: triggerPattern, Keys: []string{k}})
	}
	return out
}

// toRead is one run a pass may read in full, with why it was chosen.
type toRead struct {
	run     uuid.UUID
	line    string
	failure bool
	rank    int
	at      int64
}

// failureRank orders failures: a member's 👎 first, then the judge's worst
// verdicts.
func failureRank(verdict string, down int32) int {
	switch {
	case down > 0:
		return 0
	case verdict == verdictWrong:
		return 1
	case verdict == verdictNotDone:
		return 2
	case verdict == verdictPartly:
		return 3
	}
	return 4
}

// chooseRuns picks at most failuresToRead failures, clearest and newest
// first, and at most successesToRead successes, each run once.
func chooseRuns(failures, successes []toRead) []toRead {
	order := func(a, b toRead) int {
		if c := cmp.Compare(a.rank, b.rank); c != 0 {
			return c
		}
		return cmp.Compare(b.at, a.at)
	}
	slices.SortStableFunc(failures, order)
	slices.SortStableFunc(successes, order)
	seen := map[uuid.UUID]bool{}
	var out []toRead
	take := func(from []toRead, n int) {
		for _, r := range from {
			if n == 0 {
				return
			}
			if seen[r.run] {
				continue
			}
			seen[r.run] = true
			out = append(out, r)
			n--
		}
	}
	take(failures, failuresToRead)
	take(successes, successesToRead)
	return out
}

// grantedTool is one tool an agent may call, by the name its runs know it
// by and the name a proposal gives it.
type grantedTool struct{ runName, server, tool string }

// pruneCandidates are the skills and tools an agent held in every one of
// its last pruneWindow runs and never used: a skill no run opened, a tool
// no run called, directly or through call_tool. Fewer runs than the window
// say nothing.
func pruneCandidates(runs []uuid.UUID, use []db.ListRunsUseRow, skills []string, tools []grantedTool) []string {
	if len(runs) < pruneWindow {
		return nil
	}
	heldSkills, heldTools := map[string]int{}, map[string]int{}
	opened, called := map[string]bool{}, map[string]bool{}
	for _, u := range use {
		switch u.Kind {
		case "started":
			var p startedPayload
			if json.Unmarshal(u.Payload, &p) != nil {
				continue
			}
			for _, s := range unique(p.Skills) {
				heldSkills[s]++
			}
			for _, t := range unique(p.Tools) {
				heldTools[t]++
			}
		case "tool_call":
			var p toolCallPayload
			if json.Unmarshal(u.Payload, &p) != nil {
				continue
			}
			called[p.Name] = true
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(p.Arguments), &args) // arguments that are not JSON name nothing
			switch p.Name {
			case skillTool:
				opened[args.Name] = true
			case callTool:
				called[args.Name] = true
			}
		}
	}

	var out []string
	for _, s := range skills {
		if heldSkills[s] == len(runs) && !opened[s] {
			out = append(out, "skill:"+s)
		}
	}
	for _, t := range tools {
		if heldTools[t.runName] == len(runs) && !called[t.runName] {
			out = append(out, "tool:"+t.server+"/"+t.tool)
		}
	}
	slices.Sort(out)
	return out
}

func unique(xs []string) []string {
	xs = slices.Clone(xs)
	slices.Sort(xs)
	return slices.Compact(xs)
}
