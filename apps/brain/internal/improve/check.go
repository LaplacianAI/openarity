package improve

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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
	RecordRunOutcome(ctx context.Context, arg db.RecordRunOutcomeParams) (int64, error)
	GetRunOutcome(ctx context.Context, runID uuid.UUID) (db.RunOutcome, error)
	CountRunFeedback(ctx context.Context, runID uuid.UUID) (db.CountRunFeedbackRow, error)
	ListSettledRuns(ctx context.Context, arg db.ListSettledRunsParams) ([]db.ListSettledRunsRow, error)
	ListPendingDigests(ctx context.Context, pageSize int32) ([]db.ListPendingDigestsRow, error)
	InTx(ctx context.Context, fn func(*db.Queries) error) error
}

// Checker judges runs and digests them.
type Checker struct {
	store  Store
	judge  Judge
	logger *slog.Logger
}

func NewChecker(s Store, j Judge, logger *slog.Logger) *Checker {
	return &Checker{store: s, judge: j, logger: logger}
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
	JSON: json.RawMessage(`{
  "type": "object", "additionalProperties": false,
  "required": ["asked", "verdict", "missed", "why", "implicit"],
  "properties": {
    "asked": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "required": ["part", "delivered"],
      "properties": {"part": {"type": "string"}, "delivered": {"type": "string", "enum": ["yes", "partly", "no"]}}}},
    "verdict": {"type": "string", "enum": ["done", "partly", "not_done", "wrong", "cant_tell"]},
    "missed": {"type": "string"},
    "why": {"type": "string"},
    "implicit": {"type": "string", "enum": ["none", "correction", "repeat", "retry", "thanks"]}
  }
}`),
}

// check reads an answer as the table will hold it, refusing one outside
// the schema's sets.
func (a outcomeAnswer) check() error {
	if !oneOf(a.Verdict, verdicts) {
		return errAnswer{fmt.Sprintf("verdict %q", a.Verdict)}
	}
	if !oneOf(a.Implicit, implicits) {
		return errAnswer{fmt.Sprintf("implicit %q", a.Implicit)}
	}
	for _, p := range a.Asked {
		if !oneOf(p.Delivered, delivered) {
			return errAnswer{fmt.Sprintf("delivered %q", p.Delivered)}
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

// Check judges one settled run, once. A failure of the model's is stored as
// cant_tell with its reason, so a run is never judged again and again; only
// a failure of the brain's own is returned.
func (c *Checker) Check(ctx context.Context, runID uuid.UUID) error {
	run, err := c.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("reading run %s: %w", runID, err)
	}
	settings, err := c.store.GetImprovementSettings(ctx, run.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		return nil // turned off since it was swept
	}
	if err != nil {
		return fmt.Errorf("reading team %s's settings: %w", run.TeamID, err)
	}

	row := db.RecordRunOutcomeParams{
		RunID: runID, TeamID: run.TeamID, Verdict: verdictCantTell, Implicit: implicitNone,
		Asked: []byte("[]"), Sampled: Sampled(runID, settings.ImprovementSampleShare),
	}
	model, ok := judgeModelOf(settings)
	if ok {
		row.Model = model.Name
		c.judgeInto(ctx, runID, model, &row)
	} else {
		failed := "the team has no model to judge with: set its model, or a judge model"
		row.Error = &failed
	}

	counts, err := c.store.CountRunFeedback(ctx, runID)
	if err != nil {
		return fmt.Errorf("reading run %s's feedback: %w", runID, err)
	}
	row.Digest = digestFor(Effective(row.Verdict, row.Implicit, counts.Up, counts.Down), row.Sampled)
	if _, err := c.store.RecordRunOutcome(ctx, row); err != nil {
		return fmt.Errorf("recording run %s's outcome: %w", runID, err)
	}
	return nil
}

// judgeInto asks the model and fills row with its answer, or with why there
// is none.
func (c *Checker) judgeInto(ctx context.Context, runID uuid.UUID, model agent.ModelRef, row *db.RecordRunOutcomeParams) {
	fail := func(err error) {
		msg := clip(err.Error(), 1000)
		row.Error = &msg
		c.logger.WarnContext(ctx, "A run could not be judged", "run", runID, "err", err)
	}
	ev, err := gather(ctx, c.store, runID, false)
	if err != nil {
		fail(err)
		return
	}
	var answer outcomeAnswer
	usage, err := c.judge.doRetrying(ctx, ask{model: model, system: judgePrompt, user: ev.render(checkBudget), schema: outcomeSchema}, &answer)
	row.InputTokens, row.OutputTokens = int64(usage.InputTokens), int64(usage.OutputTokens)
	if err == nil {
		err = answer.check()
	}
	if err != nil {
		fail(err)
		return
	}
	for i := range answer.Asked {
		answer.Asked[i].Part = clip(answer.Asked[i].Part, 300)
	}
	asked, err := json.Marshal(answer.Asked)
	if err != nil {
		fail(err)
		return
	}
	row.Verdict, row.Implicit, row.Asked = answer.Verdict, answer.Implicit, asked
	row.Missed, row.Why = clip(answer.Missed, 2000), clip(answer.Why, 2000)
}
