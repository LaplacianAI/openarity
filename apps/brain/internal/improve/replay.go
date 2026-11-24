package improve

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

//go:embed prompts/replay.md
var replayPrompt string

// Replay is a past run run again, as the judge reads it: the steps it took,
// its answer, and how it ended.
type Replay struct {
	Trace  []string
	Answer string
	Ending string
}

// ReplayJudgement is the judge's verdict on a replay, with the model that
// gave it and what it cost.
type ReplayJudgement struct {
	Verdict string
	Why     string
	Usage   agent.Usage
}

// JudgeReplay judges a replay of a past run as Check judges the run: the
// same request, the conversation around it and what people said after,
// with the replay's own steps and answer in place of the run's. The prompt
// adds one paragraph: a call the replay did not make is judged as made.
// An answer that is unusable, or a team with no model, is cant_tell with
// its reason, as for a run.
func (c *Checker) JudgeReplay(ctx context.Context, runID uuid.UUID, r Replay) (ReplayJudgement, error) {
	run, err := c.store.GetRun(ctx, runID)
	if err != nil {
		return ReplayJudgement{}, fmt.Errorf("reading run %s: %w", runID, err)
	}
	settings, err := c.store.GetImprovementSettings(ctx, run.TeamID)
	if err != nil {
		return ReplayJudgement{}, fmt.Errorf("reading team %s's settings: %w", run.TeamID, err)
	}
	model, ok := judgeModelOf(settings)
	if !ok {
		return ReplayJudgement{Verdict: verdictCantTell, Why: errNoModel.Error()}, nil
	}
	ev, err := gather(ctx, c.store, runID, false)
	if err != nil {
		return ReplayJudgement{}, err
	}
	ev.trace, ev.answer, ev.ending = r.Trace, clip(r.Answer, answerChars), r.Ending
	var answer outcomeAnswer
	usage, err := c.judge.do(ctx, ask{
		model: model, system: judgePrompt + "\n\n" + replayPrompt, user: ev.render(checkBudget), schema: outcomeSchema,
	}, &answer)
	if err == nil {
		err = answer.check()
	}
	switch {
	case err != nil && final(err):
		return ReplayJudgement{Verdict: verdictCantTell, Why: clip(err.Error(), 2000), Usage: usage}, nil
	case err != nil:
		return ReplayJudgement{Usage: usage}, err
	}
	return ReplayJudgement{Verdict: answer.Verdict, Why: clip(answer.Why, 2000), Usage: usage}, nil
}
