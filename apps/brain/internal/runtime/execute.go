package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

type Store interface {
	EventStore
	BuildStore
	DelegateStore
	ClaimRun(ctx context.Context, arg db.ClaimRunParams) (db.ClaimRunRow, error)
	HeartbeatRun(ctx context.Context, arg db.HeartbeatRunParams) (*time.Time, error)
	FinishRun(ctx context.Context, arg db.FinishRunParams) (int64, error)
	FinishRunWithEvent(ctx context.Context, finish db.FinishRunParams, event db.AppendRunEventParams) (int64, uuid.UUID, error)
	ListMessagesSinceLastRun(ctx context.Context, arg db.ListMessagesSinceLastRunParams) ([]db.ListMessagesSinceLastRunRow, error)
	SetTranscriptFrom(ctx context.Context, arg db.SetTranscriptFromParams) error
	StartChildRun(ctx context.Context, arg db.StartChildRunParams) (db.Run, error)
	ReapChildren(ctx context.Context, arg db.ReapChildrenParams) ([]uuid.UUID, error)
	GetRun(ctx context.Context, id uuid.UUID) (db.Run, error)
}

const (
	heartbeatEvery     = 15 * time.Second
	heartbeatLostAfter = 60 * time.Second
	maxWallClock       = 6 * time.Hour
	finishTimeout      = 10 * time.Second
)

var (
	ErrCancelled = errors.New("the run was cancelled")
	errWallClock = fmt.Errorf("the run ran past its wall clock of %s", maxWallClock)
)

const (
	reapReason  = "its parent was resumed after the worker stopped; the work was asked for again"
	endedReason = "its parent ended before it did"
)

type Lineage struct {
	Worker string
	Chain  []uuid.UUID
	Narrow *Narrowing
}

type Delegation struct {
	Parent  db.Run
	Agent   db.Agent
	Brief   string
	CallID  string
	Lineage Lineage
	// Budget is the child's token budget: its agent's, cut to what the tree has left.
	Budget int64
}

type Executor struct {
	store   Store
	builder *Builder
	runner  *agent.Runner
	// streamer runs the runs a person is waiting on, so their text reaches
	// them as it is written. Nil, and they run as any other.
	streamer  *agent.Runner
	endpoint  agent.Endpoint
	sessions  agent.Store
	logger    *slog.Logger
	every     time.Duration
	lostAfter time.Duration

	// enqueue hands a run this executor started to the workers. Nil leaves it
	// to the sweep, which starts any queued run within a minute.
	enqueue func(uuid.UUID) error

	// kickReply has a channel run's reply sent now. Nil leaves it to the
	// reply sweep, a minute at most.
	kickReply func(uuid.UUID) error

	// notes holds, for each run executing here, the channel its recorder takes
	// the brain's own events on, so they are written in order with the agent's.
	notes sync.Map
}

func NewExecutor(s Store, b *Builder, r *agent.Runner, endpoint agent.Endpoint,
	sessions agent.Store, logger *slog.Logger,
) *Executor {
	e := &Executor{
		store: s, builder: b, runner: r, endpoint: endpoint,
		sessions: sessions, logger: logger, every: heartbeatEvery,
		lostAfter: heartbeatLostAfter,
	}
	b.delegates = &DelegateDeps{Store: s, Run: e}
	b.note = e.note
	return e
}

// WithStreaming has builder runs run on r, whose loops stream the model's
// text, recorded as it comes as text events.
func (e *Executor) WithStreaming(r *agent.Runner) *Executor {
	e.streamer = r
	return e
}

type startedPayload struct {
	Agent   string   `json:"agent"`
	Model   string   `json:"model"`
	Pattern string   `json:"pattern"`
	Tools   []string `json:"tools"`
	Skills  []string `json:"skills"`
}

type resumedPayload struct {
	Messages int `json:"messages"`
}

type unavailablePayload struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type finishedPayload struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type delegatedPayload struct {
	CallID string    `json:"call_id"`
	Agent  string    `json:"agent"`
	RunID  uuid.UUID `json:"run_id"`
	Depth  int32     `json:"depth"`
}

