package replay

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Queued is a proposal's tries still to run, in their order.
func (r *Replays) Queued(ctx context.Context, proposalID uuid.UUID) ([]uuid.UUID, error) {
	return r.store.ListQueuedReplays(ctx, proposalID)
}

// Try runs one of a proposal's tries and writes what came of it. errStop
// says the proposal's tries are over, the rest skipped. Each read and write
// is made a few times before an error is returned; the model is run once,
// so its step is never tried again, and a try whose error comes back is
// for Fail.
func (r *Replays) Try(ctx context.Context, proposalID, teamID, runID uuid.UUID) error {
	var p db.ImprovementProposal
	if err := r.again(ctx, func() (err error) {
		p, err = r.store.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: proposalID, TeamID: teamID})
		return err
	}); err != nil {
		return fmt.Errorf("reading proposal %s: %w", proposalID, err)
	}
	if p.Status != proposalWaiting && p.Status != proposalApplied {
		return r.stop(ctx, proposalID, whyDecided)
	}
	var s db.GetReplaySettingsRow
	if err := r.again(ctx, func() (err error) {
		s, err = r.store.GetReplaySettings(ctx, teamID)
		return err
	}); err != nil {
		return fmt.Errorf("reading team %s's settings: %w", teamID, err)
	}
	if !s.Improvement || !s.ImprovementReplay {
		return r.stop(ctx, proposalID, whyOff)
	}
	var started int64
	if err := r.again(ctx, func() (err error) {
		started, err = r.store.ClaimReplay(ctx, db.StartReplayParams{
			ProposalID: proposalID, SourceRunID: runID, TeamID: teamID,
			Since: midnight(r.now()), Daily: int32(s.ImprovementReplayDaily),
		})
		return err
	}); err != nil {
		return fmt.Errorf("starting a try of proposal %s: %w", proposalID, err)
	}
	if started == 0 {
		return r.stop(ctx, proposalID, whyDailyCap)
	}

	res, err := r.replay(ctx, p, runID, s)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return r.fail(ctx, proposalID, runID, err, res.Usage)
	}
	return r.finish(ctx, p, runID, res)
}

// Fail writes a try failed, whatever it was at: one whose error Try
// returned, so it is not left trying.
func (r *Replays) Fail(ctx context.Context, proposalID, runID uuid.UUID, why error) error {
	return r.fail(ctx, proposalID, runID, why, agent.Usage{})
}

func (r *Replays) fail(ctx context.Context, proposalID, runID uuid.UUID, why error, spent agent.Usage) error {
	msg := clip(why.Error(), errorBytes)
	return r.again(ctx, func() error {
		return r.store.FailReplay(context.WithoutCancel(ctx), db.FailReplayParams{
			Error: &msg, InputTokens: int64(spent.InputTokens), OutputTokens: int64(spent.OutputTokens),
			ProposalID: proposalID, SourceRunID: runID,
		})
	})
}

// again makes f up to storeTries times, pausing between, until it succeeds
// or ctx is done.
func (r *Replays) again(ctx context.Context, f func() error) error {
	var err error
	for i := range storeTries {
		if i > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(r.pause):
			}
		}
		if err = f(); err == nil {
			return nil
		}
	}
	return err
}

func (r *Replays) stop(ctx context.Context, proposalID uuid.UUID, why string) error {
	if err := r.again(ctx, func() error {
		return r.store.SkipQueuedReplays(ctx, db.SkipQueuedReplaysParams{SkipWhy: &why, ProposalID: proposalID})
	}); err != nil {
		return fmt.Errorf("stopping proposal %s's tries: %w", proposalID, err)
	}
	return errStop
}

func midnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// replay rebuilds the past run and runs it again on the proposal's after.
func (r *Replays) replay(ctx context.Context, p db.ImprovementProposal, runID uuid.UUID, s db.GetReplaySettingsRow) (runtime.Replayed, error) {
	rows, err := r.store.ListReplayRuns(ctx, db.ListReplayRunsParams{TeamID: p.TeamID, Ids: []uuid.UUID{runID}})
	if err != nil {
		return runtime.Replayed{}, fmt.Errorf("reading run %s: %w", runID, err)
	}
	if len(rows) != 1 {
		return runtime.Replayed{}, fmt.Errorf("run %s is gone", runID)
	}
	run := rows[0].Run
	before, err := r.store.GetRunStartedSeq(ctx, runID)
	if err != nil {
		return runtime.Replayed{}, fmt.Errorf("reading where run %s began: %w", runID, err)
	}
	c, err := candidateOf(r.store, r.store, r.objects, p, run, before)
	if err != nil {
		return runtime.Replayed{}, err
	}
	msgs, err := r.transcript(ctx, run)
	if err != nil {
		return runtime.Replayed{}, err
	}
	recorded, err := r.recorded(ctx, runID)
	if err != nil {
		return runtime.Replayed{}, err
	}
	return r.runner.Replay(ctx, runtime.ReplayInput{
		Store: c, Objects: c, Run: run, Messages: msgs, Recorded: recorded,
		Limits: runtime.ReplayLimits{
			Tokens: int(s.ImprovementReplayTokens), Time: time.Duration(s.ImprovementReplayMinutes) * r.minute,
		},
	})
}

// transcript is the past run's transcript up to and including the message
// it was started with: what it began from.
func (r *Replays) transcript(ctx context.Context, run db.Run) ([]agent.Message, error) {
	msgs, err := r.sessions.Messages(ctx, *run.Transcript)
	if err != nil {
		return nil, fmt.Errorf("reading run %s's transcript: %w", run.ID, err)
	}
	from := int(*run.TranscriptFrom)
	if from >= len(msgs) {
		return nil, fmt.Errorf("run %s's first message is not in its transcript", run.ID)
	}
	return slices.Clone(msgs[:from+1]), nil
}

// recorded is what the past run's calls returned, oldest first, reading
// every page of its events.
func (r *Replays) recorded(ctx context.Context, runID uuid.UUID) ([]runtime.Recorded, error) {
	var events []db.RunEvent
	after := int32(0)
	for {
		page, err := r.store.ListRunEvents(ctx, db.ListRunEventsParams{RunID: runID, After: after, PageSize: eventsPage})
		if err != nil {
			return nil, fmt.Errorf("reading run %s's events: %w", runID, err)
		}
		events = append(events, page...)
		if len(page) < eventsPage {
			break
		}
		after = page[len(page)-1].Seq
	}
	recorded, err := runtime.RecordedOf(events)
	if err != nil {
		return nil, fmt.Errorf("reading run %s's calls: %w", runID, err)
	}
	return recorded, nil
}

// finish judges a try and writes it.
func (r *Replays) finish(ctx context.Context, p db.ImprovementProposal, runID uuid.UUID, res runtime.Replayed) error {
	var recorded, live, notMade int16
	for _, c := range res.Calls {
		switch c.How {
		case runtime.ServedRecorded:
			recorded++
		case runtime.ServedLive:
			live++
		case runtime.ServedNotMade:
			notMade++
		}
	}
	answer := clip(res.Output, answerBytes)
	row := db.FinishReplayParams{
		Diverged: notMade > 0, Recorded: recorded, Live: live, NotMade: notMade,
		InputTokens: int64(res.Usage.InputTokens), CachedInputTokens: int64(res.Usage.CachedInputTokens),
		OutputTokens: int64(res.Usage.OutputTokens),
		Steps:        int32(res.Steps), WorkMs: res.Elapsed.Milliseconds(), //nolint:gosec // a run's steps fit
		Answer: &answer, Trace: traceOf(res.Calls), ProposalID: p.ID, SourceRunID: runID,
	}
	replayed := outcomeCantTell
	if res.Over != nil {
		// Stopped short: there is no whole run to judge.
		why := "The try was stopped: " + res.Over.Error() + "."
		row.Why = &why
	} else {
		j, err := r.judge.JudgeReplay(ctx, runID, improve.Replay{
			Trace: judgeTrace(res), Answer: res.Output, Ending: ending(res),
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return r.fail(ctx, p.ID, runID, fmt.Errorf("judging the try: %w", err), res.Usage)
		}
		row.JudgeTokens = int64(j.Usage.InputTokens + j.Usage.OutputTokens)
		row.Verdict, row.Why = &j.Verdict, &j.Why
		switch improve.Effective(j.Verdict, "", 0, 0) {
		case improve.Good:
			replayed = outcomeGood
		case improve.Bad:
			replayed = outcomeBad
		case improve.Neither:
		}
	}
	row.Replayed = &replayed
	if err := r.again(ctx, func() error { return r.store.FinishReplay(context.WithoutCancel(ctx), row) }); err != nil {
		return fmt.Errorf("writing a try of proposal %s: %w", p.ID, err)
	}
	return nil
}
