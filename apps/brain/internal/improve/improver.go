package improve

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

//go:embed prompts/improver.md
var improverPrompt string

const (
	triggerPattern = "pattern"
	triggerDaily   = "daily"

	skillTool = agent.SkillToolName
	callTool  = agent.CallToolName

	// A pass takes this many steps at most: reading a few runs and skills,
	// and proposing.
	passSteps = 16
	// What a pass may store and have refused, before it is done proposing.
	proposalsPerPass = 3
	refusalsPerPass  = 3
	// A running pass not seen for this long is taken as lost. Longer than
	// the most minutes a team may give one try (25), so a live one is
	// never taken.
	passAlive = 30 * time.Minute

	reportChars       = 4000
	instructionsChars = 32000
	summariesPerKey   = 8
	digestsPerKey     = 20
	wentWellShown     = 10
	wentWrongShown    = 10
	recordShown       = 20
	elsewhereShown    = 10
	// A pattern whose pass was cut short with nothing proposed is not
	// offered again for this many hours: one that keeps running out is not
	// queued every sweep, spending the team's day.
	cutShortBackOffHours = 6
	// A prune candidate looked at is not offered again for this long.
	pruneQuiet = 30 * 24 * time.Hour
)

// Why a pass failed, as the dashboard says it after "Failed:". The cause,
// which may name the model's endpoint, goes to the log.
const (
	failedEveryTry = "the model or the database could not be reached on any try; the brain's log has the cause"
	failedLost     = "it stopped answering and was taken as lost"
)

var (
	errPassTime   = errors.New("the pass ran out of time")
	errPassTokens = errors.New("the pass spent its tokens")
	errPassLost   = errors.New("the pass was failed as lost")
)

// ImproverStore is what passes read and write.
type ImproverStore interface {
	Store
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	GetAgentByName(ctx context.Context, arg db.GetAgentByNameParams) (db.Agent, error)
	ListTeamSkills(ctx context.Context, teamID uuid.UUID) ([]db.Skill, error)
	ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error)
	ListTeamServersWithTools(ctx context.Context, teamID uuid.UUID) ([]db.ListTeamServersWithToolsRow, error)
	ListTeamAgentNames(ctx context.Context, arg db.ListTeamAgentNamesParams) ([]db.ListTeamAgentNamesRow, error)
	ListPatternDigests(ctx context.Context, arg db.ListPatternDigestsParams) ([]db.ListPatternDigestsRow, error)
	ListWentWell(ctx context.Context, arg db.ListWentWellParams) ([]db.ListWentWellRow, error)
	ListWentWrong(ctx context.Context, arg db.ListWentWrongParams) ([]db.ListWentWrongRow, error)
	ListAgentProposalRecord(ctx context.Context, arg db.ListAgentProposalRecordParams) ([]db.ListAgentProposalRecordRow, error)
	ListTeamAppliedElsewhere(ctx context.Context, arg db.ListTeamAppliedElsewhereParams) ([]db.ListTeamAppliedElsewhereRow, error)
	AnyDigestOutsider(ctx context.Context, arg db.AnyDigestOutsiderParams) (bool, error)
	CountPassProposals(ctx context.Context, passID *uuid.UUID) (int32, error)
	CountPassProposalsOnTarget(ctx context.Context, arg db.CountPassProposalsOnTargetParams) (int32, error)
	AgentPassRunning(ctx context.Context, agentID uuid.UUID) (bool, error)
	CountTeamPassesSince(ctx context.Context, arg db.CountTeamPassesSinceParams) (int32, error)
	BeginImproverPass(ctx context.Context, arg db.BeginImproverPassParams) (db.ImproverPass, error)
	GetImproverPassByWorkflow(ctx context.Context, workflowID string) (db.ImproverPass, error)
	TouchImproverPass(ctx context.Context, arg db.TouchImproverPassParams) (int64, error)
	AddImproverPassUsage(ctx context.Context, arg db.AddImproverPassUsageParams) error
	FinishImproverPass(ctx context.Context, arg db.FinishImproverPassParams) (db.FinishImproverPassRow, error)
	FailLostImproverPasses(ctx context.Context, arg db.FailLostImproverPassesParams) (int64, error)
	GetImproverPass(ctx context.Context, id uuid.UUID) (db.ImproverPass, error)
	ListAgentRecentRuns(ctx context.Context, arg db.ListAgentRecentRunsParams) ([]uuid.UUID, error)
	ListRunsUse(ctx context.Context, arg db.ListRunsUseParams) ([]db.ListRunsUseRow, error)
	ListRecentPruneLooks(ctx context.Context, arg db.ListRecentPruneLooksParams) ([]string, error)
	ListRunDigests(ctx context.Context, runID uuid.UUID) ([]db.RunDigest, error)
}

