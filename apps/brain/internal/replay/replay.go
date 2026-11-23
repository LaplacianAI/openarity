// Package replay tries a proposed change to an agent on a few of its past
// runs before anyone decides on it, and keeps what came of each: the I6
// slice of the self-improving agents design.
//
// A try runs the past run again on the harness the proposal would leave
// (candidate.go), through the runtime's Replayer, which acts on nothing and
// writes nothing; the I2 judge decides it as it decided the run. This
// package chooses the runs and keeps the rows.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	// goodWithin is how recent a good run must be to be tried on.
	goodWithin = 14 * 24 * time.Hour
	// eventsRead is how many of a past run's events a try reads.
	eventsRead = 4000
	// answerBytes and traceBytes are what a try's row keeps.
	answerBytes = 16 << 10
	traceBytes  = 256 << 10

	chosenCited = "cited"
	chosenGood  = "good"

	statusQueued = "queued"

	outcomeGood     = "good"
	outcomeBad      = "bad"
	outcomeCantTell = "cant_tell"
)

// Why the tries still to run of a proposal are skipped.
const (
	whyDecided  = "the proposal was decided before it was tried"
	whyOff      = "trying changes on past runs was turned off"
	whyDailyCap = "the team's tries for today are used"
)

// Store is what a replay reads and writes.
type Store interface {
	runtime.BuildStore
	CandidateReads
	GetReplaySettings(ctx context.Context, teamID uuid.UUID) (db.GetReplaySettingsRow, error)
	GetImprovementProposal(ctx context.Context, arg db.GetImprovementProposalParams) (db.ImprovementProposal, error)
	GetRunStartedSeq(ctx context.Context, runID uuid.UUID) (int64, error)
	ListRunEvents(ctx context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error)
	ListReplayRuns(ctx context.Context, arg db.ListReplayRunsParams) ([]db.ListReplayRunsRow, error)
	ListReplayGoodRunIDs(ctx context.Context, arg db.ListReplayGoodRunIDsParams) ([]uuid.UUID, error)
	InsertReplay(ctx context.Context, arg db.InsertReplayParams) error
	ListQueuedReplays(ctx context.Context, proposalID uuid.UUID) ([]uuid.UUID, error)
	StartReplay(ctx context.Context, arg db.StartReplayParams) (int64, error)
	FinishReplay(ctx context.Context, arg db.FinishReplayParams) error
	FailReplay(ctx context.Context, arg db.FailReplayParams) error
	SkipReplay(ctx context.Context, arg db.SkipReplayParams) error
	SkipQueuedReplays(ctx context.Context, arg db.SkipQueuedReplaysParams) error
}

// Runner replays a run: the runtime's Replayer.
type Runner interface {
	Replay(ctx context.Context, in runtime.ReplayInput) (runtime.Replayed, error)
}

// Judge decides a replay: the improve Checker.
type Judge interface {
	JudgeReplay(ctx context.Context, runID uuid.UUID, r improve.Replay) (improve.ReplayJudgement, error)
}

// Replays tries proposals on past runs.
type Replays struct {
	store    Store
	sessions agent.Store
	runner   Runner
	judge    Judge
	now      func() time.Time
	// minute is how long a minute of a team's replay minutes is.
	minute time.Duration
}

// New tries proposals with store, reading past runs' transcripts from
// sessions (never writing them).
func New(s Store, sessions agent.Store, r Runner, j Judge) *Replays {
	return &Replays{store: s, sessions: sessions, runner: r, judge: j, now: time.Now, minute: time.Minute}
}

// errStop ends a proposal's tries: what is left was skipped, saying why.
var errStop = errors.New("the proposal's tries are stopped")

// Choose picks the past runs a proposal is tried on and writes a row for
// each, queued; a cited run that cannot be tried is written skipped, with
// why. Nothing is chosen for a proposal no longer waiting, or a team with
// replay off.
func (r *Replays) Choose(ctx context.Context, proposalID, teamID uuid.UUID) error {
	p, err := r.store.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: proposalID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("reading proposal %s: %w", proposalID, err)
	}
	s, err := r.store.GetReplaySettings(ctx, teamID)
	if err != nil {
		return fmt.Errorf("reading team %s's settings: %w", teamID, err)
	}
	if p.Status != "waiting" || !s.Improvement || !s.ImprovementReplay {
		return nil
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
			Chosen: c.chosen, Original: c.original, Status: "skipped", SkipWhy: &why,
		}); err != nil {
			return fmt.Errorf("noting run %s as not tried: %w", c.run.Run.ID, err)
		}
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

// Queued is a proposal's tries still to run, in their order.
func (r *Replays) Queued(ctx context.Context, proposalID uuid.UUID) ([]uuid.UUID, error) {
	return r.store.ListQueuedReplays(ctx, proposalID)
}

