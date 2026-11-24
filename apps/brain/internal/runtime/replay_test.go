package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// replayFixture is an agent holding a trusted server's read-only lookup and
// an untrusted server's send, both real MCP servers that count their calls,
// and a Replayer on a scripted model.
type replayFixture struct {
	*fixture
	crm, mail *mcpServer
	model     *scripted
	replayer  *Replayer
}

func newReplayFixture(t *testing.T, turns ...turn) *replayFixture {
	t.Helper()
	f := &replayFixture{fixture: newFixture(), crm: serve(t, "lookup"), mail: serve(t, "send")}
	crm, mail := urlServer("crm", f.crm.url, false), urlServer("mail", f.mail.url, false)
	crm.McpServer.TrustHints = true
	f.store.grants = []db.ListGrantedMCPServersRow{crm, mail}
	f.store.catalogue = []db.ListGrantedMCPToolsRow{
		{McpServerID: crm.McpServer.ID, Name: "lookup", Description: "Look a customer up", InputSchema: []byte(`{"type":"object"}`), Annotations: []byte(`{"readOnlyHint":true}`)},
		{McpServerID: mail.McpServer.ID, Name: "send", Description: "Send an email", InputSchema: []byte(`{"type":"object"}`), Annotations: []byte(`{"readOnlyHint":true}`)},
	}
	f.model = newScripted(turns...)
	runner, err := agent.New(func(agent.Endpoint) (agent.ModelClient, error) { return f.model, nil }, Patterns()...)
	if err != nil {
		t.Fatal(err)
	}
	// No graph: the tools are sent, and a test sees each by its name.
	f.replayer = NewReplayer(f.clients, roles{}, nil, nil, nil, runner, agent.Endpoint{})
	return f
}

func (f *replayFixture) replay(t *testing.T, recorded ...Recorded) Replayed {
	t.Helper()
	out, err := f.replayer.Replay(t.Context(), ReplayInput{
		Store: f.store, Objects: f.objects, Run: f.run, Recorded: recorded,
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "find the customer"}}}},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return out
}

func hows(calls []Served) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Name + ":" + c.How
	}
	return out
}

// A call the past run made gets what it got then, with no call reaching the
// server; a new read runs; a new call that is not a read is not made, and
// the model is told so.
func TestAReplayServesTheRecordAndMakesOnlyReads(t *testing.T) {
	f := newReplayFixture(t,
		callTool("1", "crm__lookup", `{"id": 1, "name": "Ada"}`),
		callTool("2", "crm__lookup", `{"id":2}`),
		callTool("3", "mail__send", `{"to":"ada@example.com"}`),
		answer("Found Ada.", 10, 5),
	)
	out := f.replay(t, Recorded{Name: "crm__lookup", Arguments: `{"name":"Ada","id":1}`, Output: "Ada, customer since 2021"})

	if want := []string{"crm__lookup:recorded", "crm__lookup:live", "mail__send:not_made"}; !slices.Equal(hows(out.Calls), want) {
		t.Errorf("served %v, want %v", hows(out.Calls), want)
	}
	if n := f.crm.calls.Load(); n != 1 {
		t.Errorf("the read server answered %d calls, want only the new read", n)
	}
	// The send's server is untrusted: its hint is not believed, so the send
	// is not a read, and no call of it reaches the server.
	if n := f.mail.calls.Load(); n != 0 {
		t.Errorf("the send's server answered %d calls, want none", n)
	}
	if out.Output != "Found Ada." || out.Steps == 0 {
		t.Errorf("replayed %+v", out)
	}
	reqs := f.model.requests()
	last := reqs[len(reqs)-1].Messages
	var said []string
	for _, m := range last {
		if m.Role == agent.RoleTool {
			for _, c := range m.Content {
				said = append(said, c.Text)
			}
		}
	}
	if !slices.Contains(said, "Ada, customer since 2021") || !slices.ContainsFunc(said, func(s string) bool { return strings.HasPrefix(s, "Not made: this is a replay") }) {
		t.Errorf("the model read %q, want the record and the not-made notice", said)
	}
}

