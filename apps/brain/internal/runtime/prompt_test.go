package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// waitingRun is a channel run a worker holds, started by a person of the given
// role, with one request asked for them: what a run looks like just before
// it parks in a conversation.
type waitingRun struct {
	store *store.Store
	raw   *pgx.Conn
	run   db.Run
	user  uuid.UUID
	req   db.Approval
}

func waitingIn(t *testing.T, role string, requireOther bool) waitingRun {
	t.Helper()
	s, raw, _ := liveSchema(t)
	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	var channelID, sessionID uuid.UUID
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO channels (team_id, provider, name) VALUES ($1, 'telegram', 'bot') RETURNING id`,
		team.ID).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, '42', 'direct') RETURNING id`,
		team.ID, channelID).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id := uuid.New()
	if _, err := raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, hub, reply_to_channel)
		VALUES ($1, $2, $3, $1, 'hub', 'open an issue about the disk', true, true)`, id, team.ID, sessionID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	row, err := s.GetRun(t.Context(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	var user uuid.UUID
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('dev', $1) RETURNING id`, uuid.NewString()).Scan(&user); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := raw.Exec(t.Context(),
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`, row.TeamID, user, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := raw.Exec(t.Context(), `
		UPDATE runs SET user_id = $2, status = 'running', worker = $3, heartbeat_at = now(), started_at = now()
		WHERE id = $1`, row.ID, user, worker); err != nil {
		t.Fatalf("hold the run: %v", err)
	}
	a, err := s.InsertApproval(t.Context(), db.InsertApprovalParams{
		TeamID: row.TeamID, RunID: row.ID, RootRunID: row.ID, CallID: "c1", Tool: "github__create_issue",
		Class: "write", Arguments: []byte(`{}`), Summary: "title: Disk full", Code: "7K2F",
		RequestedFor: &user, ExpiresAt: time.Now().Add(time.Hour), RequireOther: requireOther,
	})
	if err != nil {
		t.Fatalf("InsertApproval: %v", err)
	}
	return waitingRun{store: s, raw: raw, run: row, user: user, req: a}
}

// park parks the run with what it said, as the executor does.
func (a waitingRun) park(t *testing.T, said string) int32 {
	t.Helper()
	parked, again, asked, err := parkAndAsk(t.Context(), a.store, a.run.ID, worker, false, &said, []byte(`{}`))
	if err != nil || !parked || again != nil {
		t.Fatalf("park = parked %v, queued %v, %v; want parked and waiting", parked, again, err)
	}
	return asked
}

type promptRow struct {
	text   string
	seq    *int32
	detail approval.Prompt
}

func (a waitingRun) prompts(t *testing.T) []promptRow {
	t.Helper()
	rows, err := a.raw.Query(t.Context(),
		`SELECT text, event_seq, detail FROM replies WHERE run_id = $1 AND kind = 'approval' ORDER BY id`, a.run.ID)
	if err != nil {
		t.Fatalf("read the prompts: %v", err)
	}
	defer rows.Close()
	var out []promptRow
	for rows.Next() {
		var p promptRow
		var detail []byte
		if err := rows.Scan(&p.text, &p.seq, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal(detail, &p.detail); err != nil {
			t.Fatalf("the prompt's detail %s: %v", detail, err)
		}
		out = append(out, p)
	}
	return out
}

// A person who may approve their own is asked in the conversation, in the
// agent's words and the brain's footer, keyed by the waiting event.
func TestAWaitingRunAsksItsPersonInTheirConversation(t *testing.T) {
	a := waitingIn(t, "member", false)

	seq := a.park(t, "I'd like to open an issue about the full disk. OK?")
	if seq == 0 {
		t.Fatal("no prompt was queued")
	}

	got := a.prompts(t)
	if len(got) != 1 {
		t.Fatalf("%d prompts, want 1", len(got))
	}
	p := got[0]
	if p.seq == nil || *p.seq != seq {
		t.Errorf("the prompt is keyed %v, want the waiting event %d", p.seq, seq)
	}
	if !strings.HasPrefix(p.text, "I'd like to open an issue about the full disk. OK?\n\n") {
		t.Errorf("the prompt does not begin with the agent's words: %q", p.text)
	}
	if !strings.Contains(p.text, "create_issue on github · reply yes 7K2F or no 7K2F") {
		t.Errorf("the footer does not say how to answer: %q", p.text)
	}
	want := approval.PromptRequest{ID: a.req.ID.String(), Code: "7K2F", Tool: "github__create_issue", Summary: "title: Disk full", Ask: true}
	if r := p.detail.Requests; len(r) != 1 || r[0].ID != want.ID || r[0].Code != want.Code || !r[0].Ask || r[0].Summary != want.Summary {
		t.Errorf("the prompt's requests are %+v, want %+v", r, want)
	}
}

// Someone who may not approve their own, or a request a rule wants someone
// else to answer, is told an approver was asked: no code reaches the chat.
func TestARequestForAnApproverIsOnlySaidToHaveGoneToOne(t *testing.T) {
	for name, c := range map[string]struct {
		role         string
		requireOther bool
	}{
		"a guest":            {role: "guest"},
		"someone else's eye": {role: "admin", requireOther: true},
	} {
		t.Run(name, func(t *testing.T) {
			a := waitingIn(t, c.role, c.requireOther)
			a.park(t, "I need an approver's OK to open the issue.")

			p := a.prompts(t)[0]
			if strings.Contains(p.text, "7K2F") || !strings.Contains(p.text, "sent to an approver") {
				t.Errorf("the prompt says %q", p.text)
			}
			if r := p.detail.Requests[0]; r.Ask || r.Code != "" {
				t.Errorf("the request carries %+v, want no code and not asked here", r)
			}
		})
	}
}

// A run whose requests were all answered before it parked goes straight back
// to the queue: there is nothing to ask.
func TestARunAnsweredBeforeItParksAsksNothing(t *testing.T) {
	a := waitingIn(t, "member", false)
	if _, err := a.raw.Exec(t.Context(),
		`UPDATE approvals SET status = 'approved', decided_at = now(), decided_via = 'dashboard', decided_by = $2, decision_scope = 'once' WHERE id = $1`,
		a.req.ID, a.user); err != nil {
		t.Fatalf("answer: %v", err)
	}
	said := "I'd like to open an issue."
	parked, again, asked, err := parkAndAsk(t.Context(), a.store, a.run.ID, worker, false, &said, []byte(`{}`))
	if err != nil || !parked || again == nil || asked != 0 {
		t.Fatalf("park = parked %v, queued %v, asked %d, %v; want queued again and nothing asked", parked, again, asked, err)
	}
	if got := a.prompts(t); len(got) != 0 {
		t.Errorf("%d prompts for a run with nothing to ask", len(got))
	}
}

// A delegate's words are its report to the agent that asked it, never a
// message in the conversation: a delegate parks and stays parked for its
// parent, and only the top of the tree asks.
func TestADelegateThatParksAsksNothing(t *testing.T) {
	a := waitingIn(t, "member", false)
	child := uuid.New()
	if _, err := a.raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, parent_run_id, parent_tool_call, depth, agent_name, input,
		                  user_id, status, worker, heartbeat_at, started_at)
		VALUES ($1, $2, $3, $4, $4, 'call_1#a', 1, 'billing', 'refund', $5, 'running', $6, now(), now())`,
		child, a.run.TeamID, a.run.SessionID, a.run.ID, a.user, worker); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if _, err := a.store.InsertApproval(t.Context(), db.InsertApprovalParams{
		TeamID: a.run.TeamID, RunID: child, RootRunID: a.run.ID, CallID: "c2", Tool: "mail__send", Class: "write",
		Arguments: []byte(`{}`), Code: "9ABC", RequestedFor: &a.user, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertApproval: %v", err)
	}

	said := "the refund needs an OK"
	parked, again, asked, err := parkAndAsk(t.Context(), a.store, child, worker, true, &said, []byte(`{}`))
	if err != nil || !parked || again != nil || asked != 0 {
		t.Fatalf("a delegate's park = parked %v, queued %v, asked %d, %v; want parked, waiting, asking nothing",
			parked, again, asked, err)
	}
	var status string
	if err := a.raw.QueryRow(t.Context(), `SELECT status FROM runs WHERE id = $1`, child).Scan(&status); err != nil || status != "waiting" {
		t.Errorf("the delegate is %s, %v; want waiting for its parent", status, err)
	}
	if got := a.prompts(t); len(got) != 0 {
		t.Errorf("%d prompts from a delegate", len(got))
	}
}