func (e *Executor) Execute(ctx context.Context, runID uuid.UUID, worker string) error {
	row, err := e.store.ClaimRun(ctx, db.ClaimRunParams{
		ID: runID, Worker: worker, WallClockSeconds: maxWallClock.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("claiming run %s: %w", runID, err)
	}

	remaining := time.Duration(row.RemainingSeconds * float64(time.Second))
	bounded, cancelClock := context.WithTimeoutCause(ctx, remaining, errWallClock)
	defer cancelClock()

	return e.hold(bounded, runOf(row), Lineage{Worker: worker, Chain: chainOf(row.AgentID)})
}

func (e *Executor) Delegate(ctx context.Context, d Delegation) (db.Run, error) {
	id := uuid.New()
	child, err := e.store.StartChildRun(ctx, db.StartChildRunParams{
		ID: id, ParentID: d.Parent.ID, AgentID: d.Agent.ID, TeamID: d.Parent.TeamID,
		ParentToolCall: d.CallID + "#" + id.String(), Input: d.Brief, Worker: d.Lineage.Worker,
		TokenBudget: d.Budget,
	})
	if err != nil {
		return db.Run{}, fmt.Errorf("starting %s under run %s: %w", d.Agent.Name, d.Parent.ID, err)
	}

	err = e.note(ctx, d.Parent.ID, d.Lineage.Worker, "delegated", delegatedPayload{
		CallID: d.CallID, Agent: child.AgentName, RunID: child.ID, Depth: child.Depth,
	})
	if err != nil {
		failed := e.finish(ctx, child.ID, d.Lineage.Worker, "failed", "", "its parent could not record it: "+err.Error())
		return db.Run{}, errors.Join(err, failed)
	}

	l := Lineage{Worker: d.Lineage.Worker, Chain: append(slices.Clone(d.Lineage.Chain), d.Agent.ID)}
	if err := e.hold(ctx, child, l); err != nil {
		return db.Run{}, err
	}
	return e.store.GetRun(context.WithoutCancel(ctx), child.ID)
}

func (e *Executor) hold(ctx context.Context, row db.Run, l Lineage) error {
	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	beatCtx, stopBeat := context.WithCancel(runCtx)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		e.heartbeat(beatCtx, row.ID, l.Worker, stop)
	}()

	output, summary, runErr := e.run(runCtx, row, l, stop)

	stopBeat()
	<-beating

	if ctx.Err() != nil && !ours(context.Cause(ctx)) {
		return fmt.Errorf("the worker stopped while running %s: %w", row.ID, context.Cause(ctx))
	}

	cause := context.Cause(runCtx)
	if errors.Is(cause, ErrLostRun) {
		return nil
	}
	status, failure := outcome(runErr, cause)
	switch {
	case status == "succeeded":
	case stopped(cause) && summary != "":
		// What it had, rather than nothing: a summary says what was found and
		// what was left, which the last few words before the cut rarely do.
		output = summary
	case stopped(cause):
	default:
		output = ""
	}
	if err := e.finish(ctx, row.ID, l.Worker, status, output, failure); err != nil {
		return err
	}
	if row.ReplyToChannel && status != "cancelled" && e.kickReply != nil {
		if err := e.kickReply(row.ID); err != nil {
			e.logger.WarnContext(ctx, "reply not kicked; the sweep will send it", "run", row.ID, "err", err)
		}
	}
	return nil
}

func ours(cause error) bool {
	return errors.Is(cause, ErrCancelled) || errors.Is(cause, ErrLostRun) ||
		errors.Is(cause, ErrBudget) || errors.Is(cause, errWallClock)
}

func chainOf(agentID *uuid.UUID) []uuid.UUID {
	if agentID == nil {
		return nil
	}
	return []uuid.UUID{*agentID}
}

