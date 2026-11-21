package improve

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
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

	reportChars       = 4000
	instructionsChars = 32000
	summariesPerKey   = 8
	digestsPerKey     = 20
	wentWellShown     = 10
	recordShown       = 20
	elsewhereShown    = 10
	// A prune candidate looked at is not offered again for this long.
	pruneQuiet = 30 * 24 * time.Hour
	// A pass running longer than this is taken as lost.
	passAlive = 30 * time.Minute
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
	CountPassProposalsOnTarget(ctx context.Context, arg db.CountPassProposalsOnTargetParams) (int32, error)
	AgentPassRunning(ctx context.Context, arg db.AgentPassRunningParams) (bool, error)
	CountTeamPassesSince(ctx context.Context, arg db.CountTeamPassesSinceParams) (int32, error)
	BeginImproverPass(ctx context.Context, arg db.BeginImproverPassParams) (db.ImproverPass, error)
	FinishImproverPass(ctx context.Context, arg db.FinishImproverPassParams) error
	GetImproverPass(ctx context.Context, id uuid.UUID) (db.ImproverPass, error)
	ListAgentRecentRuns(ctx context.Context, arg db.ListAgentRecentRunsParams) ([]uuid.UUID, error)
	ListRunsUse(ctx context.Context, runIDs []uuid.UUID) ([]db.ListRunsUseRow, error)
	ListRecentPruneLooks(ctx context.Context, arg db.ListRecentPruneLooksParams) ([]string, error)
	ListRunDigests(ctx context.Context, runID uuid.UUID) ([]db.RunDigest, error)
}

// Improver runs passes: an agent's patterns read, and proposals written.
type Improver struct {
	store   ImproverStore
	objects improvements.Getter
	judge   Judge
	runner  *agent.Runner
	loops   []string
	logger  *slog.Logger
	now     func() time.Time
}

// NewImprover runs passes through the judge's model endpoint. loops are the
// patterns the brain runs, which a pattern proposal must name.
func NewImprover(s ImproverStore, objects improvements.Getter, j Judge, loops []string, logger *slog.Logger) (*Improver, error) {
	runner, err := agent.New(j.clients, patterns.ReAct())
	if err != nil {
		return nil, fmt.Errorf("building the improver's runner: %w", err)
	}
	return &Improver{store: s, objects: objects, judge: j, runner: runner, loops: loops, logger: logger, now: time.Now}, nil
}

var errNoPass = errors.New("no pass to run")

// Begin records a pass about to run, with the runs it may read, and returns
// its id; errNoPass when there is nothing to run: the team turned off, at its
// day's cap, or the agent already has one under way.
func (im *Improver) Begin(ctx context.Context, plan plannedPass) (uuid.UUID, error) {
	settings, err := im.store.GetImprovementSettings(ctx, plan.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		return uuid.UUID{}, errNoPass
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("reading team %s's settings: %w", plan.TeamID, err)
	}
	now := im.now().UTC()
	busy, err := im.store.AgentPassRunning(ctx, db.AgentPassRunningParams{AgentID: plan.AgentID, Since: now.Add(-passAlive)})
	if err != nil {
		return uuid.UUID{}, err
	}
	used, err := im.store.CountTeamPassesSince(ctx, db.CountTeamPassesSinceParams{
		TeamID: plan.TeamID, Since: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		return uuid.UUID{}, err
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
	})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("recording agent %s's pass: %w", plan.AgentID, err)
	}
	return pass.ID, nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// runsFor chooses the runs a pass may read: the clearest failures of its
