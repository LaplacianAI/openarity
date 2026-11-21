package improve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// A step tried again begins no second pass: the workflow gets the pass it
// began, even once that has finished.
func TestABegunPassIsBegunOnce(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	plan := plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily}
	first, err := im.Begin(t.Context(), plan, "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := im.Begin(t.Context(), plan, "wf-1")
	if err != nil || again != first {
		t.Fatalf("the workflow began again: %s, %v; want %s", again, err, first)
	}
	if err := im.finish(t.Context(), first, passDone, "", nil); err != nil {
		t.Fatal(err)
	}
	if again, err := im.Begin(t.Context(), plan, "wf-1"); err != nil || again != first {
		t.Errorf("after it finished: %s, %v", again, err)
	}
	var n int
	w.scan(t, &n, `SELECT count(*) FROM improver_passes WHERE agent_id = $1`, w.agent)
	if n != 1 {
		t.Errorf("%d passes, want 1", n)
	}
}

// Two passes never run at once for an agent, even begun past the check
// that looks: the database holds one running.
func TestOnePassRunsAnAgentAtATime(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	_, err := w.store.BeginImproverPass(t.Context(), db.BeginImproverPassParams{
		TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily, Signatures: []string{}, Pruned: []string{},
		RunIds: []uuid.UUID{}, WorkflowID: "wf-other",
	})
	if err == nil {
		t.Fatal("a second running pass was recorded")
	}
}

