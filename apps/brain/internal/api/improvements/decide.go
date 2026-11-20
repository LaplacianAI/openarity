package improvements

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/skills"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Store is the database a decision is made in.
type Store interface {
	skills.Reserver
	InTx(ctx context.Context, fn func(*db.Queries) error) error
}

// Objects stores and reads a skill's file bytes.
type Objects interface {
	skills.Objects
	Getter
}

// Decider applies, rejects and reverts proposals.
type Decider struct {
	store   Store
	objects Objects
	loops   []string
}

func NewDecider(s Store, o Objects, loops []string) Decider {
	return Decider{store: s, objects: o, loops: loops}
}

// errState is a decision the proposal's status does not allow.
type errState struct{ status string }

func (e errState) Error() string { return "the proposal is " + e.status }

const (
	actionApprove = "approve"
	actionReject  = "reject"
	actionRevert  = "revert"
)

// Proposal is a stored proposal's snapshots, decoded.
type Proposal struct {
	Row    db.ImprovementProposal
	Before Snapshot
	After  Snapshot
}

func decode(row db.ImprovementProposal) (Proposal, error) {
	p := Proposal{Row: row}
	if err := json.Unmarshal(row.Before, &p.Before); err != nil {
		return Proposal{}, fmt.Errorf("reading the proposal's before: %w", err)
	}
	if err := json.Unmarshal(row.After, &p.After); err != nil {
		return Proposal{}, fmt.Errorf("reading the proposal's after: %w", err)
	}
	return p, nil
}

// Decide makes one decision on one proposal, as the person by.
func (d Decider) Decide(ctx context.Context, teamID, id, by uuid.UUID, action string) (db.ImprovementProposal, error) {
	// A skill's bytes are stored before the transaction, as the skills API
	// does: what never commits is the reaper's to remove.
	var keys []string
	var dir *skill.Directory
	if action != actionReject {
		var err error
		if dir, keys, err = d.prepareSkill(ctx, teamID, id, action); err != nil {
			return db.ImprovementProposal{}, err
		}
	}

	var out db.ImprovementProposal
	err := d.store.InTx(ctx, func(q *db.Queries) error {
		// The target is held before the proposal. Approving withdraws the other
		// proposals on the same target, so two approvals that each held their
		// own proposal and then waited on the target would hold what the other
		// needs. In this order the second waits on the target holding nothing,
		// and then finds its proposal withdrawn.
		if action != actionReject {
			if err := lockTarget(ctx, q, teamID, id); err != nil {
				return err
			}
		}
		row, err := q.LockImprovementProposal(ctx, db.LockImprovementProposalParams{ID: id, TeamID: teamID})
		if err != nil {
			return err
		}
		p, err := decode(row)
		if err != nil {
			return err
		}

		switch action {
		case actionReject:
			if row.Status != "waiting" {
				return errState{row.Status}
			}
			out, err = q.DecideImprovementProposal(ctx, db.DecideImprovementProposalParams{ID: id, Status: "rejected", By: &by})
			return err

		case actionApprove:
			if row.Status != "waiting" {
				return errState{row.Status}
			}
			if err := Check(Kind(row.Kind), p.Before, p.After); err != nil {
				return err
			}
			if err := d.apply(ctx, q, p, dir, keys); err != nil {
				return err
			}
			if out, err = q.DecideImprovementProposal(ctx, db.DecideImprovementProposalParams{ID: id, Status: "applied", By: &by}); err != nil {
				return err
			}
			return q.WithdrawImprovementProposals(ctx, db.WithdrawImprovementProposalsParams{
				AppliedID: &id, TeamID: teamID, AgentID: row.AgentID, SkillID: row.SkillID,
			})

		case actionRevert:
			if row.Status != "applied" {
				return errState{row.Status}
			}
			if err := d.revert(ctx, q, p, dir, keys); err != nil {
				return err
			}
			out, err = q.RevertImprovementProposal(ctx, db.RevertImprovementProposalParams{ID: id, By: &by})
			return err
		}
		return fmt.Errorf("unknown action %q", action)
	})
	return out, err
}