// patterns (or, with none, the agent's newest), then successes on the same
// part to contrast.
func (im *Improver) runsFor(ctx context.Context, plan plannedPass) ([]toRead, error) {
	var failures []toRead
	part, target := "", ""
	for i, s := range plan.Keys {
		k, ok := parseKey(s)
		if !ok {
			continue
		}
		if i == 0 {
			part, target = k.Part, k.Target
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
			return nil, err
		}
		for _, r := range rows {
			k := Key{Part: r.Part, Target: r.Target, Cause: r.Cause}
			failures = append(failures, toRead{run: r.RunID, failure: true, rank: 4, at: r.CreatedAt.UnixNano(), line: "went wrong (" + k.String() + ")"})
		}
	}
	well, err := im.store.ListWentWell(ctx, db.ListWentWellParams{AgentID: plan.AgentID, Part: part, Target: target, PageSize: successesToRead})
	if err != nil {
		return nil, err
	}
	var successes []toRead
	for _, r := range well {
		k := Key{Part: r.Part, Target: r.Target, Cause: r.Cause}
		successes = append(successes, toRead{run: r.RunID, at: r.CreatedAt.UnixNano(), line: "went well (" + k.Part + ":" + k.Target + ")"})
	}
	return chooseRuns(failures, successes), nil
}

// passResult is what a pass came to.
type passResult struct {
	report       string
	proposed     int32
	screenedOut  int32
	inputTokens  int64
	outputTokens int64
}

// Run runs a begun pass and records it done. A model or store that cannot
// be reached is returned for the step to try again, the pass still running.
func (im *Improver) Run(ctx context.Context, passID uuid.UUID) error {
	pass, err := im.store.GetImproverPass(ctx, passID)
	if err != nil {
		return fmt.Errorf("reading pass %s: %w", passID, err)
	}
	if pass.Status != passRunning {
		return nil
	}
	settings, err := im.store.GetImprovementSettings(ctx, pass.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		return im.finish(ctx, pass.ID, passDone, passResult{report: "Improvement was turned off before the pass ran."}, nil)
	}
	if err != nil {
		return err
	}
	model, ok := judgeModelOf(settings)
	if !ok {
		return im.finish(ctx, pass.ID, passFailed, passResult{}, errNoModel)
	}

	m, err := im.material(ctx, pass)
	if errors.Is(err, pgx.ErrNoRows) {
		return im.finish(ctx, pass.ID, passDone, passResult{report: "The agent is gone."}, nil)
	}
	if err != nil {
		return err
	}
	t := &passTools{im: im, pass: pass, material: m}
	res, err := im.runner.Run(ctx, agent.Spec{
		Model: model, Pattern: agent.PatternReAct, System: agent.System(improverPrompt),
		Tools: t.tools(), MaxSteps: passSteps,
	}, []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: m.text}}}}, im.judge.endpoint, nil)
	out := t.result()
	out.inputTokens, out.outputTokens = int64(res.Usage.InputTokens), int64(res.Usage.OutputTokens)
	if err != nil && !errors.Is(err, agent.ErrMaxSteps) {
		return fmt.Errorf("running pass %s: %w", pass.ID, err)
	}
	out.report = strings.TrimSpace(res.Output)
	if errors.Is(err, agent.ErrMaxSteps) {
		out.report = strings.TrimSpace(out.report + "\n\nThe pass ran out of steps.")
	}
	return im.finish(ctx, pass.ID, passDone, out, nil)
}

// Fail records a pass that failed every try.
func (im *Improver) Fail(ctx context.Context, passID uuid.UUID, cause error) error {
	return im.finish(ctx, passID, passFailed, passResult{}, cause)
}

const (
	passRunning = "running"
	passDone    = "done"
	passFailed  = "failed"
)

func (im *Improver) finish(ctx context.Context, id uuid.UUID, status string, r passResult, failed error) error {
	var msg *string
	if failed != nil {
		m := clip(failed.Error(), 2000)
		msg = &m
		im.logger.WarnContext(ctx, "An improver pass failed", "pass", id, "err", failed)
	}
	return im.store.FinishImproverPass(ctx, db.FinishImproverPassParams{
		ID: id, Status: status, Report: clip(r.report, reportChars), Error: msg,
		Proposed: r.proposed, ScreenedOut: r.screenedOut,
		InputTokens: r.inputTokens, OutputTokens: r.outputTokens,
	})
}