// Improver runs passes: an agent's patterns read, and proposals written.
type Improver struct {
	store   ImproverStore
	objects proposals.Getter
	judge   Judge
	runner  *agent.Runner
	loops   []string
	logger  *slog.Logger
	now     func() time.Time
	// minute is how long a minute of a team's pass minutes is: a minute,
	// shorter in a test.
	minute time.Duration
}

// NewImprover runs passes through the judge's model endpoint. loops are the
// patterns the brain runs, which a pattern proposal must name.
func NewImprover(s ImproverStore, objects proposals.Getter, j Judge, loops []string, logger *slog.Logger) (*Improver, error) {
	runner, err := agent.New(j.clients, patterns.ReAct())
	if err != nil {
		return nil, fmt.Errorf("building the improver's runner: %w", err)
	}
	return &Improver{
		store: s, objects: objects, judge: j, runner: runner, loops: loops, logger: logger, now: time.Now,
		minute: time.Minute,
	}, nil
}

// improving is a team's improvement settings, and whether improvement is on.
func (im *Improver) improving(ctx context.Context, teamID uuid.UUID) (db.GetImprovementSettingsRow, bool, error) {
	settings, err := im.store.GetImprovementSettings(ctx, teamID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return settings, false, nil
	case err != nil:
		return settings, false, fmt.Errorf("reading team %s's settings: %w", teamID, err)
	}
	return settings, settings.Improvement, nil
}

var errNoPass = errors.New("no pass to run")

