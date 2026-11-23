package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Proposed is a proposal as the improver writes it.
type Proposed struct {
	TeamID     uuid.UUID
	AgentID    uuid.UUID
	SkillID    *uuid.UUID
	Kind       Kind
	Why        string
	Hypothesis string
	Scope      string
	Signature  *string
	RunIDs     []uuid.UUID
	Outsider   bool
	Before     Snapshot
	After      Snapshot
	// The improver pass that wrote it; nil for one written any other way.
	PassID *uuid.UUID
	// Goal is GoalQuality, the default, or GoalEfficiency, which names
	// what it Saves: SavesCost, SavesTime or both.
	Goal  string
	Saves []string
}

// What a proposal is for.
const (
	GoalQuality    = "quality"
	GoalEfficiency = "efficiency"
	SavesCost      = "cost"
	SavesTime      = "time"
)

// Create stores a proposal after holding it to its kind's rules. Its marks
// are computed here, never taken from the caller.
func Create(ctx context.Context, q *db.Queries, n Proposed) (db.ImprovementProposal, error) {
	if (n.Kind == KindSkillEdit) != (n.SkillID != nil) {
		return db.ImprovementProposal{}, refuse("a skill_edit names its skill, and only a skill_edit does")
	}
	if n.Scope != "invariant" && n.Scope != "domain" {
		return db.ImprovementProposal{}, refuse("scope is invariant or domain")
	}
	if err := Check(n.Kind, n.Before, n.After); err != nil {
		return db.ImprovementProposal{}, err
	}
	goal, saves, err := goalOf(n)
	if err != nil {
		return db.ImprovementProposal{}, err
	}
	for _, s := range []*SkillState{n.Before.Skill, n.After.Skill} {
		if s == nil {
			continue
		}
		for _, f := range s.Files {
			if f.Text != nil && HashText(*f.Text) != f.SHA256 {
				return db.ImprovementProposal{}, refuse("%q's text does not match its hash", f.Path)
			}
		}
	}

	before, err := json.Marshal(n.Before)
	if err != nil {
		return db.ImprovementProposal{}, fmt.Errorf("encoding before: %w", err)
	}
	after, err := json.Marshal(n.After)
	if err != nil {
		return db.ImprovementProposal{}, fmt.Errorf("encoding after: %w", err)
	}
	marks := Marks(n.Kind, n.Before, n.After)
	slices.Sort(marks)
	if marks == nil {
		marks = []string{}
	}
	runs := n.RunIDs
	if runs == nil {
		runs = []uuid.UUID{}
	}

	return q.CreateImprovementProposal(ctx, db.CreateImprovementProposalParams{
		TeamID: n.TeamID, AgentID: n.AgentID, SkillID: n.SkillID, Kind: string(n.Kind),
		Why: n.Why, Hypothesis: n.Hypothesis, Scope: n.Scope, Signature: n.Signature,
		RunIds: runs, Outsider: n.Outsider, Marks: marks, Before: before, After: after,
		PassID: n.PassID, Goal: goal, Saves: saves,
	})
}

// goalOf is a proposal's goal and what it saves, held together: a quality
// change saves nothing, and an efficiency change names cost, time or both.
func goalOf(n Proposed) (string, []string, error) {
	switch n.Goal {
	case "", GoalQuality:
		if len(n.Saves) > 0 {
			return "", nil, refuse("only an efficiency change names what it saves")
		}
		return GoalQuality, []string{}, nil
	case GoalEfficiency:
		saves := slices.Clone(n.Saves)
		slices.Sort(saves)
		saves = slices.Compact(saves)
		if len(saves) == 0 {
			return "", nil, refuse("an efficiency change names what it saves: cost, time or both")
		}
		for _, s := range saves {
			if s != SavesCost && s != SavesTime {
				return "", nil, refuse("%q is not something a change saves: cost or time", s)
			}
		}
		return GoalEfficiency, saves, nil
	}
	return "", nil, refuse("%q is not a goal: quality or efficiency", n.Goal)
}

// AgentNow and SkillNow read a target's current state: what a proposal is
// written against.
func AgentNow(ctx context.Context, q *db.Queries, a db.Agent) (AgentState, error) {
	return agentState(ctx, q, a)
}

func SkillNow(ctx context.Context, q *db.Queries, s db.Skill) (SkillState, error) {
	return skillState(ctx, q, s)
}