func (e *Executor) run(ctx context.Context, row db.Run, l Lineage,
	stop context.CancelCauseFunc,
) (output, summary string, err error) {
	session, err := agent.Open(transcriptOf(row), e.sessions)
	if err != nil {
		return "", "", err
	}
	msgs, err := session.Messages(ctx)
	if err != nil {
		return "", "", fmt.Errorf("reading the transcript: %w", err)
	}

	built, err := e.builder.Build(ctx, row, session, l)
	if err != nil {
		return "", "", err
	}
	defer built.Close()
	built.Spec = forChannel(row, built.Spec)

	resuming, msgs, err := e.begin(ctx, row, msgs)
	if err != nil {
		return "", "", err
	}
	if resuming {
		if _, err := e.store.ReapChildren(ctx, db.ReapChildrenParams{ParentID: row.ID, Reason: reapReason}); err != nil {
			return "", "", fmt.Errorf("reaping what the run started before its worker stopped: %w", err)
		}
		err = appendEvent(ctx, e.store, row.ID, l.Worker, "resumed", resumedPayload{Messages: len(msgs)})
	} else {
		err = appendEvent(ctx, e.store, row.ID, l.Worker, "started", startedOf(built.Agent, built.Spec))
	}
	if err != nil {
		return "", "", err
	}

	for _, u := range built.Unavailable {
		if err := appendEvent(ctx, e.store, row.ID, l.Worker, "tool_unavailable",
			unavailablePayload(u)); err != nil {
			return "", "", err
		}
	}

	// Unbuffered, so a tool runs only once the recorder holds its call, and a
	// note written from inside the tool lands after it.
	events := make(chan agent.Event)
	notes := make(chan note)
	e.notes.Store(row.ID, notes)
	defer e.notes.Delete(row.ID)
	runner, streams := e.runner, false
	if e.streamer != nil && streamsText(row) {
		runner, streams = e.streamer, true
	}
	lines := newSoftLines(budgetOf(row.TokenBudget), int64(built.Spec.Model.MaxTokens))
	recorded := record(context.WithoutCancel(ctx), e.store, row.ID, l.Worker, lines,
		streams, events, notes, stop)
	run := runner.Start(ctx, built.Spec, msgs, e.endpoint, events)
	lines.hold(run)
	if until, ok := ctx.Deadline(); ok {
		clock := time.AfterFunc(time.Until(until)-timeWrapBefore, func() {
			if lines.wrap(timeWrapUp) {
				_ = e.note(ctx, row.ID, l.Worker, "wrap_up", wrapUpPayload{Reason: "time"})
			}
		})
		defer clock.Stop()
	}
	result, err := run.Wait()
	close(events)
	<-recorded

	return result.Output, run.Summary(), err
}

// note writes one of the brain's own events through the run's recorder, its
// only writer while it runs: two writers would race for the next seq, and a
// delegated event could land before the call it belongs to. A run not
// executing here has no recorder, and is written to directly.
func (e *Executor) note(ctx context.Context, runID uuid.UUID, worker, kind string, payload any) error {
	v, _ := e.notes.Load(runID)
	notes, ok := v.(chan note)
	if !ok {
		return appendEvent(ctx, e.store, runID, worker, kind, payload)
	}
	n := note{kind: kind, payload: payload, done: make(chan error, 1)}
	select {
	case notes <- n:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	return <-n.done
}

func (e *Executor) heartbeat(ctx context.Context, runID uuid.UUID, worker string, stop context.CancelCauseFunc) {
	tick := time.NewTicker(e.every)
	defer tick.Stop()
	beaten := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		cancelled, err := e.store.HeartbeatRun(ctx, db.HeartbeatRunParams{ID: runID, Worker: worker})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			stop(ErrLostRun)
			return
		case err != nil && time.Since(beaten) >= e.lostAfter:
			e.logger.Error("Heartbeat lost; stopping before another worker takes the run over",
				"run", runID, "last_beat", beaten, "err", err)
			stop(ErrLostRun)
			return
		case err != nil:
			e.logger.Warn("heartbeat failed", "run", runID, "err", err)
		case cancelled != nil:
			stop(ErrCancelled)
			return
		default:
			beaten = time.Now()
		}
	}
}