// Try runs one of a proposal's tries and writes what came of it. errStop
// says the proposal's tries are over, the rest skipped; any other error is
// the store's, for the step to try again.
func (r *Replays) Try(ctx context.Context, proposalID, teamID, runID uuid.UUID) error {
	p, err := r.store.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: proposalID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("reading proposal %s: %w", proposalID, err)
	}
	if p.Status != "waiting" && p.Status != "applied" {
		return r.stop(ctx, proposalID, whyDecided)
	}
	s, err := r.store.GetReplaySettings(ctx, teamID)
	if err != nil {
		return fmt.Errorf("reading team %s's settings: %w", teamID, err)
	}
	if !s.Improvement || !s.ImprovementReplay {
		return r.stop(ctx, proposalID, whyOff)
	}
	started, err := r.store.StartReplay(ctx, db.StartReplayParams{
		ProposalID: proposalID, SourceRunID: runID, Since: midnight(r.now()), Daily: int32(s.ImprovementReplayDaily),
	})
	if err != nil {
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
		msg := clipText(err.Error(), 2000)
		return r.store.FailReplay(context.WithoutCancel(ctx), db.FailReplayParams{
			Error: &msg, InputTokens: int64(res.Usage.InputTokens), OutputTokens: int64(res.Usage.OutputTokens),
			ProposalID: proposalID, SourceRunID: runID,
		})
	}
	return r.finish(ctx, p, runID, res)
}

func (r *Replays) stop(ctx context.Context, proposalID uuid.UUID, why string) error {
	if err := r.store.SkipQueuedReplays(ctx, db.SkipQueuedReplaysParams{SkipWhy: &why, ProposalID: proposalID}); err != nil {
		return fmt.Errorf("stopping proposal %s's tries: %w", proposalID, err)
	}
	return errStop
}

// Stopped is whether err says a proposal's tries are over.
func Stopped(err error) bool { return errors.Is(err, errStop) }

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
	c, err := candidateOf(r.store, r.store, p, before)
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
		Store: c, Texts: c.texts, Run: run, Messages: msgs, Recorded: recorded,
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

type callPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type resultPayload struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

// recorded is what the past run's calls returned, oldest first: each call
// with the result its id names. A call with no result is left out, as there
// is nothing to serve back.
func (r *Replays) recorded(ctx context.Context, runID uuid.UUID) ([]runtime.Recorded, error) {
	events, err := r.store.ListRunEvents(ctx, db.ListRunEventsParams{RunID: runID, After: 0, PageSize: eventsRead})
	if err != nil {
		return nil, fmt.Errorf("reading run %s's events: %w", runID, err)
	}
	results := map[string]resultPayload{}
	var calls []callPayload
	for _, e := range events {
		switch e.Kind {
		case "tool_call":
			var c callPayload
			if json.Unmarshal(e.Payload, &c) == nil {
				calls = append(calls, c)
			}
		case "tool_result":
			var res resultPayload
			if json.Unmarshal(e.Payload, &res) == nil {
				results[res.ID] = res
			}
		}
	}
	out := make([]runtime.Recorded, 0, len(calls))
	for _, c := range calls {
		res, ok := results[c.ID]
		if !ok {
			continue
		}
		out = append(out, runtime.Recorded{Name: c.Name, Arguments: c.Arguments, Output: res.Output, Error: res.Error})
	}
	return out, nil
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
	answer := clipText(res.Output, answerBytes)
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
			msg := clipText("judging the try: "+err.Error(), 2000)
			return r.store.FailReplay(context.WithoutCancel(ctx), db.FailReplayParams{
				Error: &msg, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
				ProposalID: p.ID, SourceRunID: runID,
			})
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
	if err := r.store.FinishReplay(context.WithoutCancel(ctx), row); err != nil {
		return fmt.Errorf("writing a try of proposal %s: %w", p.ID, err)
	}
	return nil
}

// judgeTrace is the try's steps as the judge reads a run's, each call saying
// how it was served.
func judgeTrace(res runtime.Replayed) []string {
	lines := []string{fmt.Sprintf("the agent started with tools [%s]", strings.Join(res.Tools, ", "))}
	for _, c := range res.Calls {
		how := ""
		switch c.How {
		case runtime.ServedRecorded:
			how = " [as in the past run]"
		case runtime.ServedLive:
			how = " [run in the replay]"
		case runtime.ServedNotMade:
			lines = append(lines, fmt.Sprintf("  the agent calls %s(%s) [not made in the replay]", c.Name, clipText(c.Arguments, 300)))
			continue
		}
		lines = append(lines, fmt.Sprintf("  the agent calls %s(%s)%s", c.Name, clipText(c.Arguments, 300), how))
		if c.Error != "" {
			lines = append(lines, fmt.Sprintf("  %s failed: %s", c.Name, clipText(c.Error, 300)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s returned: %s", c.Name, clipText(c.Output, 300)))
		}
	}
	return lines
}

func ending(res runtime.Replayed) string {
	return fmt.Sprintf("status succeeded; %d steps; tokens in %d, out %d", res.Steps, res.Usage.InputTokens, res.Usage.OutputTokens)
}

// traceOf is the try's calls as its row keeps them, dropped from the end
// past traceBytes, saying how many were.
func traceOf(calls []runtime.Served) []byte {
	kept := calls
	for {
		b, err := json.Marshal(kept)
		if err == nil && len(b) <= traceBytes {
			return b
		}
		if len(kept) == 0 {
			return []byte("[]")
		}
		kept = kept[:len(kept)*3/4]
	}
}

// clipText cuts s to n bytes on a rune boundary.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