// statusFor is the status a proposal must be in for the action.
func statusFor(action string) string {
	if action == actionRevert {
		return "applied"
	}
	return "waiting"
}

// lockTarget holds the agent or skill a proposal changes: the skill for a
// skill_edit, the agent for everything else.
func lockTarget(ctx context.Context, q *db.Queries, teamID, id uuid.UUID) error {
	row, err := q.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: id, TeamID: teamID})
	if err != nil {
		return err
	}
	if Kind(row.Kind) == KindSkillEdit && row.SkillID != nil {
		_, err = q.LockSkill(ctx, db.LockSkillParams{ID: *row.SkillID, TeamID: teamID})
	} else {
		_, err = q.LockAgent(ctx, db.LockAgentParams{ID: row.AgentID, TeamID: teamID})
	}
	return mapChanged(err)
}

// prepareSkill builds and stores the skill a decision will write, outside
// the transaction. The transaction then checks the skill is still what this
// was built from, so a read here that races an edit is refused there.
func (d Decider) prepareSkill(ctx context.Context, teamID, id uuid.UUID, action string) (*skill.Directory, []string, error) {
	var p Proposal
	var stored []db.SkillFile
	err := d.store.InTx(ctx, func(q *db.Queries) error {
		row, err := q.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: id, TeamID: teamID})
		if err != nil {
			return err
		}
		if p, err = decode(row); err != nil {
			return err
		}
		// A decision the status refuses stores nothing for the reaper to clear;
		// the transaction asks again, on the locked row.
		if want := statusFor(action); row.Status != want {
			return errState{row.Status}
		}
		if row.SkillID != nil {
			if stored, err = q.ListSkillFiles(ctx, *row.SkillID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	var target *SkillState
	switch {
	case Kind(p.Row.Kind) == KindSkillEdit && action == actionApprove:
		target = p.After.Skill
	case Kind(p.Row.Kind) == KindSkillEdit && action == actionRevert:
		target = p.Before.Skill
	case Kind(p.Row.Kind) == KindSkillNew && action == actionApprove:
		target = p.After.Skill
	default:
		return nil, nil, nil
	}
	if target == nil {
		return nil, nil, refuse("the proposal carries no skill")
	}

	dir, err := directory(ctx, d.objects, teamID, *target, stored)
	if err != nil {
		return nil, nil, err
	}
	keys, err := skills.Reserve(ctx, d.store, d.objects, teamID, dir.Files)
	if err != nil {
		return nil, nil, err
	}
	return &dir, keys, nil
}

func (d Decider) apply(ctx context.Context, q *db.Queries, p Proposal, dir *skill.Directory, keys []string) error {
	row := p.Row
	switch Kind(row.Kind) {
	case KindSkillEdit:
		return d.writeSkill(ctx, q, row, *p.Before.Skill, dir, keys)

	case KindSkillNew:
		agent, err := d.lockAgent(ctx, q, row, *p.Before.Agent)
		if err != nil {
			return err
		}
		created, err := skills.Create(ctx, q, row.TeamID, *dir, skills.Origin{Source: "upload"}, keys)
		if err != nil {
			return mapWrite(err)
		}
		after := *p.After.Agent
		after.Agent.Skills = append(append([]uuid.UUID{}, after.Agent.Skills...), created.ID)
		if err := writeAgent(ctx, q, agent, after, d.loops); err != nil {
			return mapWrite(err)
		}
		// The revert needs the agent as it now is, and which skill to delete.
		snap := p.After
		snap.Agent = &after
		snap.CreatedSkill = &created.ID
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		return mapWrite(q.SetImprovementProposalAfter(ctx, db.SetImprovementProposalAfterParams{ID: row.ID, After: raw}))

	case KindInstructions, KindDescription, KindSkillGrant, KindSkillDrop, KindServerGrant, KindServerDrop,
		KindToolsNarrow, KindBuiltinTool, KindToolNote, KindLimits, KindDelegation, KindPattern:
		agent, err := d.lockAgent(ctx, q, row, *p.Before.Agent)
		if err != nil {
			return err
		}
		return mapWrite(writeAgent(ctx, q, agent, *p.After.Agent, d.loops))
	}
	return fmt.Errorf("unknown kind %q", row.Kind)
}

func (d Decider) revert(ctx context.Context, q *db.Queries, p Proposal, dir *skill.Directory, keys []string) error {
	row := p.Row
	switch Kind(row.Kind) {
	case KindSkillEdit:
		return d.writeSkill(ctx, q, row, *p.After.Skill, dir, keys)

	case KindSkillNew:
		agent, err := d.lockAgent(ctx, q, row, *p.After.Agent)
		if err != nil {
			return err
		}
		if p.After.CreatedSkill == nil {
			return errors.New("an applied skill_new without the skill it created")
		}
		made, err := q.LockSkill(ctx, db.LockSkillParams{ID: *p.After.CreatedSkill, TeamID: row.TeamID})
		if err != nil {
			return mapChanged(err)
		}
		now, err := skillState(ctx, q, made)
		if err != nil {
			return err
		}
		if !sameSkill(now, *p.After.Skill) {
			return errChanged
		}
		if err := writeAgent(ctx, q, agent, *p.Before.Agent, d.loops); err != nil {
			return mapWrite(err)
		}
		if err := q.DeleteSkill(ctx, made.ID); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23001" {
				return refuse("other agents hold %s now; ungrant it from them first", made.Name)
			}
			return err
		}
		return nil

	case KindInstructions, KindDescription, KindSkillGrant, KindSkillDrop, KindServerGrant, KindServerDrop,
		KindToolsNarrow, KindBuiltinTool, KindToolNote, KindLimits, KindDelegation, KindPattern:
		agent, err := d.lockAgent(ctx, q, row, *p.After.Agent)
		if err != nil {
			return err
		}
		return mapWrite(writeAgent(ctx, q, agent, *p.Before.Agent, d.loops))
	}
	return fmt.Errorf("unknown kind %q", row.Kind)
}