// Begin records a pass about to run, with the runs it may read, and returns
// its id; errNoPass when there is nothing to run: the team turned off, at its
// day's cap, or the agent already has one under way. A workflow that has
// begun its pass already gets that one back, so a step tried again never
// begins a second.
func (im *Improver) Begin(ctx context.Context, plan plannedPass, workflowID string) (uuid.UUID, error) {
	if id, ok, err := im.begun(ctx, workflowID); ok || err != nil {
		return id, err
	}
	settings, on, err := im.improving(ctx, plan.TeamID)
	if err != nil {
		return uuid.UUID{}, err
	}
	if !on {
		return uuid.UUID{}, errNoPass
	}
	now := im.now().UTC()
	busy, err := im.store.AgentPassRunning(ctx, plan.AgentID)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("reading agent %s's passes: %w", plan.AgentID, err)
	}
	used, err := im.store.CountTeamPassesSince(ctx, db.CountTeamPassesSinceParams{
		TeamID: plan.TeamID, Since: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("counting team %s's passes: %w", plan.TeamID, err)
	}
	if busy || used >= int32(settings.ImprovementDailyPasses) {
		return uuid.UUID{}, errNoPass
	}

	runs, err := im.runsFor(ctx, plan)
	if err != nil {
		return uuid.UUID{}, err
	}
	ids := make([]uuid.UUID, len(runs))
	for i, r := range runs {
		ids[i] = r.run
	}
	model, _ := judgeModelOf(settings)
	pass, err := im.store.BeginImproverPass(ctx, db.BeginImproverPassParams{
		TeamID: plan.TeamID, AgentID: plan.AgentID, Trigger: plan.Trigger,
		Signatures: orEmpty(plan.Keys), Pruned: orEmpty(plan.Prune), RunIds: ids, Model: model.Name,
		WorkflowID: workflowID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another worker began this workflow's pass, or another pass for
		// the agent, in the meantime.
		if id, ok, err := im.begun(ctx, workflowID); ok || err != nil {
			return id, err
		}
		return uuid.UUID{}, errNoPass
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("recording agent %s's pass: %w", plan.AgentID, err)
	}
	im.logger.InfoContext(ctx, "An improver pass began", "pass", pass.ID, "team", plan.TeamID, "agent", plan.AgentID,
		"trigger", plan.Trigger, "patterns", len(plan.Keys), "prune", len(plan.Prune), "runs", len(ids), "model", model.Name)
	return pass.ID, nil
}

func (im *Improver) begun(ctx context.Context, workflowID string) (uuid.UUID, bool, error) {
	pass, err := im.store.GetImproverPassByWorkflow(ctx, workflowID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return uuid.UUID{}, false, nil
	case err != nil:
		return uuid.UUID{}, false, fmt.Errorf("reading workflow %s's pass: %w", workflowID, err)
	}
	return pass.ID, true, nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// leadPart is the part and target of a pass's first pattern: what its
// successes are chosen to contrast with. Empty for a pass with none.
func leadPart(keys []string) (part, target string) {
	if len(keys) == 0 {
		return "", ""
	}
	k, _ := parseKey(keys[0])
	return k.Part, k.Target
}

// runsFor chooses the runs a pass may read: the clearest failures of its
// patterns (or, with none, the agent's newest), then successes on the same
// part to contrast.
func (im *Improver) runsFor(ctx context.Context, plan plannedPass) ([]toRead, error) {
	var failures []toRead
	for _, s := range plan.Keys {
		k, ok := parseKey(s)
		if !ok {
			continue
		}
		rows, err := im.store.ListPatternDigests(ctx, db.ListPatternDigestsParams{
			AgentID: plan.AgentID, Part: k.Part, Target: k.Target, Cause: k.Cause, PageSize: digestsPerKey,
		})
		if err != nil {
			return nil, fmt.Errorf("reading agent %s's pattern %s: %w", plan.AgentID, s, err)
		}
		for _, r := range rows {
			failures = append(failures, toRead{
				run: r.RunID, failure: true, rank: failureRank(r.Verdict, r.Down), at: r.CreatedAt.UnixNano(),
				line: fmt.Sprintf("went wrong (%s); judged %s, %d 👎 %d 👍", s, r.Verdict, r.Down, r.Up),
			})
		}
	}
	if len(plan.Keys) == 0 {
		rows, err := im.store.ListWentWrong(ctx, db.ListWentWrongParams{AgentID: plan.AgentID, PageSize: failuresToRead})
		if err != nil {
			return nil, fmt.Errorf("reading agent %s's failures: %w", plan.AgentID, err)
		}
		for _, r := range rows {
			k := Key{Part: r.Part, Target: r.Target, Cause: r.Cause}
			failures = append(failures, toRead{run: r.RunID, failure: true, rank: rankUnweighed, at: r.CreatedAt.UnixNano(), line: "went wrong (" + k.String() + ")"})
		}
	}
	part, target := leadPart(plan.Keys)
	well, err := im.store.ListWentWell(ctx, db.ListWentWellParams{AgentID: plan.AgentID, Part: part, Target: target, PageSize: successesToRead})
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's successes: %w", plan.AgentID, err)
	}
	var successes []toRead
	for _, r := range well {
		successes = append(successes, toRead{run: r.RunID, at: r.CreatedAt.UnixNano(), line: "went well (" + r.Part + ":" + r.Target + ")"})
	}
	return chooseRuns(failures, successes), nil
}

// outcome is what a try at a pass came to.
type outcome struct {
	report string
	// cutShort: a limit, its steps or the team ended it before it finished.
	cutShort bool
	// lost: it was failed as lost while it ran, and its agent may already
	// have another; nothing more is written for it.
	lost bool
}

// Run runs a begun pass and records it done. A model or database that
// cannot be reached is returned for the step to try again, the pass still
// running; what the attempt spent and stored stays counted, so a pass tried
// again never stores more than its share.
func (im *Improver) Run(ctx context.Context, passID uuid.UUID) error {
	pass, err := im.store.GetImproverPass(ctx, passID)
	if err != nil {
		return fmt.Errorf("reading pass %s: %w", passID, err)
	}
	if pass.Status != passRunning {
		return nil
	}
	settings, on, err := im.improving(ctx, pass.TeamID)
	if err != nil {
		return err
	}
	if !on {
		// It looked at nothing: its patterns count on when the team turns
		// improvement on again.
		return im.done(ctx, pass.ID, outcome{report: "Improvement was turned off before the pass ran.", cutShort: true})
	}
	model, ok := judgeModelOf(settings)
	if !ok {
		return im.fail(ctx, pass.ID, errNoModel)
	}

	m, err := im.material(ctx, pass)
	if errors.Is(err, pgx.ErrNoRows) {
		return im.done(ctx, pass.ID, outcome{report: "The agent is gone.", cutShort: true})
	}
	if err != nil {
		return err
	}
	if alive, err := im.touch(ctx, pass.ID, m.drewOn); err != nil || !alive {
		return err
	}
	out, err := im.try(ctx, pass, settings, model, m)
	if err != nil || out.lost {
		return err
	}
	return im.done(ctx, pass.ID, out)
}

// touch marks a running pass alive, and draws on these runs; false when it
// is running no longer, failed as lost meanwhile.
func (im *Improver) touch(ctx context.Context, id uuid.UUID, drewOn []uuid.UUID) (bool, error) {
	n, err := im.store.TouchImproverPass(ctx, db.TouchImproverPassParams{ID: id, DrewOn: drewOn})
	if err != nil {
		return false, fmt.Errorf("marking pass %s alive: %w", id, err)
	}
	return n > 0, nil
}

// recordTimeout bounds a write made past a try's own context: what it
// spent, or a proposal it screened.
const recordTimeout = 30 * time.Second

// lasting is ctx without its cancellation, so a write a try owes is made
// even as the try ends, held to recordTimeout.
func lasting(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

// try runs the pass once, within what the team lets it spend: tokens across
// its tries, so earlier ones count, and minutes for this one. An error is
// for the step to try again, what this try spent and stored kept either
// way.
func (im *Improver) try(ctx context.Context, pass db.ImproverPass, settings db.GetImprovementSettingsRow, model agent.ModelRef, m material) (outcome, error) {
	spent := int(pass.InputTokens + pass.OutputTokens)
	if spent >= int(settings.ImprovementPassTokens) {
		// Earlier tries spent it all: not one call more.
		return outcome{report: "The pass stopped: " + errPassTokens.Error() + ".", cutShort: true}, nil
	}
	stored, err := im.store.CountPassProposals(ctx, &pass.ID)
	if err != nil {
		return outcome{}, fmt.Errorf("counting pass %s's proposals: %w", pass.ID, err)
	}
	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	runCtx, cancel := context.WithTimeoutCause(runCtx, time.Duration(settings.ImprovementPassMinutes)*im.minute, errPassTime)
	defer cancel()
	t := &passTools{
		im: im, pass: pass, material: m, proposed: stored, stop: stop,
		tokens: int(settings.ImprovementPassTokens), before: spent,
	}
	events := make(chan agent.Event, 16)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		t.watch(runCtx, events)
	}()
	res, err := im.runner.Run(runCtx, agent.Spec{
		Model: model, Pattern: agent.PatternReAct, System: agent.System(improverPrompt),
		Tools: t.tools(), MaxSteps: passSteps,
	}, []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: m.text}}}}, im.judge.endpoint, events)
	close(events)
	<-watched

	// Written even when the worker is shutting down: a try tried again
	// starts from what this one spent.
	in, out, screened := t.spent(res.Usage)
	wctx, done := lasting(ctx)
	uerr := im.store.AddImproverPassUsage(wctx, db.AddImproverPassUsageParams{
		ID: pass.ID, InputTokens: in, OutputTokens: out, ScreenedOut: screened,
	})
	done()
	if uerr != nil {
		return outcome{}, errors.Join(err, fmt.Errorf("recording pass %s's usage: %w", pass.ID, uerr))
	}

	report := strings.TrimSpace(res.Output)
	switch cause := context.Cause(runCtx); {
	case ctx.Err() != nil:
		return outcome{}, fmt.Errorf("running pass %s: %w", pass.ID, context.Cause(ctx))
	case errors.Is(cause, errPassLost):
		return outcome{lost: true}, nil
	case errors.Is(cause, errPassTime), errors.Is(cause, errPassTokens):
		// Trying again would spend as much again: it stops with what it has.
		return outcome{report: strings.TrimSpace(report + "\n\nThe pass stopped: " + cause.Error() + "."), cutShort: true}, nil
	case cause != nil:
		// A tool stopped it: the screen could not be reached.
		return outcome{}, fmt.Errorf("running pass %s: %w", pass.ID, cause)
	case errors.Is(err, agent.ErrMaxSteps):
		return outcome{report: strings.TrimSpace(report + "\n\nThe pass ran out of steps."), cutShort: true}, nil
	case err != nil:
		return outcome{}, fmt.Errorf("running pass %s: %w", pass.ID, err)
	}
	return outcome{report: report}, nil
}

