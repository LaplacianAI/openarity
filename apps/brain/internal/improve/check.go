package improve

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

//go:embed prompts/judge.md
var judgePrompt string

// Store is what judging and digesting read and write.
type Store interface {
	Reads
	GetImprovementSettings(ctx context.Context, teamID uuid.UUID) (db.GetImprovementSettingsRow, error)
	GetRunOutcome(ctx context.Context, runID uuid.UUID) (db.RunOutcome, error)
	InTx(ctx context.Context, fn func(*db.Queries) error) error
}

// Checker judges runs and digests them.
type Checker struct {
	store  Store
	judge  Judge
	logger *slog.Logger
	now    func() time.Time
}

func NewChecker(s Store, j Judge, logger *slog.Logger) *Checker {
	return &Checker{store: s, judge: j, logger: logger, now: time.Now}
}

type askedPart struct {
	Part      string `json:"part"`
	Delivered string `json:"delivered"`
}

type outcomeAnswer struct {
	Asked    []askedPart `json:"asked"`
	Verdict  string      `json:"verdict"`
	Missed   string      `json:"missed"`
	Why      string      `json:"why"`
	Implicit string      `json:"implicit"`
}

var outcomeSchema = &agent.OutputSchema{
	Name:        "outcome",
	Description: "Whether the agent did what was asked.",
	Strict:      true,
	JSON: schemaJSON(object(obj{
		"asked":    listOf(object(obj{"part": text(), "delivered": oneOfSet(delivered)})),
		"verdict":  oneOfSet(verdicts),
		"missed":   text(),
		"why":      text(),
		"implicit": oneOfSet(implicits),
	})),
}

// check reads an answer as the table will hold it, refusing one outside
// the schema's sets.
func (a outcomeAnswer) check() error {
	if !oneOf(a.Verdict, verdicts) {
		return unusable("verdict %q", a.Verdict)
	}
	if !oneOf(a.Implicit, implicits) {
		return unusable("implicit %q", a.Implicit)
	}
	for _, p := range a.Asked {
		if !oneOf(p.Delivered, delivered) {
			return unusable("delivered %q", p.Delivered)
		}
	}
	return nil
}

// judgeModelOf is the model that judges: the judge's, else the team's.
func judgeModelOf(s db.GetImprovementSettingsRow) (agent.ModelRef, bool) {
	switch {
	case s.JudgeModelName != nil && s.JudgeModelMaxTokens != nil:
		return agent.ModelRef{Name: *s.JudgeModelName, MaxTokens: int(*s.JudgeModelMaxTokens), Temperature: s.JudgeModelTemperature}, true
	case s.ModelName != nil && s.ModelMaxTokens != nil:
		return agent.ModelRef{Name: *s.ModelName, MaxTokens: int(*s.ModelMaxTokens), Temperature: s.ModelTemperature}, true
	}
	return agent.ModelRef{}, false
}

var errNoModel = errFinal{"the team has no model to judge with: set its model, or a judge model"}

// Check judges one settled run, once. An answer that is unusable, or a team
// with no model, is recorded as cant_tell with its reason; a model or a
// store that cannot be reached is returned, for the step to try again, and
// GiveUp records it once the tries run out.
func (c *Checker) Check(ctx context.Context, runID uuid.UUID) error {
	run, settings, on, err := c.target(ctx, runID)
	if err != nil || !on {
		return err
	}
	row := newOutcome(run, settings)
	model, ok := judgeModelOf(settings)
	if !ok {
		return c.record(ctx, row, errNoModel)
	}
	row.Model = model.Name
	j, usage, err := c.judgeRun(ctx, runID, model)
	row.InputTokens, row.OutputTokens = int64(usage.InputTokens), int64(usage.OutputTokens)
	if err != nil && !final(err) {
		return err
	}
	if err == nil {
		row.Verdict, row.Implicit, row.Asked, row.Missed, row.Why = j.Verdict, j.Implicit, j.asked, j.Missed, j.Why
	}
	return c.record(ctx, row, err)
}

// giveUpAfter is how long a run's check keeps being tried, an hour apart,
// before it is recorded as given up: an outage shorter than this loses no
// judgement.
const giveUpAfter = 24 * time.Hour