// A run that answers no channel (the dashboard's, the API's) parks as before
// and writes no prompt.
func TestARunThatAnswersNoChannelAsksNothingInOne(t *testing.T) {
	a := waitingIn(t, "member", false)
	if _, err := a.raw.Exec(t.Context(), `UPDATE runs SET reply_to_channel = false WHERE id = $1`, a.run.ID); err != nil {
		t.Fatalf("update: %v", err)
	}
	if seq := a.park(t, "OK to open it?"); seq != 0 {
		t.Errorf("a run that answers no channel queued prompt %d", seq)
	}
	if got := a.prompts(t); len(got) != 0 {
		t.Errorf("%d prompts", len(got))
	}
}

// A custom channel with no reply URL has nowhere to post a prompt: the run
// parks and nothing is queued to be given up.
func TestARunOnAChannelWithNowhereToReplyAsksNothingInIt(t *testing.T) {
	a := waitingIn(t, "member", false)
	if _, err := a.raw.Exec(t.Context(), `
		UPDATE channels SET provider = 'custom', reply_url = NULL
		WHERE id = (SELECT channel_id FROM sessions WHERE id = $1)`, a.run.SessionID); err != nil {
		t.Fatalf("make the channel custom with no reply URL: %v", err)
	}
	if seq := a.park(t, "OK to open it?"); seq != 0 {
		t.Errorf("a channel with nowhere to post queued prompt %d", seq)
	}
	if got := a.prompts(t); len(got) != 0 {
		t.Errorf("%d prompts", len(got))
	}
}