// A call the past run made twice is served twice; a third is a new call. A
// recorded failure is served as one.
func TestARecordIsServedAsOftenAsItWasMade(t *testing.T) {
	f := newReplayFixture(t,
		callTool("1", "mail__send", `{"to":"a"}`),
		callTool("2", "mail__send", `{"to":"a"}`),
		callTool("3", "mail__send", `{"to":"a"}`),
		callTool("4", "crm__lookup", `{"id":9}`),
		answer("done", 1, 1),
	)
	out := f.replay(t,
		Recorded{Name: "mail__send", Arguments: `{"to":"a"}`, Output: "sent"},
		Recorded{Name: "mail__send", Arguments: `{"to":"a"}`, Output: "sent again"},
		Recorded{Name: "crm__lookup", Arguments: `{"id":9}`, Error: "no such customer"},
	)
	want := []string{"mail__send:recorded", "mail__send:recorded", "mail__send:not_made", "crm__lookup:recorded"}
	if !slices.Equal(hows(out.Calls), want) {
		t.Errorf("served %v, want %v", hows(out.Calls), want)
	}
	if out.Calls[3].Error != "no such customer" || f.crm.calls.Load() != 0 || f.mail.calls.Load() != 0 {
		t.Errorf("a recorded failure was %+v, with %d and %d calls made", out.Calls[3], f.crm.calls.Load(), f.mail.calls.Load())
	}
}

// A workspace and a delegation are held as the agent would hold them, and
// nothing of either is made: there is no room or delegate to reach.
func TestAReplayHoldsTheWorkspaceAndDelegationAsStandIns(t *testing.T) {
	f := newReplayFixture(t,
		callTool("1", BashToolName, `{"command":"rm -rf /workspace"}`),
		callTool("2", DelegateToolName, `{"agent":"writer","brief":"write it"}`),
		answer("done", 1, 1),
	)
	a := f.agent
	a.Workspace, a.Delegation = true, "team"
	f.store.agents[a.ID] = a
	f.store.settings = &db.TeamSetting{SandboxProvider: "e2b", BrowserProvider: "none"}
	out := f.replay(t)
	if !slices.Contains(out.Tools, BashToolName) || !slices.Contains(out.Tools, DelegateToolName) {
		t.Errorf("held %v, want the workspace and delegate as the agent holds them", out.Tools)
	}
	if want := []string{BashToolName + ":not_made", DelegateToolName + ":not_made"}; !slices.Equal(hows(out.Calls), want) {
		t.Errorf("served %v, want %v", hows(out.Calls), want)
	}
}

// Every tool a replay holds that is not a read cannot be made, whatever the
// matching does: its own call is gone before the record is consulted.
func TestATriedToolThatIsNotAReadCannotBeMade(t *testing.T) {
	called := false
	r := newReplaying(nil, map[string]bool{})
	tool := r.wrap(agent.Tool{Name: "send", Invoke: func(context.Context, json.RawMessage) (string, error) {
		called = true
		return "sent", nil
	}})
	_, err := tool.Result(t.Context(), json.RawMessage(`{}`))
	if called || err == nil || !strings.HasPrefix(err.Error(), "Not made") {
		t.Errorf("called %v, err %v; want not made", called, err)
	}
}

// A try that spends its tokens is stopped, and says why, not failed.
func TestAReplayIsStoppedAtItsTokens(t *testing.T) {
	f := newReplayFixture(t, callTool("1", "crm__lookup", `{"id":1}`), answer("done", 10, 5))
	out, err := f.replayer.Replay(t.Context(), ReplayInput{
		Store: f.store, Objects: f.objects, Run: f.run, Limits: ReplayLimits{Tokens: 10},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "go"}}}},
	})
	if err != nil || !errors.Is(out.Over, ErrReplayTokens) {
		t.Errorf("replay = %+v, %v; want stopped at its tokens", out.Over, err)
	}
	// What it spent is kept, and the call the crossing reply asked for is
	// not made.
	if out.Usage.InputTokens+out.Usage.OutputTokens < 10 {
		t.Errorf("a stopped try spent %+v, want what its reply used", out.Usage)
	}
	if n := f.crm.calls.Load(); n != 0 {
		t.Errorf("the lookup reached its server %d times past the cap", n)
	}
}