// material is what a pass reads: the message it starts from, and what its
// tools may reach.
type material struct {
	text    string
	agent   db.Agent
	state   improvements.AgentState
	skills  map[string]db.Skill // the team's, by name
	servers map[string]db.ListTeamServersWithToolsRow
	agents  map[string]uuid.UUID // its delegates, by name
	runs    map[uuid.UUID]bool   // the runs it may read in full
	keys    []string
	prune   []string
}

func (im *Improver) material(ctx context.Context, pass db.ImproverPass) (material, error) {
	m := material{runs: map[uuid.UUID]bool{}, keys: pass.Signatures, prune: pass.Pruned}
	var err error
	if m.agent, err = im.store.GetAgent(ctx, pass.AgentID); err != nil {
		return m, err
	}
	if err := im.store.InTx(ctx, func(q *db.Queries) error {
		m.state, err = improvements.AgentNow(ctx, q, m.agent)
		return err
	}); err != nil {
		return m, fmt.Errorf("reading agent %s: %w", pass.AgentID, err)
	}
	if err := im.lookups(ctx, &m); err != nil {
		return m, err
	}
	for _, id := range pass.RunIds {
		m.runs[id] = true
	}

	var b strings.Builder
	write := func(title string, lines ...string) {
		writeSection(&b, section{title, lines})
	}
	write("Agent", m.agent.Name)
	write("Its description", m.state.Agent.Description)
	write("Its instructions", middle(m.state.Agent.Instructions, instructionsChars))
	write("Its setup", m.setup()...)

	for _, s := range pass.Signatures {
		k, ok := parseKey(s)
		if !ok {
			continue
		}
		rows, err := im.store.ListPatternDigests(ctx, db.ListPatternDigestsParams{
			AgentID: pass.AgentID, Part: k.Part, Target: k.Target, Cause: k.Cause, PageSize: digestsPerKey,
		})
		if err != nil {
			return m, err
		}
		lines := []string{fmt.Sprintf("%d runs went wrong with this signature.", len(rows))}
		for i, r := range rows {
			lines = append(lines, "behaviour: "+r.Behaviour)
			if i < summariesPerKey {
				lines = append(lines, "summary: "+r.Summary)
			}
		}
		write("Pattern "+s, lines...)
	}

	part, target := "", ""
	if len(pass.Signatures) > 0 {
		if k, ok := parseKey(pass.Signatures[0]); ok {
			part, target = k.Part, k.Target
		}
	}
	well, err := im.store.ListWentWell(ctx, db.ListWentWellParams{AgentID: pass.AgentID, Part: part, Target: target, PageSize: wentWellShown})
	if err != nil {
		return m, err
	}
	var wl []string
	for _, w := range well {
		wl = append(wl, fmt.Sprintf("%s:%s: %s", w.Part, w.Target, w.Summary))
	}
	write("What went well", wl...)

	if len(pass.Signatures) == 0 {
		wrong, err := im.store.ListWentWrong(ctx, db.ListWentWrongParams{AgentID: pass.AgentID, PageSize: wentWellShown})
		if err != nil {
			return m, err
		}
		var lines []string
		for _, w := range wrong {
			lines = append(lines, fmt.Sprintf("%s: %s", Key{Part: w.Part, Target: w.Target, Cause: w.Cause}, w.Summary))
		}
		write("What went wrong lately", lines...)
	}

	var rl []string
	for _, id := range pass.RunIds {
		rl = append(rl, id.String())
	}
	write("Runs to read", rl...)

	record, err := im.store.ListAgentProposalRecord(ctx, db.ListAgentProposalRecordParams{AgentID: pass.AgentID, PageSize: recordShown})
	if err != nil {
		return m, err
	}
	var rec []string
	for _, r := range record {
		sig := ""
		if r.Signature != nil {
			sig = *r.Signature
		}
		rec = append(rec, fmt.Sprintf("%s %s (%s, %s): %s. Expected: %s", r.CreatedAt.UTC().Format(time.DateOnly), r.Kind, sig, r.Status, r.Why, r.Hypothesis))
	}
	write("Its record", rec...)

	elsewhere, err := im.store.ListTeamAppliedElsewhere(ctx, db.ListTeamAppliedElsewhereParams{
		TeamID: pass.TeamID, AgentID: pass.AgentID, PageSize: elsewhereShown,
	})
	if err != nil {
		return m, err
	}
	var el []string
	for _, e := range elsewhere {
		el = append(el, fmt.Sprintf("%s, %s: %s", e.Agent, e.Kind, e.Why))
	}
	write("Applied on the team's other agents", el...)
	write("Prune candidates", pass.Pruned...)
	m.text = b.String()
	return m, nil
}

