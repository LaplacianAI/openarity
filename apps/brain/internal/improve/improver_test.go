package improve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const instructions = "You answer customers about their orders. Look the order up before you answer. " +
	"Keep answers short and kind. When the customer is satisfied, close the ticket."

var skipped = Key{Part: "instructions", Cause: "skipped_step"}

type noObjects struct{}

func (noObjects) Get(context.Context, uuid.UUID, string) ([]byte, error) {
	return nil, errors.New("no objects here")
}

func (w *world) improver(t *testing.T) *Improver {
	t.Helper()
	judge := NewJudge(func(agent.Endpoint) (agent.ModelClient, error) { return w.model, nil }, agent.Endpoint{})
	im, err := NewImprover(w.store, noObjects{}, judge, []string{"react", "plan"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE agents SET instructions = $2 WHERE id = $1`, w.agent, instructions)
	return im
}

// digested is a finished run of support-bot judged and digested with key k.
func (w *world) digested(t *testing.T, k Key, went string, outsider bool) uuid.UUID {
	t.Helper()
	run := w.finished(t, w.session(t), "1 hour")
	verdict := verdictWrong
	if went == "well" {
		verdict = verdictDone
	}
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest) VALUES ($1, $2, $3, 'm', false, 'done')`,
		run, w.team, verdict)
	w.exec(t, `INSERT INTO run_digests (run_id, agent_id, team_id, went, summary, part, target, cause, behaviour, outsider, model)
		VALUES ($1, $2, $3, $4, 'Asked to refund; refunded without confirming the order.', $5, $6, $7,
		        'closed the ticket before confirming the order', $8, 'm')`,
		run, w.agent, w.team, went, k.Part, k.Target, k.Cause, outsider)
	return run
}

func (w *world) patterns(t *testing.T) []db.ListPatternsRow {
	t.Helper()
	rows, err := w.store.ListPatterns(t.Context(), patternBar)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A signature becomes a pattern in three runs; what no setup change fixes
// never does, nor does a team with it off.
func TestAPatternNeedsThreeRuns(t *testing.T) {
	w := newWorld(t)
	w.digested(t, skipped, "wrong", false)
	w.digested(t, skipped, "wrong", false)
	w.digested(t, skipped, "well", false)
	for range 3 {
		w.digested(t, Key{Part: "none", Cause: "external"}, "wrong", false)
		w.digested(t, Key{Part: "server", Target: "crm", Cause: "external"}, "wrong", false)
	}
	if got := w.patterns(t); len(got) != 0 {
		t.Fatalf("patterns = %+v below the bar", got)
	}
	w.digested(t, skipped, "wrong", false)
	got := w.patterns(t)
	if len(got) != 1 || got[0].AgentID != w.agent || got[0].Runs != 3 || (Key{got[0].Part, got[0].Target, got[0].Cause}) != skipped {
		t.Fatalf("patterns = %+v, want instructions::skipped_step in 3 runs", got)
	}

	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	if got := w.patterns(t); len(got) != 0 {
		t.Errorf("a team with improvement off has patterns: %+v", got)
	}
}

// A proposal on a key, or a finished pass given it, starts its count again.
func TestAPatternCountsFromItsLastLook(t *testing.T) {
	for name, look := range map[string]string{
		"a proposal": `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, signature, before, after)
			VALUES ($1, $2, 'instructions', 'w', 'h', 'invariant', 'instructions::skipped_step', '{}', '{}')`,
		"a pass": `INSERT INTO improver_passes (team_id, agent_id, trigger, signatures, status, finished_at, workflow_id)
			VALUES ($1, $2, 'pattern', '{instructions::skipped_step}', 'done', now(), gen_random_uuid()::text)`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			for range 3 {
				w.digested(t, skipped, "wrong", false)
			}
			time.Sleep(10 * time.Millisecond)
			w.exec(t, look, w.team, w.agent)
			if got := w.patterns(t); len(got) != 0 {
				t.Fatalf("after %s the pattern still counts: %+v", name, got)
			}
			for range 3 {
				w.digested(t, skipped, "wrong", false)
			}
			if got := w.patterns(t); len(got) != 1 {
				t.Errorf("three runs after %s: %+v", name, got)
			}
		})
	}
}