// A pass whose worker stopped answering is failed, saying so, and its
// agent may have another; a live one is left alone.
func TestALostPassIsFailed(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	plan := plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily}
	lost := w.begin(t, im, plan)
	if err := im.FailLost(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.store.GetImproverPass(t.Context(), lost.ID); p.Status != passRunning {
		t.Fatalf("a live pass was failed: %+v", p)
	}
	w.exec(t, `UPDATE improver_passes SET alive_at = now() - interval '31 minutes' WHERE id = $1`, lost.ID)
	if err := im.FailLost(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, _ := w.store.GetImproverPass(t.Context(), lost.ID)
	if p.Status != passFailed || p.Error == nil || *p.Error != failedLost || p.FinishedAt == nil {
		t.Errorf("lost pass = %+v", p)
	}
	if _, err := im.Begin(t.Context(), plan, uuid.NewString()); err != nil {
		t.Errorf("the agent could not begin another: %v", err)
	}
}

// A pass tried again keeps what it stored and spent: it never stores more
// than its share, and its tokens add up.
func TestAPassTriedAgainKeepsCount(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	for range proposalsPerPass {
		w.exec(t, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, signature, before, after, pass_id)
			VALUES ($1, $2, 'description', 'w', 'h', 'invariant', $3, '{}', '{}', $4)`, w.team, w.agent, skipped.String(), pass.ID)
	}
	// The first attempt cannot reach its model after one call.
	w.model.replies = []reply{
		{calls: []agent.ToolCall{call("read_run", map[string]string{"run": runs[0].String()})}},
		{err: errors.New("503")},
	}
	if err := im.Run(t.Context(), pass.ID); err == nil {
		t.Fatal("an unreachable model was not returned")
	}
	tried, _ := w.store.GetImproverPass(t.Context(), pass.ID)
	if tried.Status != passRunning || tried.InputTokens != 120 {
		t.Fatalf("after a failed attempt: %+v, want running with its 120 tokens kept", tried)
	}
	w.model.replies = []reply{
		{calls: []agent.ToolCall{call("propose_change", instructionsEdit(runs[0]))}},
		{text: "Nothing more."},
	}
	if err := im.Run(t.Context(), pass.ID); err != nil {
		t.Fatal(err)
	}
	done, _ := w.store.GetImproverPass(t.Context(), pass.ID)
	if done.Proposed != proposalsPerPass || done.InputTokens != 360 {
		t.Errorf("pass = %+v, want %d proposed and 360 tokens over both attempts", done, proposalsPerPass)
	}
	if said := w.model.asked[len(w.model.asked)-1].Messages; !strings.Contains(said[len(said)-1].Text(), "done proposing") {
		t.Error("a pass at its share was not told it is done proposing")
	}
}

// A pass past its tokens or its time stops, with what it had, and is not
// tried again.
func TestAPassStopsAtItsLimits(t *testing.T) {
	for name, limit := range map[string]func(*world, *Improver){
		// The least a team may set, crossed in a fake model's few calls.
		"tokens": func(w *world, _ *Improver) {
			w.exec(t, `UPDATE team_settings SET improvement_pass_tokens = 20000 WHERE team_id = $1`, w.team)
			w.model.usage = agent.Usage{InputTokens: 15_000, OutputTokens: 1_000}
		},
		"time": func(w *world, im *Improver) {
			w.exec(t, `UPDATE team_settings SET improvement_pass_minutes = 1 WHERE team_id = $1`, w.team)
			im.minute = 50 * time.Millisecond
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			im := w.improver(t)
			limit(w, im)
			var runs []uuid.UUID
			for range 3 {
				runs = append(runs, w.digested(t, skipped, "wrong", false))
			}
			pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
			read := reply{calls: []agent.ToolCall{call("read_run", map[string]string{"run": runs[0].String()})}}
			w.model.replies = []reply{read, read, read, {text: "never reached"}}
			if name == "time" {
				w.model.onAsk = func() { time.Sleep(80 * time.Millisecond) }
			}
			if err := im.Run(t.Context(), pass.ID); err != nil {
				t.Fatalf("a pass at its limit was returned to be tried again: %v", err)
			}
			p, _ := w.store.GetImproverPass(t.Context(), pass.ID)
			if p.Status != passDone || !strings.Contains(p.Report, "The pass stopped") {
				t.Errorf("pass = %+v", p)
			}
		})
	}
}

// A screen that cannot be reached stops the pass for its step to try
// again: it is no refusal, and nothing is stored unscreened.
func TestAnUnreachedScreenStopsThePass(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	w.model.replies = []reply{
		{calls: []agent.ToolCall{call("propose_change", instructionsEdit(runs[0]))}},
		{err: errors.New("503")},
		{err: errors.New("503")},
		{text: "never reached"},
	}
	err := im.Run(t.Context(), pass.ID)
	if err == nil || !strings.Contains(err.Error(), "screening") || errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the screen's failure, retryable", err)
	}
	p, _ := w.store.GetImproverPass(t.Context(), pass.ID)
	if p.Status != passRunning || p.ScreenedOut != 0 || len(w.proposals(t, "waiting")) != 0 {
		t.Errorf("pass = %+v, proposals %d", p, len(w.proposals(t, "waiting")))
	}
}

// What reached a pass may steer it: an outsider in a run it only saw as a
// pattern's summary, never in full and never cited, still bars a widening
// change, and every run it drew on is kept.
func TestAnOutsiderAnywhereInThePassBarsWidening(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var server uuid.UUID
	w.scan(t, &server, `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'crm', 'https://crm.example/mcp') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'lookup')`, server, w.team)
	// The oldest failure: summarised under the pattern, but past the runs
	// a pass reads in full.
	outsider := w.digested(t, skipped, "wrong", true)
	time.Sleep(5 * time.Millisecond)
	var runs []uuid.UUID
	for range failuresToRead {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	if containsID(pass.RunIds, outsider) {
		t.Fatalf("the outsider's run is read in full: %v", pass.RunIds)
	}

	grant := instructionsEdit(runs[0])
	grant["kind"], grant["edit"] = "server_grant", map[string]any{"server": "crm"}
	if out, _ := w.proposeWith(t, im, pass, grant); !strings.Contains(out, "outside the team") {
		t.Errorf("a grant citing a clean run, the outsider's summarised: %q", out)
	}
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	if !m.outsider || len(m.drewOn) != failuresToRead+1 || !containsID(m.drewOn, outsider) {
		t.Errorf("material outsider %v, drew on %v", m.outsider, m.drewOn)
	}
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// A prune answers its own candidate: another skill, or more than the one
// tool, is refused.
func TestAPruneAnswersOnlyItsCandidate(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		edit any
		ok   bool
	}{
		"the skill":         {"skill_drop", map[string]string{"skill": "refunds"}, true},
		"another skill":     {"skill_drop", map[string]string{"skill": "returns"}, false},
		"the tool":          {"tools_narrow", map[string]any{"server": "crm", "remove": []string{"close"}}, false},
		"a narrow of skill": {"tools_narrow", map[string]any{"server": "refunds", "remove": []string{}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(tc.edit)
			if err := pruneAnswers(skillCandidate("refunds"), tc.kind, b); (err == nil) != tc.ok {
				t.Errorf("pruneAnswers = %v, want ok %v", err, tc.ok)
			}
		})
	}
	b, _ := json.Marshal(map[string]any{"server": "crm", "remove": []string{"close"}})
	if err := pruneAnswers(toolCandidate("crm", "close"), "tools_narrow", b); err != nil {
		t.Errorf("the one tool: %v", err)
	}
	b, _ = json.Marshal(map[string]any{"server": "crm", "remove": []string{"close", "lookup"}})
	if err := pruneAnswers(toolCandidate("crm", "close"), "tools_narrow", b); err == nil {
		t.Error("removing more than the candidate was accepted")
	}
}

// A grant of a skill the agent holds, or a drop of one it does not, is
// refused.
func TestASkillChangeMustChangeSomething(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var held uuid.UUID
	w.scan(t, &held, `INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'refunds', 'd', 'b') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, w.agent, held, w.team)
	w.exec(t, `INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'returns', 'd', 'b')`, w.team)
	var runs []uuid.UUID
	for range 3 {
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	for kind, skill := range map[string]string{"skill_grant": "refunds", "skill_drop": "returns"} {
		a := instructionsEdit(runs[0])
		a["kind"], a["edit"] = kind, map[string]string{"skill": skill}
		if out, _ := w.proposeWith(t, im, pass, a); !strings.Contains(out, "already holds") && !strings.Contains(out, "does not hold") {
			t.Errorf("%s %s: %q", kind, skill, out)
		}
	}
}

// Every kind a pass may propose has its builder.
func TestEveryKindIsBuilt(t *testing.T) {
	for _, k := range kinds {
		if _, ok := agentEdits[improvements.Kind(k)]; !ok && k != "skill_edit" {
			t.Errorf("kind %s has no builder", k)
		}
	}
}
