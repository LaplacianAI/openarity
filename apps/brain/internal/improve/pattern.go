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
	// RunsSeen is how many judged runs a daily pass had since the agent's
	// last pass; none for a pass a pattern started.
	RunsSeen *int32 `json:"runs_seen,omitempty"`
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

// How clear a failure is, clearest first: a member's 👎, then the judge's
// verdicts from worst, then a failure nothing weighed.
const (
	rankThumbsDown = iota
	rankWrong
	rankNotDone
	rankPartly
	rankUnweighed
)

// failureRank orders failures: a member's 👎 first, then the judge's worst
// verdicts.
func failureRank(verdict string, down int32) int {
	switch {
	case down > 0:
		return rankThumbsDown
	case verdict == verdictWrong:
		return rankWrong
	case verdict == verdictNotDone:
		return rankNotDone
	case verdict == verdictPartly:
		return rankPartly
	}
	return rankUnweighed
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

func unique(xs []string) []string {
	xs = slices.Clone(xs)
	slices.Sort(xs)
	return slices.Compact(xs)
}
