package blueprints

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// builderRun is a builder run in a session of its own kind, owned by owner
// when the session is direct.
func (w *world) builderRun(t *testing.T, kind string, owner *uuid.UUID) db.Run {
	t.Helper()
	var session uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind, user_id) VALUES ($1, $2, $3) RETURNING id`, w.team, kind, owner).Scan(&session); err != nil {
		t.Fatalf("session: %v", err)
	}
	run, err := w.store.CreateBuilderRun(t.Context(), db.CreateBuilderRunParams{
		ID: uuid.New(), SessionID: session, Input: "build me a team", TeamID: w.team, UserID: owner,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return run
}

func (w *world) newUser(t *testing.T, subject string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('dev', $1) RETURNING id`, subject).Scan(&id); err != nil {
		t.Fatalf("user: %v", err)
	}
	return id
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// propose is the builder's call, as the runtime makes it.
func (w *world) propose(t *testing.T, run db.Run, bp any) (Proposed, error) {
	t.Helper()
	return NewWorkshop(w, &w.probe, loops).Propose(t.Context(), run, raw(t, bp), "a support team")
}

func (w *world) proposal(t *testing.T, id uuid.UUID) (proposal, int) {
	t.Helper()
	rec := w.serve(t, httptest.NewRequestWithContext(t.Context(), "GET", "/teams/"+w.team.String()+"/proposals/"+id.String(), nil))
	var p proposal
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
	}
	return p, rec.Code
}