// lookups reads the team's skills, servers and agents by name: what an
// edit names and the setup is shown in.
func (im *Improver) lookups(ctx context.Context, m *material) error {
	skills, err := im.store.ListTeamSkills(ctx, m.agent.TeamID)
	if err != nil {
		return err
	}
	m.skills = map[string]db.Skill{}
	for _, s := range skills {
		m.skills[s.Name] = s
	}
	servers, err := im.store.ListTeamServersWithTools(ctx, m.agent.TeamID)
	if err != nil {
		return err
	}
	m.servers = map[string]db.ListTeamServersWithToolsRow{}
	for _, s := range servers {
		m.servers[s.Name] = s
	}
	agents, err := im.store.ListTeamAgentNames(ctx, db.ListTeamAgentNamesParams{TeamID: m.agent.TeamID, Ids: m.state.Agent.Delegates})
	if err != nil {
		return err
	}
	m.agents = map[string]uuid.UUID{}
	for _, a := range agents {
		m.agents[a.Name] = a.ID
	}
	return nil
}

func (m material) skillName(id uuid.UUID) string {
	for n, s := range m.skills {
		if s.ID == id {
			return n
		}
	}
	return id.String()
}

func (m material) serverName(id uuid.UUID) string {
	for n, s := range m.servers {
		if s.ID == id {
			return n
		}
	}
	return id.String()
}

func (m material) agentName(id uuid.UUID) string {
	for n, a := range m.agents {
		if a == id {
			return n
		}
	}
	return id.String()
}

// setup is the agent's harness beyond its text, a line a part.
func (m material) setup() []string {
	a := m.state.Agent
	lines := []string{
		fmt.Sprintf("pattern %s, at most %d steps and %d steer continuations, max tokens %d", a.Pattern, a.MaxSteps, a.SteerContinuations, a.Model.MaxTokens),
	}
	if a.TokenBudget != nil {
		lines = append(lines, fmt.Sprintf("token budget %d", *a.TokenBudget))
	}
	for _, id := range a.Skills {
		s := m.skills[m.skillName(id)]
		lines = append(lines, fmt.Sprintf("skill %s (%s): %s", m.skillName(id), s.Source, s.Description))
	}
	for _, g := range a.MCPServers {
		tools := "every tool"
		if g.Allow != nil {
			tools = strings.Join(g.Allow, ", ")
		}
		lines = append(lines, fmt.Sprintf("server %s: %s", m.serverName(g.ID), tools))
	}
	for _, n := range m.state.Notes {
		lines = append(lines, fmt.Sprintf("note on %s %s: %s", m.serverName(n.Server), n.Tool, n.Note))
	}
	del := "none"
	if a.Delegation != nil {
		del = *a.Delegation
	}
	var delegates []string
	for _, id := range a.Delegates {
		delegates = append(delegates, m.agentName(id))
	}
	lines = append(lines, fmt.Sprintf("delegation %s %s", del, strings.Join(delegates, ", ")))
	lines = append(lines, fmt.Sprintf("web %v, browser %v", flagOf(a.Web), flagOf(a.Browser)))
	return lines
}

func flagOf(b *bool) bool { return b != nil && *b }

