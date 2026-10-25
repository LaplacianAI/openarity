package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
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
	store     Store
	builder   *Builder
	runner    *agent.Runner
	endpoint  agent.Endpoint
	sessions  agent.Store
	logger    *slog.Logger
	every     time.Duration
	lostAfter time.Duration
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

	err = appendEvent(ctx, e.store, d.Parent.ID, d.Lineage.Worker, "delegated", delegatedPayload{
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

	output, runErr := e.run(runCtx, row, l, stop)

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
	return e.finish(ctx, row.ID, l.Worker, status, output, failure)
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
) (string, error) {
	session, err := agent.Open(row.ID.String(), e.sessions)
	if err != nil {
		return "", err
	}
	msgs, err := session.Messages(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the transcript: %w", err)
	}

	built, err := e.builder.Build(ctx, row, session, l)
	if err != nil {
		return "", err
	}
	defer built.Close()

	if len(msgs) > 0 {
		if _, err := e.store.ReapChildren(ctx, db.ReapChildrenParams{ParentID: row.ID, Reason: reapReason}); err != nil {
			return "", fmt.Errorf("reaping what the run started before its worker stopped: %w", err)
		}
		err = appendEvent(ctx, e.store, row.ID, l.Worker, "resumed", resumedPayload{Messages: len(msgs)})
	} else {
		msgs = []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: agent.ContentText, Text: row.Input}},
		}}
		err = appendEvent(ctx, e.store, row.ID, l.Worker, "started", startedOf(row.AgentName, built.Spec))
	}
	if err != nil {
		return "", err
	}

	for _, u := range built.Unavailable {
		if err := appendEvent(ctx, e.store, row.ID, l.Worker, "tool_unavailable",
			unavailablePayload(u)); err != nil {
			return "", err
		}
	}

	events := make(chan agent.Event, 64)
	recorded := record(context.WithoutCancel(ctx), e.store, row.ID, l.Worker, budgetOf(row.TokenBudget), events, stop)
	result, err := e.runner.Run(ctx, built.Spec, msgs, e.endpoint, events)
	close(events)
	<-recorded

	return result.Output, err
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
		return "failed", fmt.Sprintf("the model endpoint OPENARITY_LLM_BASE_URL could not be reached: %v", unreachable.Err)
	case errors.As(runErr, &answered):
		return "failed", "OPENARITY_LLM_BASE_URL: " + answered.Error()
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
	if status == "succeeded" {
		p.Output = &output
	}
	if failure != "" {
		p.Error = &failure
	}

	n, err := e.store.FinishRun(ctx, p)
	if err != nil {
		return fmt.Errorf("finishing run %s as %s: %w", runID, status, err)
	}
	if n == 0 {
		return nil
	}

	if _, err := e.store.ReapChildren(ctx, db.ReapChildrenParams{ParentID: runID, Reason: endedReason}); err != nil {
		return fmt.Errorf("ending what run %s left running: %w", runID, err)
	}

	failure, _ = clip(failure)
	err = appendEvent(ctx, e.store, runID, worker, "finished", finishedPayload{Status: status, Error: failure})
	if err != nil && !errors.Is(err, ErrLostRun) {
		return err
	}
	return nil
}

func startedOf(agentName string, spec agent.Spec) startedPayload {
	p := startedPayload{
		Agent: agentName, Model: spec.Model.Name, Pattern: string(spec.Pattern),
		Tools: make([]string, 0, len(spec.Tools)), Skills: make([]string, 0, len(spec.Skills)),
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
	}
}
