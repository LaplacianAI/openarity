package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
)

const (
	replayQueue    = "openarity-replays"
	replayWorkflow = "openarity.improve.replay"
	// Tries a worker runs at once: each is a whole run on the model.
	triesPerWorker = 2
	// A step that only reads and writes the store is tried a few times on
	// its account.
	storeRetries = 3
)

// Job runs a proposal's tries in the background, one workflow a proposal.
type Job struct {
	replays *Replays
	dbos    dbos.Context
}

func NewJob(r *Replays) *Job { return &Job{replays: r} }

func (*Job) Name() string { return "replay" }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.replays == nil {
		return nil, errors.New("the replay job has nothing to replay with")
	}
	if _, err := dbos.RegisterQueue(d, replayQueue, dbos.WithWorkerConcurrency(triesPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the replay queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.replay, dbos.WithWorkflowName(replayWorkflow))
	j.dbos = d
	return nil, nil
}

// Enqueue has a stored proposal tried, once: its workflow id is the
// proposal's, so a second enqueue is the same workflow.
func (j *Job) Enqueue(ctx context.Context, proposalID, teamID uuid.UUID) error {
	if j.dbos == nil {
		return errors.New("the replay job is not registered")
	}
	_, err := dbos.Enqueue[string](j.dbos, replayQueue, replayWorkflow, input(proposalID, teamID),
		dbos.WithEnqueueWorkflowID("replay:"+proposalID.String()))
	if err != nil {
		return fmt.Errorf("enqueuing proposal %s's tries: %w", proposalID, err)
	}
	return nil
}

func input(proposalID, teamID uuid.UUID) string { return proposalID.String() + ":" + teamID.String() }

func parseInput(s string) (proposalID, teamID uuid.UUID, err error) {
	p, t, ok := strings.Cut(s, ":")
	if !ok {
		return uuid.Nil, uuid.Nil, fmt.Errorf("replay input %q is not proposal:team", s)
	}
	if proposalID, err = uuid.Parse(p); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	teamID, err = uuid.Parse(t)
	return proposalID, teamID, err
}

func storeStep(name string) []dbos.StepOption {
	return []dbos.StepOption{
		dbos.WithStepName(name), dbos.WithStepMaxRetries(storeRetries),
		dbos.WithStepRetryPredicate(func(err error) bool { return !errors.Is(err, context.Canceled) && !Stopped(err) }),
	}
}

func (j *Job) replay(ctx dbos.Context, in string) (string, error) {
	proposalID, teamID, err := parseInput(in)
	if err != nil {
		return in, err
	}
	if _, err := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in, j.replays.Choose(sctx, proposalID, teamID)
	}, storeStep("choose")...); err != nil {
		return in, err
	}
	runs, err := dbos.RunAsStep(ctx, func(sctx context.Context) ([]uuid.UUID, error) {
		return j.replays.Queued(sctx, proposalID)
	}, storeStep("queued")...)
	if err != nil {
		return in, err
	}
	for _, runID := range runs {
		// Run once: a try is a whole run on the model, and Try makes its own
		// reads and writes again.
		_, err := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
			return runID.String(), j.replays.Try(sctx, proposalID, teamID, runID)
		}, dbos.WithStepName("try "+runID.String()))
		if Stopped(err) {
			return in, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				// The worker is stopping: the try is picked up again.
				return in, err
			}
			// Not left trying: the try failed, and the next is tried. Fail
			// makes its own write again, so the step is run once.
			if _, ferr := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
				return runID.String(), j.replays.Fail(sctx, proposalID, runID, err)
			}, dbos.WithStepName("fail "+runID.String())); ferr != nil {
				return in, errors.Join(err, ferr)
			}
		}
	}
	return in, nil
}