// lockAgent holds the proposal's agent and checks it is still expect.
func (d Decider) lockAgent(ctx context.Context, q *db.Queries, row db.ImprovementProposal, expect AgentState) (db.Agent, error) {
	agent, err := q.LockAgent(ctx, db.LockAgentParams{ID: row.AgentID, TeamID: row.TeamID})
	if err != nil {
		return db.Agent{}, mapChanged(err)
	}
	now, err := agentState(ctx, q, agent)
	if err != nil {
		return db.Agent{}, err
	}
	if !sameAgent(now, expect) {
		return db.Agent{}, errChanged
	}
	return agent, nil
}

// writeSkill holds the skill, checks it is still expect, and writes dir.
func (d Decider) writeSkill(ctx context.Context, q *db.Queries, row db.ImprovementProposal, expect SkillState, dir *skill.Directory, keys []string) error {
	if row.SkillID == nil || dir == nil {
		return errors.New("a skill_edit without its skill")
	}
	target, err := q.LockSkill(ctx, db.LockSkillParams{ID: *row.SkillID, TeamID: row.TeamID})
	if err != nil {
		return mapChanged(err)
	}
	now, err := skillState(ctx, q, target)
	if err != nil {
		return err
	}
	if !sameSkill(now, expect) {
		return errChanged
	}
	_, err = skills.Replace(ctx, q, target.ID, *dir, skills.Origin{Source: "upload"}, keys)
	return mapWrite(err)
}

func mapChanged(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errChanged
	}
	return err
}

// mapWrite says a write the database refused as a broken rule: a grant of
// something deleted since, a name taken, a delegate gone.
func mapWrite(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23503":
			return refuse("something it grants or names no longer exists in this team")
		case "23505":
			return refuse("a skill with that name already exists")
		case "23514":
			return refuse("the database refused the change (%s)", pg.ConstraintName)
		}
	}
	return err
}

// hashText is the sha256 a file's text is named by.
func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