func (w *world) applyProposal(t *testing.T, id uuid.UUID, digest string) *httptest.ResponseRecorder {
	t.Helper()
	body := raw(t, applyProposal{PlanDigest: digest})
	req := httptest.NewRequestWithContext(t.Context(), "POST",
		"/teams/"+w.team.String()+"/proposals/"+id.String()+"/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return w.serve(t, req)
}

func (w *world) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.raw.QueryRow(t.Context(), `SELECT state FROM proposals WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A skill as the builder writes it: text files.
var builtSkill = map[string]any{"files": []map[string]string{
	{"path": "SKILL.md", "text": "---\nname: support-faq\ndescription: Answers from the FAQ.\n---\nRead faq.md first.\n"},
	{"path": "faq.md", "text": "Q: refunds? A: within 30 days."},
}}

func builtTeam() map[string]any {
	triage := map[string]any{
		"name": "triage", "instructions": "Sort the email.", "pattern": "react", "max_steps": 10,
		"model": map[string]any{"name": "m", "max_tokens": 1024}, "skills": []string{"support-faq"},
	}
	return map[string]any{"skills": []any{builtSkill}, "agents": []any{triage}}
}

// The whole of B3's promise: the builder proposes, nothing is written, a
// person applies the plan they read, once.
func TestAProposalIsWrittenOnlyWhenAPersonAppliesIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)

	got, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if got.Changes != 2 || got.Refusals != 0 || !strings.Contains(got.Report, "create agent triage") {
		t.Fatalf("proposed = %+v, want two creates and the report to say so", got)
	}
	if n := w.count(t, "SELECT count(*) FROM agents WHERE team_id = $1"); n != 0 {
		t.Fatalf("proposing wrote %d agents", n)
	}

	p, code := w.proposal(t, got.ID)
	if code != http.StatusOK || p.State != "open" || p.Plan == nil {
		t.Fatalf("proposal: %d %+v", code, p)
	}
	if rec := w.applyProposal(t, got.ID, p.Plan.Digest); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if n := w.count(t, "SELECT count(*) FROM agent_skills WHERE team_id = $1"); n != 1 {
		t.Errorf("after apply, %d skill grants, want triage holding support-faq", n)
	}
	if s := w.state(t, got.ID); s != "applied" {
		t.Errorf("state = %s, want applied", s)
	}

	before := w.written(t)
	if rec := w.applyProposal(t, got.ID, p.Plan.Digest); rec.Code != http.StatusConflict {
		t.Errorf("second apply: %d, want 409", rec.Code)
	}
	if after := w.written(t); !equalCounts(before, after) {
		t.Errorf("the second apply wrote: %v → %v", before, after)
	}
}

func TestANewerProposalSupersedesTheOpenOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)

	first, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatal(err)
	}
	if w.state(t, first.ID) != "superseded" || w.state(t, second.ID) != "open" {
		t.Errorf("states = %s, %s; want superseded, open", w.state(t, first.ID), w.state(t, second.ID))
	}
	if rec := w.applyProposal(t, first.ID, "sha256:any"); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "superseded") {
		t.Errorf("apply superseded: %d %s, want 409 saying so", rec.Code, rec.Body)
	}
}

func TestARefusedProposalIsRecordedAndSaysWhy(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)

	bp := builtTeam()
	delete(bp, "skills")
	got, err := w.propose(t, run, bp)
	if err != nil {
		t.Fatal(err)
	}
	if got.Refusals != 1 || !strings.Contains(got.Report, `REFUSED agent triage`) {
		t.Errorf("proposed = %+v, want triage refused for its missing skill", got)
	}
	p, _ := w.proposal(t, got.ID)
	if rec := w.applyProposal(t, got.ID, p.Plan.Digest); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("apply a refused proposal: %d, want 422", rec.Code)
	}
	if w.state(t, got.ID) != "open" {
		t.Error("a refused apply changed the proposal's state")
	}
}

func TestAProposalTheModelGotWrongIsRefusedBeforeItIsRecorded(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)

	for name, bp := range map[string]any{
		"an unknown key": map[string]any{"agentz": []any{}, "skills": []any{builtSkill}},
		"nothing":        map[string]any{},
	} {
		if _, err := w.propose(t, run, bp); !errors.Is(err, ErrProposal) {
			t.Errorf("%s: err = %v, want ErrProposal", name, err)
		}
	}
	if n := w.count(t, "SELECT count(*) FROM proposals WHERE team_id = $1"); n != 0 {
		t.Errorf("%d proposals recorded", n)
	}
}

func TestARunProposesAtMostFiveTimes(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)
	for i := range maxProposals {
		if _, err := w.propose(t, run, builtTeam()); err != nil {
			t.Fatalf("proposal %d: %v", i+1, err)
		}
	}
	if _, err := w.propose(t, run, builtTeam()); !errors.Is(err, ErrProposal) {
		t.Errorf("sixth: err = %v, want ErrProposal", err)
	}
}

// The plan shown is the reader's: one who may not write agents sees that
// refused, and applying as them writes nothing.
func TestAProposalIsPlannedAndAppliedAsTheReader(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)
	got, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatal(err)
	}

	w.authz = &fakeAuthz{holds: map[authz.Action]bool{"skill:write": true}}
	p, _ := w.proposal(t, got.ID)
	if len(p.Plan.Refusals) != 1 || !strings.Contains(p.Plan.Refusals[0].Message, "agent:write") {
		t.Fatalf("refusals = %+v, want agent:write named", p.Plan.Refusals)
	}
	before := w.written(t)
	if rec := w.applyProposal(t, got.ID, p.Plan.Digest); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("apply without agent:write: %d, want 422", rec.Code)
	}
	if after := w.written(t); !equalCounts(before, after) || w.state(t, got.ID) != "open" {
		t.Errorf("a refused apply wrote or closed the proposal: %v → %v", before, after)
	}
}

func TestAProposalOverATeamThatChangedIsNotApplied(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	run := w.builderRun(t, "group", nil)
	got, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := w.proposal(t, got.ID)

	if rec := w.apply(t, Blueprint{Skills: []skillSpec{faqSkill("changed meanwhile")}}); rec.Code != http.StatusOK {
		t.Fatalf("the change: %d %s", rec.Code, rec.Body)
	}
	if rec := w.applyProposal(t, got.ID, p.Plan.Digest); rec.Code != http.StatusConflict {
		t.Errorf("apply over a changed team: %d, want 409 with the new plan", rec.Code)
	}
	if w.state(t, got.ID) != "open" {
		t.Error("the proposal closed although nothing was applied")
	}
}

// A proposal in someone's direct session is theirs, as the session is.
func TestAProposalInADirectSessionIsSeenByItsPersonOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	owner := w.newUser(t, "owner")
	run := w.builderRun(t, "direct", &owner)
	got, err := w.propose(t, run, builtTeam())
	if err != nil {
		t.Fatal(err)
	}

	w.user = w.newUser(t, "someone-else")
	if _, code := w.proposal(t, got.ID); code != http.StatusNotFound {
		t.Errorf("another member reads it: %d, want 404", code)
	}
	if rec := w.applyProposal(t, got.ID, "sha256:any"); rec.Code != http.StatusNotFound {
		t.Errorf("another member applies it: %d, want 404", rec.Code)
	}
	list := w.serve(t, httptest.NewRequestWithContext(t.Context(), "GET", "/teams/"+w.team.String()+"/proposals", nil))
	if strings.Contains(list.Body.String(), got.ID.String()) {
		t.Error("another member's list holds it")
	}

	w.user = owner
	if _, code := w.proposal(t, got.ID); code != http.StatusOK {
		t.Errorf("its person reads it: %d, want 200", code)
	}
}

func TestProposalsAreListedBySession(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a, b := w.builderRun(t, "group", nil), w.builderRun(t, "group", nil)
	pa, _ := w.propose(t, a, builtTeam())
	pb, _ := w.propose(t, b, builtTeam())

	rec := w.serve(t, httptest.NewRequestWithContext(t.Context(), "GET",
		"/teams/"+w.team.String()+"/proposals?session="+a.SessionID.String(), nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), pa.ID.String()) ||
		strings.Contains(rec.Body.String(), pb.ID.String()) {
		t.Errorf("session list: %d %s, want only session a's", rec.Code, rec.Body)
	}
}

// What the builder reads: the blueprint, skills by name and description, and
// each server's tools by name.
func TestTheBuilderReadsTheTeamAsABlueprint(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO mcp_tools (mcp_server_id, team_id, name, description, input_schema)
		SELECT id, team_id, 'create_ticket', 'd', '{}' FROM mcp_servers WHERE team_id = $1`, w.team); err != nil {
		t.Fatalf("tool: %v", err)
	}

	out, err := NewWorkshop(w, &w.probe, loops).Team(t.Context(), w.team)
	if err != nil {
		t.Fatal(err)
	}
	var v teamView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(v.Blueprint.Agents) != 2 || len(v.Skills) != 1 || v.Skills[0].Description == "" ||
		!equalNames(v.Tools["helpdesk"], "create_ticket") {
		t.Errorf("team = %s, want two agents, the skill described, helpdesk's tool", out)
	}
}

func TestAnAppliedProposalAsksItsNewServersForTheirTools(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.refresh.tools = map[string][]string{"billing": {"refund"}}
	run := w.builderRun(t, "group", nil)

	bp := builtTeam()
	bp["mcp_servers"] = []any{map[string]any{"name": "billing", "url": "https://billing.example/mcp"}}
	got, err := w.propose(t, run, bp)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if n := len(w.refresh.names()); n != 0 {
		t.Fatalf("proposing asked %d servers; only an apply may", n)
	}
	p, _ := w.proposal(t, got.ID)
	rec := w.applyProposal(t, got.ID, p.Plan.Digest)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	d := discoveredBy(appliedOf(t, rec.Body.Bytes()))["billing"]
	if len(d.Tools) != 1 || d.Tools[0] != "refund" {
		t.Errorf("billing: %+v, want its tool", d)
	}
}