// GiveUp records a run whose check failed every try as cant_tell, with the
// last failure, so it is not judged again and again; but only once the run
// is giveUpAfter old. Until then it returns the failure, writing nothing,
// and the next hour's sweep queues the check again.
func (c *Checker) GiveUp(ctx context.Context, runID uuid.UUID, cause error) error {
	run, settings, on, err := c.target(ctx, runID)
	if err != nil || !on {
		return err
	}
	if run.FinishedAt != nil && c.now().Sub(*run.FinishedAt) < giveUpAfter {
		return fmt.Errorf("run %s is tried again next hour: %w", runID, cause)
	}
	row := newOutcome(run, settings)
	if model, ok := judgeModelOf(settings); ok {
		row.Model = model.Name
	}
	return c.record(ctx, row, fmt.Errorf("the check failed every try: %w", cause))
}

// target is the run to judge and its team's settings; on is false when
// there is nothing to do: improvement turned off since the sweep, or the
// run judged already.
func (c *Checker) target(ctx context.Context, runID uuid.UUID) (db.Run, db.GetImprovementSettingsRow, bool, error) {
	run, err := c.store.GetRun(ctx, runID)
	if err != nil {
		return db.Run{}, db.GetImprovementSettingsRow{}, false, fmt.Errorf("reading run %s: %w", runID, err)
	}
	settings, err := c.store.GetImprovementSettings(ctx, run.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		return run, settings, false, nil
	}
	if err != nil {
		return run, settings, false, fmt.Errorf("reading team %s's settings: %w", run.TeamID, err)
	}
	_, err = c.store.GetRunOutcome(ctx, runID)
	switch {
	case err == nil:
		return run, settings, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return run, settings, false, fmt.Errorf("reading run %s's outcome: %w", runID, err)
	}
	return run, settings, true, nil
}

func newOutcome(run db.Run, settings db.GetImprovementSettingsRow) db.RecordRunOutcomeParams {
	return db.RecordRunOutcomeParams{
		RunID: run.ID, TeamID: run.TeamID, Verdict: verdictCantTell, Implicit: implicitNone,
		Asked: []byte("[]"), Sampled: Sampled(run.ID, settings.ImprovementSampleShare),
	}
}

// record writes the outcome, with failed as its reason when there is one,
// deciding whether it is digested while holding the run's votes: a 👎 given
// at the same moment is either seen here or reopens the digest after.
func (c *Checker) record(ctx context.Context, row db.RecordRunOutcomeParams, failed error) error {
	if failed != nil {
		msg := clip(failed.Error(), 1000)
		row.Error = &msg
		c.logger.WarnContext(ctx, "A run could not be judged", "run", row.RunID, "err", failed)
	}
	err := c.store.InTx(ctx, func(q *db.Queries) error {
		if err := q.LockRunVotes(ctx, row.RunID); err != nil {
			return err
		}
		counts, err := q.CountRunFeedback(ctx, row.RunID)
		if err != nil {
			return err
		}
		row.Digest = digestFor(Effective(row.Verdict, row.Implicit, counts.Up, counts.Down), row.Sampled)
		_, err = q.RecordRunOutcome(ctx, row)
		return err
	})
	if err != nil {
		return fmt.Errorf("recording run %s's outcome: %w", row.RunID, err)
	}
	return nil
}

// judgement is the judge's answer, checked and clipped as the table holds it.
type judgement struct {
	outcomeAnswer
	asked []byte
}

func (c *Checker) judgeRun(ctx context.Context, runID uuid.UUID, model agent.ModelRef) (judgement, agent.Usage, error) {
	ev, err := gather(ctx, c.store, runID, false)
	if err != nil {
		return judgement{}, agent.Usage{}, err
	}
	var answer outcomeAnswer
	usage, err := c.judge.do(ctx, ask{model: model, system: judgePrompt, user: ev.render(checkBudget), schema: outcomeSchema}, &answer)
	if err == nil {
		err = answer.check()
	}
	if err != nil {
		return judgement{}, usage, err
	}
	for i := range answer.Asked {
		answer.Asked[i].Part = clip(answer.Asked[i].Part, 300)
	}
	asked, err := json.Marshal(answer.Asked)
	if err != nil {
		return judgement{}, usage, unusable("%v", err)
	}
	answer.Missed, answer.Why = clip(answer.Missed, 2000), clip(answer.Why, 2000)
	return judgement{outcomeAnswer: answer, asked: asked}, usage, nil
}