// Fail records a pass that failed every try.
func (im *Improver) Fail(ctx context.Context, passID uuid.UUID, cause error) error {
	return im.fail(ctx, passID, cause)
}

// FailLost fails the passes whose worker stopped answering, so their agents
// may have another.
func (im *Improver) FailLost(ctx context.Context) error {
	msg := failedLost
	// The database's clock, which alive_at was written by.
	n, err := im.store.FailLostImproverPasses(ctx, db.FailLostImproverPassesParams{Error: &msg, SilentMinutes: int32(passAlive / time.Minute)})
	if err != nil {
		return fmt.Errorf("failing lost passes: %w", err)
	}
	if n > 0 {
		im.logger.WarnContext(ctx, "Improver passes were lost and failed", "passes", n)
	}
	return nil
}

const (
	passRunning = "running"
	passDone    = "done"
	passFailed  = "failed"
)

// done records a pass done, with what it came to.
func (im *Improver) done(ctx context.Context, id uuid.UUID, o outcome) error {
	return im.record(ctx, id, db.FinishImproverPassParams{Status: passDone, Report: o.report, CutShort: o.cutShort}, nil)
}

// fail records a pass failed, with failed's reason.
func (im *Improver) fail(ctx context.Context, id uuid.UUID, failed error) error {
	return im.record(ctx, id, db.FinishImproverPassParams{Status: passFailed}, failed)
}

// record finishes a pass as p says, with failed's reason when it failed.
func (im *Improver) record(ctx context.Context, id uuid.UUID, p db.FinishImproverPassParams, failed error) error {
	var msg *string
	switch {
	case errors.Is(failed, errNoModel):
		m := failed.Error()
		msg = &m
	case failed != nil:
		m := failedEveryTry
		msg = &m
		im.logger.WarnContext(ctx, "An improver pass failed", "pass", id, "err", failed)
	}
	p.ID, p.Report, p.Error = id, clip(p.Report, reportChars), msg
	row, err := im.store.FinishImproverPass(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // failed as lost meanwhile; that sweep said so
	}
	if err != nil {
		return fmt.Errorf("recording pass %s %s: %w", id, p.Status, err)
	}
	im.logger.InfoContext(ctx, "An improver pass finished", "pass", id, "team", row.TeamID, "agent", row.AgentID,
		"trigger", row.Trigger, "status", p.Status, "proposed", row.Proposed, "screened_out", row.ScreenedOut,
		"cut_short", p.CutShort, "input_tokens", row.InputTokens, "output_tokens", row.OutputTokens)
	return nil
}