func outcome(runErr, cause error) (string, string) {
	var unreachable *url.Error
	var answered *agent.ModelError
	switch {
	case errors.Is(cause, ErrCancelled):
		return "cancelled", ""
	case cause != nil:
		return "failed", cause.Error()
	case runErr == nil:
		return "succeeded", ""
	case errors.As(runErr, &unreachable):
		return "failed", fmt.Sprintf("the model endpoint could not be reached: %v", unreachable.Err)
	case errors.As(runErr, &answered):
		return "failed", answered.Error()
	default:
		return "failed", runErr.Error()
	}
}

func (e *Executor) finish(ctx context.Context, runID uuid.UUID, worker, status, output, failure string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()

	// Postgres text cannot hold a NUL, and a run whose finish fails stays running.
	output = strings.ReplaceAll(output, "\x00", "\uFFFD")
	failure = strings.ReplaceAll(failure, "\x00", "\uFFFD")
	p := db.FinishRunParams{ID: runID, Worker: worker, Status: status}
	// A run stopped at a limit keeps what it had; hold decides which.
	if status == "succeeded" || output != "" {
		p.Output = &output
	}
	if failure != "" {
		p.Error = &failure
	}

	said, _ := clip(failure)
	raw, err := json.Marshal(finishedPayload{Status: status, Error: said})
	if err != nil {
		return fmt.Errorf("encoding the finished event: %w", err)
	}

	n, followUp, err := e.store.FinishRunWithEvent(ctx, p, db.AppendRunEventParams{
		RunID: runID, Kind: "finished", Payload: raw, Worker: worker,
	})
	if err != nil {
		return fmt.Errorf("finishing run %s as %s: %w", runID, status, err)
	}
	if n == 0 {
		return nil
	}

	if _, err := e.store.ReapChildren(ctx, db.ReapChildrenParams{ParentID: runID, Reason: endedReason}); err != nil {
		return fmt.Errorf("ending what run %s left running: %w", runID, err)
	}
	if followUp != uuid.Nil && e.enqueue != nil {
		if err := e.enqueue(followUp); err != nil {
			e.logger.WarnContext(ctx, "run not enqueued; the sweep will start it", "run", followUp, "err", err)
		}
	}
	return nil
}

func startedOf(agentName string, spec agent.Spec) startedPayload {
	p := startedPayload{
		Agent: agentName, Model: spec.Model.Name, Pattern: string(spec.Pattern),
		Tools: make([]string, 0, len(spec.Tools)), Skills: make([]string, 0, len(spec.Skills)),
	}
	// Found by search or sent, they are the run's to use.
	if spec.Discovery != nil {
		for _, t := range spec.Discovery.Tools {
			p.Tools = append(p.Tools, t.Name)
		}
	}
	for _, t := range spec.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	for _, s := range spec.Skills {
		p.Skills = append(p.Skills, s.Name)
	}
	return p
}

func runOf(r db.ClaimRunRow) db.Run {
	return db.Run{
		ID: r.ID, TeamID: r.TeamID, SessionID: r.SessionID, ParentRunID: r.ParentRunID,
		RootRunID: r.RootRunID, ParentToolCall: r.ParentToolCall, Depth: r.Depth,
		AgentID: r.AgentID, AgentName: r.AgentName, UserID: r.UserID, Status: r.Status,
		Input: r.Input, Output: r.Output, Error: r.Error, InputTokens: r.InputTokens,
		OutputTokens: r.OutputTokens, Worker: r.Worker, HeartbeatAt: r.HeartbeatAt,
		CancelRequestedAt: r.CancelRequestedAt, CreatedAt: r.CreatedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, TokenBudget: r.TokenBudget,
		Hub: r.Hub, MessageID: r.MessageID, ReplyToChannel: r.ReplyToChannel, Builder: r.Builder,
		Transcript: r.Transcript, TranscriptFrom: r.TranscriptFrom,
	}
}

// streamsText is whether a run's text and reasoning are recorded as they are
// written: a run a person is waiting on, the builder or any top-level run. A
// delegated run's are its parent's to report.
func streamsText(row db.Run) bool {
	return row.Builder || row.ParentRunID == nil
}
