package replay

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Choose picks the past runs a proposal is tried on and writes a row for
// each, queued; a cited run that cannot be tried is written skipped, with
// why. Nothing is chosen for a proposal no longer waiting, or a team with
// replay off; the first is marked chosen, with none.
func (r *Replays) Choose(ctx context.Context, proposalID, teamID uuid.UUID) error {
	p, err := r.store.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: proposalID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("reading proposal %s: %w", proposalID, err)
	}
	s, err := r.store.GetReplaySettings(ctx, teamID)
	if err != nil {
		return fmt.Errorf("reading team %s's settings: %w", teamID, err)
	}
	if !s.Improvement || !s.ImprovementReplay {
		return nil
	}
	if p.Status != proposalWaiting {
		return r.markChosen(ctx, p)
	}
	n := int(s.ImprovementReplayRuns)

	cited, err := r.store.ListReplayRuns(ctx, db.ListReplayRunsParams{TeamID: teamID, Ids: p.RunIds})
	if err != nil {
		return fmt.Errorf("reading the runs proposal %s cites: %w", proposalID, err)
	}
	newestFirst(cited)
	good, err := r.goodRuns(ctx, p, n)
	if err != nil {
		return err
	}
	picks, skipped := pick(cited, good, p.AgentID, n)
	for i, c := range picks {
		if err := r.store.InsertReplay(ctx, db.InsertReplayParams{
			ProposalID: p.ID, TeamID: teamID, SourceRunID: c.run.Run.ID, Position: int16(i), //nolint:gosec // at most ten
			Chosen: c.chosen, Original: c.original, Status: statusQueued,
		}); err != nil {
			return fmt.Errorf("choosing run %s: %w", c.run.Run.ID, err)
		}
	}
	for i, c := range skipped {
		why := c.skip
		if err := r.store.InsertReplay(ctx, db.InsertReplayParams{
			ProposalID: p.ID, TeamID: teamID, SourceRunID: c.run.Run.ID, Position: int16(len(picks) + i), //nolint:gosec // at most the cited runs
			Chosen: c.chosen, Original: c.original, Status: statusSkipped, SkipWhy: &why,
		}); err != nil {
			return fmt.Errorf("noting run %s as not tried: %w", c.run.Run.ID, err)
		}
	}
	return r.markChosen(ctx, p)
}

// markChosen says choosing ran with trying on, last: a proposal it marks
// with no tries is one none of whose runs could be tried.
func (r *Replays) markChosen(ctx context.Context, p db.ImprovementProposal) error {
	if err := r.store.MarkReplayChosen(ctx, db.MarkReplayChosenParams{ID: p.ID, TeamID: p.TeamID}); err != nil {
		return fmt.Errorf("marking proposal %s's runs chosen: %w", p.ID, err)
	}
	return nil
}

func (r *Replays) goodRuns(ctx context.Context, p db.ImprovementProposal, n int) ([]db.ListReplayRunsRow, error) {
	agentID := p.AgentID
	// More than are needed: some may not be replayable.
	ids, err := r.store.ListReplayGoodRunIDs(ctx, db.ListReplayGoodRunIDsParams{
		TeamID: p.TeamID, AgentID: &agentID, Since: r.now().Add(-goodWithin), Exclude: p.RunIds, PageSize: int32(n * 3), //nolint:gosec // at most thirty
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err := r.store.ListReplayRuns(ctx, db.ListReplayRunsParams{TeamID: p.TeamID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("reading the agent's good runs: %w", err)
	}
	newestFirst(rows)
	return rows, nil
}

func newestFirst(rows []db.ListReplayRunsRow) {
	slices.SortStableFunc(rows, func(a, b db.ListReplayRunsRow) int { return b.Run.CreatedAt.Compare(a.Run.CreatedAt) })
}

type choice struct {
	run      db.ListReplayRunsRow
	chosen   string
	original string
	skip     string
}

// pick is up to n runs: half from those the proposal cites, the rest from
// recent good ones, either half filling what the other lacks, newest first.
// A cited run that cannot be tried comes back among the skipped, with why;
// one whose outcome could not be told is left out, as there is nothing to
// compare a try with.
func pick(cited, good []db.ListReplayRunsRow, agentID uuid.UUID, n int) (picks, skipped []choice) {
	var okCited, okGood []choice
	for _, row := range cited {
		c := choice{run: row, chosen: chosenCited, original: originalOf(row)}
		if c.original == "" {
			continue
		}
		if c.skip = unreplayable(row, agentID); c.skip != "" {
			skipped = append(skipped, c)
			continue
		}
		okCited = append(okCited, c)
	}
	for _, row := range good {
		c := choice{run: row, chosen: chosenGood, original: originalOf(row)}
		if c.original == outcomeGood && unreplayable(row, agentID) == "" {
			okGood = append(okGood, c)
		}
	}
	half := (n + 1) / 2
	takeCited := min(len(okCited), half)
	takeGood := min(len(okGood), n-takeCited)
	takeCited = min(len(okCited), n-takeGood)
	picks = append(picks, okCited[:takeCited]...)
	picks = append(picks, okGood[:takeGood]...)
	return picks, skipped
}

// originalOf is a past run's outcome as I2's Effective has it: "" when it
// could not be told.
func originalOf(row db.ListReplayRunsRow) string {
	switch improve.Effective(row.Verdict, "", row.Up, row.Down) {
	case improve.Good:
		return outcomeGood
	case improve.Bad:
		return outcomeBad
	case improve.Neither:
	}
	return ""
}

// unreplayable is why a past run cannot be tried again as it was; "" when
// it can.
func unreplayable(row db.ListReplayRunsRow, agentID uuid.UUID) string {
	run := row.Run
	switch {
	case run.ParentRunID != nil:
		return "it was a run another run handed work to"
	case run.Hub || run.Builder:
		return "it ran as the team's hub or the builder, not as this agent"
	case run.AgentID == nil || *run.AgentID != agentID:
		return "it was another agent's run"
	case run.Status != "succeeded" && run.Status != "failed" && run.Status != "cancelled":
		return "it has not finished"
	case run.Transcript == nil || run.TranscriptFrom == nil:
		return "its conversation is not kept as it began"
	case row.Clipped:
		return "one of its calls was too long to keep whole, so it cannot be served back as it was"
	case row.Compacted:
		return "its conversation was compacted since it began, so it cannot be rebuilt as it was"
	}
	return ""
}