func (w *world) begin(t *testing.T, im *Improver, plan plannedPass) db.ImproverPass {
	t.Helper()
	id, err := im.Begin(t.Context(), plan, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.store.GetImproverPass(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func call(name string, args any) agent.ToolCall {
	b, _ := json.Marshal(args)
	return agent.ToolCall{ID: uuid.NewString(), Name: name, Arguments: b}
}

func instructionsEdit(runs ...uuid.UUID) map[string]any {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.String()
	}
	return map[string]any{
		"kind": "instructions", "signature": skipped.String(), "runs": ids, "scope": "invariant",
		"why":        "In three runs the ticket was closed before the order was confirmed; the run that went well confirmed it first.",
		"hypothesis": "instructions::skipped_step becomes rarer.",
		"edit": map[string]string{
			"replace": "When the customer is satisfied, close the ticket.",
			"with":    "Confirm the order id with the customer before you close the ticket.",
		},
	}
}

const screenOKText = `{"verdict":"ok","reason":"a general rule"}`

// A pass reads a run, proposes one instruction edit, which is screened and
// stored waiting with its pass; the pass is done with its report.
func TestAPassProposesAnEdit(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	well := w.digested(t, Key{Part: "instructions", Cause: "other"}, "well", false)
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	if len(pass.RunIds) != 4 || !slices.Contains(pass.RunIds, well) || pass.Status != passRunning {
		t.Fatalf("pass = %+v, want the 3 failures and the success to read", pass)
	}

	w.model.replies = []reply{
		{calls: []agent.ToolCall{call("read_run", map[string]string{"run": runs[0].String()})}},
		{calls: []agent.ToolCall{call("propose_change", instructionsEdit(runs[0], runs[1]))}},
		{text: screenOKText},
		{text: "One pattern: the ticket is closed too early. Proposed confirming the order first."},
	}
	if err := im.Run(t.Context(), pass.ID); err != nil {
		t.Fatal(err)
	}

	done, _ := w.store.GetImproverPass(t.Context(), pass.ID)
	if done.Status != passDone || done.Proposed != 1 || !strings.Contains(done.Report, "closed too early") || done.InputTokens == 0 {
		t.Errorf("pass = %+v", done)
	}
	rows, err := w.store.ListImprovementProposals(t.Context(), db.ListImprovementProposalsParams{TeamID: w.team, Statuses: []string{"waiting"}, PageSize: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("proposals = %+v, %v", rows, err)
	}
	p := rows[0]
	if p.PassID == nil || *p.PassID != pass.ID || p.Signature == nil || *p.Signature != skipped.String() || len(p.RunIds) != 2 || p.Outsider {
		t.Errorf("proposal = %+v", p)
	}
	var after improvements.Snapshot
	if err := json.Unmarshal(p.After, &after); err != nil || !strings.Contains(after.Agent.Agent.Instructions, "Confirm the order id") ||
		strings.Contains(after.Agent.Agent.Instructions, "When the customer is satisfied") {
		t.Errorf("after = %+v, %v", after.Agent, err)
	}
	if read := w.model.asked[1].Messages; !strings.Contains(read[len(read)-1].Text(), "| ") {
		t.Error("read_run did not hand back marked evidence")
	}
}

// proposeWith calls propose_change once in a begun pass, the screen
// answering screen.
func (w *world) proposeWith(t *testing.T, im *Improver, pass db.ImproverPass, args any, screen ...reply) (string, *passTools) {
	t.Helper()
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	tools := &passTools{im: im, pass: pass, material: m}
	w.model.replies = screen
	b, _ := json.Marshal(args)
	out, err := tools.propose(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	return out, tools
}

func (w *world) proposals(t *testing.T, status string) []db.ImprovementProposal {
	t.Helper()
	rows, err := w.store.ListImprovementProposals(t.Context(), db.ListImprovementProposalsParams{TeamID: w.team, Statuses: []string{status}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Every rule refuses without storing, and says why.
func TestAProposalIsRefused(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	other := w.finished(t, w.session(t), "1 hour")

	with := func(change func(map[string]any)) map[string]any {
		a := instructionsEdit(runs[0])
		change(a)
		return a
	}
	for name, tc := range map[string]struct {
		args   map[string]any
		screen []reply
		says   string
	}{
		"another signature": {with(func(a map[string]any) { a["signature"] = "instructions::gave_up" }), nil, "not one of this pass's patterns"},
		"a run not given":   {with(func(a map[string]any) { a["runs"] = []string{other.String()} }), nil, "not one of the Runs to read"},
		"no runs":           {with(func(a map[string]any) { a["runs"] = []string{} }), nil, "at least one"},
		"no hypothesis":     {with(func(a map[string]any) { a["hypothesis"] = " " }), nil, "says why"},
		"a passage twice": {with(func(a map[string]any) {
			a["edit"] = map[string]string{"replace": "the ", "with": "a "}
		}), nil, "more than once"},
		"a built-in field": {with(func(a map[string]any) {
			a["kind"], a["edit"] = "pattern", map[string]string{"pattern": "code"}
		}), nil, ""},
		"an unknown skill": {with(func(a map[string]any) {
			a["kind"], a["edit"] = "skill_grant", map[string]string{"skill": "nope"}
		}), nil, "no skill"},
		"an unknown skill held": {with(func(a map[string]any) {
			a["kind"], a["edit"] = "skill_drop", map[string]string{"skill": "nope"}
		}), nil, "no skill"},
		"screened out":    {instructionsEdit(runs[0]), []reply{{text: `{"verdict":"refuse","reason":"it names a customer"}`}}, "names a customer"},
		"screen unusable": {instructionsEdit(runs[0]), []reply{{text: `{"verdict":"maybe"}`}, {text: `not json`}}, "could not be screened"},
	} {
		t.Run(name, func(t *testing.T) {
			out, tools := w.proposeWith(t, im, pass, tc.args, tc.screen...)
			if !strings.HasPrefix(out, "Not stored: ") || !strings.Contains(out, tc.says) {
				t.Errorf("answer = %q, want a refusal saying %q", out, tc.says)
			}
			if n := len(w.proposals(t, "waiting")); n != 0 {
				t.Errorf("a refused proposal was stored (%d)", n)
			}
			if name == "screened out" && tools.screenedOut != 1 {
				t.Errorf("screened out = %d", tools.screenedOut)
			}
			if tools.refused != 1 {
				t.Errorf("refusals counted = %d, want 1", tools.refused)
			}
		})
	}
}

// A conversation an outsider spoke in never widens access; an instruction
// edit from it is still proposed, marked.
func TestAnOutsiderRunNeverWidensAccess(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var server uuid.UUID
	w.scan(t, &server, `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'crm', 'https://crm.example/mcp') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'lookup')`, server, w.team)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", true))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])

	grant := instructionsEdit(runs[0])
	grant["kind"], grant["edit"] = "server_grant", map[string]any{"server": "crm"}
	if out, _ := w.proposeWith(t, im, pass, grant); !strings.Contains(out, "outside the team") {
		t.Errorf("a server grant from an outsider's run: %q", out)
	}
	if out, _ := w.proposeWith(t, im, pass, instructionsEdit(runs[0]), reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatalf("an instruction edit from an outsider's run: %q", out)
	}
	if got := w.proposals(t, "waiting"); len(got) != 1 || !got[0].Outsider {
		t.Errorf("proposals = %+v, want one marked outsider", got)
	}
}

// One proposal a target in a pass. A later pass's change of the same kind
// on the same pattern replaces one still waiting; a change of another kind,
// and a person's own, wait on.
func TestANewerProposalReplacesTheOlder(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	first := w.begin(t, im, patternPasses(w.patterns(t))[0])
	if out, _ := w.proposeWith(t, im, first, instructionsEdit(runs[0]), reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatal(out)
	}
	desc := instructionsEdit(runs[0])
	desc["kind"], desc["edit"] = "description", map[string]string{"text": "Answers order questions and confirms the order first."}
	if out, _ := w.proposeWith(t, im, first, desc, reply{text: screenOKText}); !strings.Contains(out, "already proposed a change to that target") {
		t.Errorf("a second change to the agent in one pass: %q", out)
	}
	if err := im.finish(t.Context(), first.ID, passDone, "", nil); err != nil {
		t.Fatal(err)
	}
	// A person's own proposal on the same pattern.
	w.exec(t, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, signature, before, after)
		VALUES ($1, $2, 'instructions', 'w', 'h', 'invariant', $3, '{}', '{}')`, w.team, w.agent, skipped.String())

	second := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerPattern, Keys: []string{skipped.String()}})
	if out, _ := w.proposeWith(t, im, second, desc, reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatal(out)
	}
	if got := len(w.proposals(t, "withdrawn")); got != 0 {
		t.Errorf("a description withdrew %d proposals of other kinds", got)
	}
	if err := im.finish(t.Context(), second.ID, passDone, "", nil); err != nil {
		t.Fatal(err)
	}
	third := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerPattern, Keys: []string{skipped.String()}})
	again := instructionsEdit(runs[1])
	again["edit"] = map[string]string{"replace": "Keep answers short and kind.", "with": "Keep answers short, kind, and confirm the order."}
	if out, _ := w.proposeWith(t, im, third, again, reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatal(out)
	}
	waiting, withdrawn := w.proposals(t, "waiting"), w.proposals(t, "withdrawn")
	if len(withdrawn) != 1 || withdrawn[0].Kind != "instructions" || withdrawn[0].PassID == nil || *withdrawn[0].PassID != first.ID {
		t.Errorf("withdrawn %+v, want only the first pass's instruction edit", withdrawn)
	}
	if len(waiting) != 3 {
		t.Errorf("waiting %d, want the person's, the description and the newest edit", len(waiting))
	}
}

// Nothing begins for a team at its day's cap, an agent with a pass under
// way, or a team turned off.
func TestAPassBeginsOnlyWhenItMay(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	plan := plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily}
	first := w.begin(t, im, plan)
	if _, err := im.Begin(t.Context(), plan, uuid.NewString()); !errors.Is(err, errNoPass) {
		t.Errorf("a second pass began beside one running: %v", err)
	}
	if err := im.finish(t.Context(), first.ID, passDone, "", nil); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE team_settings SET improvement_daily_passes = 1 WHERE team_id = $1`, w.team)
	if _, err := im.Begin(t.Context(), plan, uuid.NewString()); !errors.Is(err, errNoPass) {
		t.Errorf("a pass began past the day's cap: %v", err)
	}
	w.exec(t, `UPDATE team_settings SET improvement_daily_passes = 20, improvement = false WHERE team_id = $1`, w.team)
	if _, err := im.Begin(t.Context(), plan, uuid.NewString()); !errors.Is(err, errNoPass) {
		t.Errorf("a pass began with improvement off: %v", err)
	}
}

// A team with no model to run on fails its pass, saying so; a run that
// cannot reach its model is returned to be tried again, the pass running.
func TestAPassThatCannotRun(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	w.model.replies = []reply{{err: errors.New("503")}}
	if err := im.Run(t.Context(), pass.ID); err == nil {
		t.Fatal("an unreachable model was not returned")
	}
	if p, _ := w.store.GetImproverPass(t.Context(), pass.ID); p.Status != passRunning {
		t.Errorf("status = %s after a 503, want still running", p.Status)
	}

	w.exec(t, `UPDATE team_settings SET model_name = NULL, model_max_tokens = NULL WHERE team_id = $1`, w.team)
	if err := im.Run(t.Context(), pass.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.store.GetImproverPass(t.Context(), pass.ID); p.Status != passFailed || p.Error == nil {
		t.Errorf("pass = %+v, want failed with its reason", p)
	}
}

// What an agent held in each of its last 50 runs and never used is offered
// for pruning, once; what it used is not.
func TestPruneCandidatesComeFromRuns(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var skill uuid.UUID
	w.scan(t, &skill, `INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'refunds', 'How refunds go', 'Refund within 30 days.') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, w.agent, skill, w.team)
	var server uuid.UUID
	w.scan(t, &server, `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'orders', 'https://orders.example/mcp') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'lookup'), ($1, $2, 'cancel')`, server, w.team)
	w.exec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, w.agent, server, w.team)

	s := w.session(t)
	for i := range pruneWindow - 1 {
		run := w.finished(t, s, "2 hours")
		w.exec(t, `UPDATE run_events SET payload = '{"tools":["orders__lookup","orders__cancel"],"skills":["refunds"]}' WHERE run_id = $1 AND kind = 'started'`, run)
		_ = i
	}
	if got, err := im.pruneFor(t.Context(), w.agent); err != nil || got != nil {
		t.Fatalf("with 49 runs: %v %v", got, err)
	}
	run := w.finished(t, s, "1 hour")
	w.exec(t, `UPDATE run_events SET payload = '{"tools":["orders__lookup","orders__cancel"],"skills":["refunds"]}' WHERE run_id = $1 AND kind = 'started'`, run)

	got, err := im.pruneFor(t.Context(), w.agent)
	if err != nil || !slices.Equal(got, []string{"skill:refunds", "tool:orders/cancel"}) {
		t.Fatalf("candidates = %v, %v; want the unopened skill and the uncalled tool", got, err)
	}
	w.exec(t, `INSERT INTO improver_passes (team_id, agent_id, trigger, pruned, status, finished_at, workflow_id)
		VALUES ($1, $2, 'daily', '{skill:refunds}', 'done', now(), gen_random_uuid()::text)`, w.team, w.agent)
	if got, _ := im.pruneFor(t.Context(), w.agent); !slices.Equal(got, []string{"tool:orders/cancel"}) {
		t.Errorf("a candidate looked at was offered again: %v", got)
	}
}

// A prune proposal names its candidate, and only skill_drop or
// tools_narrow answers one.
func TestAPruneIsProposedOnItsCandidate(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var skill uuid.UUID
	w.scan(t, &skill, `INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'refunds', 'How refunds go', 'Refund within 30 days.') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, w.agent, skill, w.team)
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily, Prune: []string{"skill:refunds"}})

	drop := map[string]any{
		"kind": "skill_drop", "signature": "prune:skill:refunds", "runs": []string{}, "scope": "invariant",
		"why": "Held in its last 50 runs and never opened.", "hypothesis": "Runs read one skill less.",
		"edit": map[string]string{"skill": "refunds"},
	}
	wrong := maps(drop, "kind", "instructions")
	if out, _ := w.proposeWith(t, im, pass, wrong); !strings.Contains(out, "answered by dropping that skill") {
		t.Errorf("an instruction edit on a prune candidate: %q", out)
	}
	if out, _ := w.proposeWith(t, im, pass, maps(drop, "signature", "prune:skill:other")); !strings.Contains(out, "not one of this pass's prune candidates") {
		t.Errorf("another candidate: %q", out)
	}
	if out, _ := w.proposeWith(t, im, pass, drop, reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatalf("the drop: %q", out)
	}
	if got, _ := im.pruneFor(t.Context(), w.agent); got != nil {
		t.Errorf("a candidate with a proposal waiting was offered again: %v", got)
	}
}

func maps(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for key, val := range m {
		out[key] = val
	}
	out[k] = v
	return out
}

// read_run and read_skill reach only what the pass was given.
func TestAPassReadsOnlyWhatItWasGiven(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	for range 3 {
		w.digested(t, skipped, "wrong", false)
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	tools := &passTools{im: im, pass: pass, material: m}
	other := w.finished(t, w.session(t), "1 hour")
	out, err := tools.readRun(t.Context(), json.RawMessage(`{"run":"`+other.String()+`"}`))
	if err != nil || !strings.Contains(out, "not one of the Runs to read") {
		t.Errorf("read_run of another run = %q, %v", out, err)
	}
	w.exec(t, `INSERT INTO skills (team_id, name, description, body, source, source_ref, source_sha) VALUES ($1, 'imported', 'd', 'b', 'github', 'o/r', 'abc')`, w.team)
	m, _ = im.material(t.Context(), pass)
	tools.material = m
	out, err = tools.readSkill(t.Context(), json.RawMessage(`{"skill":"imported"}`))
	if err != nil || !strings.Contains(out, "never changed") {
		t.Errorf("read_skill of an import = %q, %v", out, err)
	}
	if !strings.Contains(m.text, "## Pattern instructions::skipped_step") || !strings.Contains(m.text, "| closed the ticket") && !strings.Contains(m.text, "| behaviour: closed the ticket") {
		t.Errorf("material:\n%s", m.text)
	}
	if _, err := w.store.GetImproverPass(t.Context(), uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("an unknown pass: %v", err)
	}
}

// A run judged done is digested anyway when one tool failed twice in it;
// a single failure, or two different tools failing once each, is not.
func TestARunThatFoughtAToolIsDigested(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `UPDATE team_settings SET improvement_sample_share = 0 WHERE team_id = $1`, w.team)
	fail := func(run uuid.UUID, seq int, tool string) {
		w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, $2, 'tool_result', $3)`,
			run, seq, `{"name":"`+tool+`","error":"timed out"}`)
	}
	for name, tc := range map[string]struct {
		fails []string
		want  string
	}{
		"one failure":            {[]string{"orders__lookup"}, digestSkipped},
		"two tools, once each":   {[]string{"orders__lookup", "crm__close"}, digestSkipped},
		"one tool, failed twice": {[]string{"orders__lookup", "orders__lookup"}, digestPending},
	} {
		t.Run(name, func(t *testing.T) {
			run := w.finished(t, w.session(t), "11 minutes")
			for i, tool := range tc.fails {
				fail(run, 10+i, tool)
			}
			w.model.replies = []reply{{text: goodAnswer}}
			if err := w.checker.Check(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if o, _ := w.store.GetRunOutcome(t.Context(), run); o.Verdict != verdictDone || o.Digest != tc.want {
				t.Errorf("verdict %s, digest %s; want done and %s", o.Verdict, o.Digest, tc.want)
			}
		})
	}
}