// A try past its minutes is stopped the same way.
func TestAReplayIsStoppedAtItsTime(t *testing.T) {
	f := newReplayFixture(t, blocked())
	out, err := f.replayer.Replay(t.Context(), ReplayInput{
		Store: f.store, Objects: f.objects, Run: f.run, Limits: ReplayLimits{Time: 50 * time.Millisecond},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "go"}}}},
	})
	if err != nil || !errors.Is(out.Over, ErrReplayTime) {
		t.Errorf("replay = %+v, %v; want stopped at its time", out.Over, err)
	}
}

func TestCanonicalArgumentsAreOneText(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`},
		{`{"n":12345678901234567890}`, `{"n": 12345678901234567890}`},
		{`not json`, `  not json `},
	} {
		if canonical(tc.a) != canonical(tc.b) {
			t.Errorf("%q and %q differ: %q, %q", tc.a, tc.b, canonical(tc.a), canonical(tc.b))
		}
	}
	if canonical(`{"a":1}`) == canonical(`{"a":2}`) {
		t.Error("different arguments read alike")
	}
}

// roles is a team in which the run's person holds what perms says.
type roles struct {
	perms []string
	rules []db.ApprovalRule
}

func (roles) FindTeamMember(context.Context, db.FindTeamMemberParams) (string, error) {
	return "member", nil
}

func (r roles) ListRolePermissions(context.Context, string) ([]string, error) { return r.perms, nil }

func (r roles) ListApprovalRules(context.Context, uuid.UUID) ([]db.ApprovalRule, error) {
	return r.rules, nil
}

// A read the team's rules deny, or ask about, is not made live in a replay:
// there is no one to ask, and a deny holds whoever runs it.
func TestAReplayKeepsTheTeamsRulesOnReads(t *testing.T) {
	member := uuid.New()
	for _, tc := range []struct {
		name  string
		mode  string
		rules []db.ApprovalRule
		read  bool
	}{
		{"no rule", "writes", nil, true},
		{"approvals off", "off", []db.ApprovalRule{{TargetKind: "tool", Target: WebFetchToolName, Mode: "deny"}}, true},
		{"denied", "writes", []db.ApprovalRule{{TargetKind: "tool", Target: WebFetchToolName, Mode: "deny"}}, false},
		{"asked about", "writes", []db.ApprovalRule{{TargetKind: "tool", Target: WebFetchToolName, Mode: "ask"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWebFixture("none", "")
			w.asAgent(true)
			w.builder.replay, w.builder.roles = true, roles{perms: []string{"approval:self"}, rules: tc.rules}
			w.run.UserID = &member
			if w.store.settings == nil {
				w.store.settings = &db.TeamSetting{TeamID: w.run.TeamID}
			}
			w.store.settings.ApprovalMode = tc.mode

			got := w.build(t)

			if got.reads[WebFetchToolName] != tc.read {
				t.Errorf("web_fetch read = %v, want %v", got.reads[WebFetchToolName], tc.read)
			}
		})
	}
}

// The web is a read in a replay only for a past run a person who may
// approve their own calls was there for: for a guest's, or a run nobody was
// there for, a fetch carries data out unseen, so it is not made.
func TestAReplayReachesTheWebOnlyAsThePastRunsPersonWould(t *testing.T) {
	member := uuid.New()
	for _, tc := range []struct {
		name       string
		user       *uuid.UUID
		perms      []string
		unattended bool
		read       bool
	}{
		{"a member who was there", &member, []string{"approval:self"}, false, true},
		{"a member's unattended run", &member, []string{"approval:self"}, true, false},
		{"a guest", &member, nil, false, false},
		{"nobody", nil, []string{"approval:self"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWebFixture("none", "")
			w.asAgent(true)
			w.builder.replay, w.builder.roles = true, roles{perms: tc.perms}
			w.run.UserID, w.run.Unattended = tc.user, tc.unattended

			got := w.build(t)

			if len(toolsNamed(got, WebFetchToolName)) != 1 {
				t.Fatal("web_fetch is not held as the agent holds it")
			}
			if got.reads[WebFetchToolName] != tc.read {
				t.Errorf("web_fetch read = %v, want %v", got.reads[WebFetchToolName], tc.read)
			}
		})
	}
}

// Two tools of one name could make a write read as a read: the replay is
// refused rather than guessed at.
func TestAReplayRefusesTwoToolsOfOneName(t *testing.T) {
	if d := duplicate([]string{"a", "b", "a"}); d != "a" {
		t.Errorf("duplicate = %q, want a", d)
	}
	if d := duplicate([]string{"a", "b"}); d != "" {
		t.Errorf("duplicate = %q, want none", d)
	}
}

// A replayed hub holds its delegate tool as a stand-in, as any agent does.
func TestAReplayedHubHoldsItsDelegateAsAStandIn(t *testing.T) {
	f := newFixture()
	f.builder.replay = true
	f.store.others = 1
	f.run.Hub = true

	tool, ok, err := f.builder.delegateFor(t.Context(), f.agent, f.run, Lineage{})
	if err != nil || !ok || tool.Name != DelegateToolName {
		t.Fatalf("delegate = %q, %v, %v; want the stand-in", tool.Name, ok, err)
	}
}

// A call is read with the result its id names; one with no result is left
// out, and an event that cannot be read fails the reading.
func TestARunsCallsAreReadFromItsEvents(t *testing.T) {
	got, err := RecordedOf([]db.RunEvent{
		{Seq: 1, Kind: "tool_call", Payload: []byte(`{"id":"a","name":"crm__lookup","arguments":"{}"}`)},
		{Seq: 2, Kind: "tool_call", Payload: []byte(`{"id":"b","name":"mail__send","arguments":"{}"}`)},
		{Seq: 3, Kind: "tool_result", Payload: []byte(`{"id":"a","name":"crm__lookup","output":"Ada"}`)},
	})
	if err != nil || len(got) != 1 || got[0] != (Recorded{Name: "crm__lookup", Arguments: "{}", Output: "Ada"}) {
		t.Errorf("recorded %+v, %v", got, err)
	}
	if _, err := RecordedOf([]db.RunEvent{{Seq: 1, Kind: "tool_call", Payload: []byte(`{`)}}); err == nil {
		t.Error("an unreadable event was passed over")
	}
}

// A trusted read-only tool a team rule denies is not made live in a
// replay: the server sees no call.
func TestAReplayKeepsADenyOnAServersRead(t *testing.T) {
	f := newReplayFixture(t, callTool("1", "crm__lookup", `{"id":2}`), answer("done", 10, 5))
	f.store.settings = &db.TeamSetting{
		TeamID: f.run.TeamID, ApprovalMode: "writes", SearchProvider: "none",
		CompactionMessages: 10, CompactionTokensMin: 50_000, CompactionTokensMax: 60_000,
	}
	f.replayer.roles = roles{rules: []db.ApprovalRule{{TargetKind: "tool", Target: "crm__lookup", Mode: "deny"}}}

	out := f.replay(t)

	if want := []string{"crm__lookup:not_made"}; !slices.Equal(hows(out.Calls), want) {
		t.Errorf("calls %v, want %v", hows(out.Calls), want)
	}
	if n := f.crm.calls.Load(); n != 0 {
		t.Errorf("a denied read reached its server %d times", n)
	}
}