// middle keeps the head and tail of a long text, saying how much is left
// out between.
func middle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	half := n / 2
	return string(r[:half]) + fmt.Sprintf("\n… %d characters left out …\n", len(r)-n) + string(r[len(r)-half:])
}

// passTools are a pass's tools and what they have done.
type passTools struct {
	im       *Improver
	pass     db.ImproverPass
	material material

	mu       sync.Mutex
	proposed int32
	refused  int32
	screened int32
}

func (t *passTools) result() passResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return passResult{proposed: t.proposed, screenedOut: t.screened}
}

func (t *passTools) tools() []agent.Tool {
	return []agent.Tool{
		{
			Name:        "read_run",
			Description: "Read one of the runs listed under Runs to read, in full: the conversation before it, the request, what the agent did, its answer and what came next.",
			Schema:      schemaJSON(object(obj{"run": text()})),
			Invoke:      t.readRun,
		},
		{
			Name:        "read_skill",
			Description: "Read a skill's SKILL.md, or with path one of its text files under references/ or scripts/. Only the team's own skills.",
			Schema:      schemaJSON(obj{"type": "object", "properties": obj{"skill": text(), "path": text()}, "required": []string{"skill"}}),
			Invoke:      t.readSkill,
		},
		{
			Name:        "list_team",
			Description: "List the team's skills and MCP servers with their tools: what a grant may name.",
			Schema:      schemaJSON(obj{"type": "object", "properties": obj{}}),
			Invoke:      t.listTeam,
		},
		{
			Name:        "propose_change",
			Description: proposeDescription,
			Schema:      proposeSchema,
			Invoke:      t.propose,
		},
	}
}

func (t *passTools) readRun(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Run string `json:"run"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	id, parsed := uuid.Parse(strings.TrimSpace(in.Run))
	if parsed != nil || !t.material.runs[id] {
		return "That run is not one of the Runs to read.", nil //nolint:nilerr // the model is told, and may read another
	}
	ev, err := gather(ctx, t.im.store, id, true)
	if err != nil {
		return "", err
	}
	return ev.render(checkBudget), nil
}

func (t *passTools) readSkill(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Skill string `json:"skill"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	s, ok := t.material.skills[strings.TrimSpace(in.Skill)]
	if !ok || s.Source != "upload" {
		return "That is not one of the team's own skills: an imported skill is never changed, and is not read here.", nil
	}
	files, err := t.im.store.ListSkillFiles(ctx, s.ID)
	if err != nil {
		return "", err
	}
	if in.Path == "" {
		var b strings.Builder
		writeSection(&b, section{"SKILL.md of " + s.Name, []string{"description: " + s.Description, s.Body}})
		var paths []string
		for _, f := range files {
			paths = append(paths, fmt.Sprintf("%s (%d bytes)", f.Path, f.Size))
		}
		writeSection(&b, section{"Its files", paths})
		return b.String(), nil
	}
	i := slices.IndexFunc(files, func(f db.SkillFile) bool { return f.Path == in.Path })
	if i < 0 {
		return "The skill has no file " + in.Path + ".", nil
	}
	data, err := t.im.objects.Get(ctx, s.TeamID, files[i].ObjectKey)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeSection(&b, section{s.Name + "/" + in.Path, []string{clip(string(data), checkBudget)}})
	return b.String(), nil
}

func (t *passTools) listTeam(context.Context, json.RawMessage) (string, error) {
	var skills, servers []string
	for _, s := range t.material.skills {
		skills = append(skills, fmt.Sprintf("%s (%s): %s", s.Name, s.Source, s.Description))
	}
	for _, s := range t.material.servers {
		servers = append(servers, fmt.Sprintf("%s: %s", s.Name, strings.Join(s.Tools, ", ")))
	}
	slices.Sort(skills)
	slices.Sort(servers)
	var b strings.Builder
	writeSection(&b, section{"The team's skills", skills})
	writeSection(&b, section{"The team's MCP servers", servers})
	return b.String(), nil
}