// The final answer is still the run's one answer: a prompt does not take its
// place, and a run that asks twice asks twice.
func TestAPromptIsNotTheRunsAnswer(t *testing.T) {
	a := waitingIn(t, "member", false)
	first := a.park(t, "OK to open it?")
	if _, err := a.raw.Exec(t.Context(), `
		UPDATE runs SET status = 'running', worker = $2, heartbeat_at = now(), waiting_since = NULL WHERE id = $1`,
		a.run.ID, worker); err != nil {
		t.Fatalf("resume: %v", err)
	}
	second := a.park(t, "Still waiting on that OK.")
	if second == 0 || second == first {
		t.Fatalf("the second park asked %d after %d; want a prompt of its own", second, first)
	}
	if got := a.prompts(t); len(got) != 2 {
		t.Fatalf("%d prompts, want 2", len(got))
	}
	if _, err := a.raw.Exec(t.Context(), `
		INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text)
		SELECT r.team_id, r.id, r.session_id, s.channel_id, 'answer', 'Done.'
		FROM runs r JOIN sessions s ON s.id = r.session_id WHERE r.id = $1`, a.run.ID); err != nil {
		t.Errorf("the run's answer beside its prompts: %v", err)
	}
}

// A prompt is sent as soon as the park commits, kicked by its waiting event
// as a step is: kicked by the run, it would take the run's own kick, and the
// answer that follows would wait for the sweep.
func TestAParkKicksItsPromptByItsWaitingEvent(t *testing.T) {
	a := waitingIn(t, "member", false)
	k := &stepKicks{}
	e := &Executor{store: a.store, logger: quietLogger(), kickEvent: k.kick}

	if err := e.park(t.Context(), a.run, Lineage{Worker: worker}, "OK to open the issue?"); err != nil {
		t.Fatalf("park: %v", err)
	}

	p := a.prompts(t)
	if len(p) != 1 || p[0].seq == nil {
		t.Fatalf("prompts %+v", p)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.seqs) != 1 || k.seqs[0] != *p[0].seq {
		t.Errorf("kicked %v, want the waiting event %d", k.seqs, *p[0].seq)
	}
}
