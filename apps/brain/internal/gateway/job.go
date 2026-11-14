package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
)

const (
	replyQueue       = "openarity-replies"
	replyWorkflow    = "openarity.gateway.reply"
	repliesPerWorker = 4

	// The sweep sends what a kick did not: a retry that came due, or a reply
	// whose kick was lost. Every minute, so a retry is late by at most that.
	sweepRepliesWorkflow = "openarity.gateway.replies-sweep"
	replySweepSchedule   = "gateway-replies-sweep"
	replySweepCron       = "30 * * * * *"
)

type replyJob struct {
	sender *Sender
}

// ReplyJob sends the outbox's replies: when a run's finish kicks it, and on a
// sweep every minute for everything else.
func ReplyJob(s *Sender) replyJob { return replyJob{sender: s} }

func (replyJob) Name() string { return "gateway.replies" }

func (j replyJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.sender == nil {
		return nil, errors.New("the reply job has no sender, so every reply would wait forever")
	}

	if _, err := dbos.RegisterQueue(d, replyQueue, dbos.WithWorkerConcurrency(repliesPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the reply queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.kicked, dbos.WithWorkflowName(replyWorkflow))
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(sweepRepliesWorkflow))
	return j.schedules(), nil
}

// No backfill: a missed tick has no work of its own, and the next one sends
// everything that came due in between.
func (replyJob) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{
		ScheduleName:      replySweepSchedule,
		Schedule:          replySweepCron,
		WorkflowName:      sweepRepliesWorkflow,
		AutomaticBackfill: false,
	}}
}

// kicked drains everything due, not only what kicked it: key is the run or
// the notice that asked, so the workflow id keeps a double kick to one send.
func (j replyJob) kicked(ctx dbos.Context, key string) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		_, err := j.sender.Drain(sctx)
		return key, err
	})
}

func (j replyJob) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		_, err := j.sender.Drain(sctx)
		return in.ScheduledTime.UTC().Format(time.RFC3339), err
	})
}

// KickReplies has the replies due now sent at once, rather than at the next
// sweep. The workflow id is the run's, so a finish that kicks twice sends once.
func KickReplies(c dbos.Client, runID uuid.UUID) error {
	_, err := dbos.Enqueue[string](c, replyQueue, replyWorkflow, runID.String(),
		dbos.WithEnqueueWorkflowID("reply:"+runID.String()))
	if err != nil {
		return fmt.Errorf("kicking the reply to run %s: %w", runID, err)
	}
	return nil
}

// KickEvent has one step of a run sent at once. The workflow id is the run
// event's, so each step is its own kick and a step kicked twice sends once.
func KickEvent(c dbos.Client, runID uuid.UUID, seq int32) error {
	key := "reply:" + runID.String() + ":" + strconv.Itoa(int(seq))
	if _, err := dbos.Enqueue[string](c, replyQueue, replyWorkflow, key, dbos.WithEnqueueWorkflowID(key)); err != nil {
		return fmt.Errorf("kicking step %d of run %s: %w", seq, runID, err)
	}
	return nil
}

// NoticeKicker has a notice sent at once, through the same queue and workflow
// as a reply, rather than at the next sweep.
type NoticeKicker struct{ c dbos.Client }

func NewNoticeKicker(c dbos.Client) NoticeKicker { return NoticeKicker{c: c} }

func (k NoticeKicker) KickNotice(_ context.Context, noticeID int64) error {
	key := "notice:" + strconv.FormatInt(noticeID, 10)
	if _, err := dbos.Enqueue[string](k.c, replyQueue, replyWorkflow, key, dbos.WithEnqueueWorkflowID(key)); err != nil {
		return fmt.Errorf("kicking notice %d: %w", noticeID, err)
	}
	return nil
}
