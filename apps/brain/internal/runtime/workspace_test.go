package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/workroom"
)

// fakeRoom is a workspace that answers each route with what replies says,
// and counts what it was sent.
type fakeRoom struct {
	mu      sync.Mutex
	srv     *httptest.Server
	replies map[string]reply
	got     map[string][]map[string]any
}

type reply struct {
	status int
	body   string
}

func newFakeRoom(t *testing.T) *fakeRoom {
	t.Helper()
	r := &fakeRoom{replies: map[string]reply{}, got: map[string][]map[string]any{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		r.mu.Lock()
		r.got[req.URL.Path] = append(r.got[req.URL.Path], body)
		rp, ok := r.replies[req.URL.Path]
		r.mu.Unlock()
		if !ok {
			rp = reply{200, `{}`}
		}
		w.WriteHeader(rp.status)
		_, _ = w.Write([]byte(rp.body))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRoom) answer(route string, status int, body string) {
	r.mu.Lock()
	r.replies[route] = reply{status, body}
	r.mu.Unlock()
}

func (r *fakeRoom) calls(route string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got[route])
}

// fakeWorkspaces is the session's rooms: every Ensure is a new room at the
// fake, counted, and every Touch is counted.
type fakeWorkspaces struct {
	room           *fakeRoom
	mu             sync.Mutex
	ensures, touch int
	team, session  uuid.UUID
	ensureErr      error
	// stops are the sessions stopped, each with what idle said under the lock.
	stops []stoppedRoom
}

type stoppedRoom struct {
	team, session uuid.UUID
	idle          bool
}

func (f *fakeWorkspaces) Ensure(_ context.Context, team, session uuid.UUID) (sandbox.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	f.team, f.session = team, session
	if f.ensureErr != nil {
		return sandbox.Endpoint{}, f.ensureErr
	}
	return sandbox.Endpoint{URL: f.room.srv.URL, Header: http.Header{"Authorization": {"Bearer room"}}}, nil
}

func (f *fakeWorkspaces) Stop(ctx context.Context, team, session uuid.UUID, idle func(context.Context) (bool, error)) error {
	ok, err := idle(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, stoppedRoom{team, session, ok})
	return nil
}

func (f *fakeWorkspaces) stopped() []stoppedRoom {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stoppedRoom(nil), f.stops...)
}

func (f *fakeWorkspaces) Touch(_ context.Context, session uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touch++
	return nil
}

func (f *fakeWorkspaces) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensures, f.touch
}

// newWorkspaceFixture is an agent of a team with a sandbox, granted the
// workspace when grant is set.
func newWorkspaceFixture(t *testing.T, grant bool) (*fixture, *fakeWorkspaces) {
	t.Helper()
	f := newFixture()
	f.teamModel()
	f.store.settings.SandboxProvider = string(sandbox.Sandboxd)
	f.agent.Workspace = grant
	f.store.agents[f.agent.ID] = f.agent
	f.run.Hub, f.run.AgentID, f.run.AgentName = false, &f.agent.ID, f.agent.Name
	ws := &fakeWorkspaces{room: newFakeRoom(t)}
	f.builder.WithWorkspaces(&WorkspaceDeps{Rooms: ws, Client: http.DefaultClient})
	return f, ws
}

func workspaceToolsIn(built Built) []string {
	var names []string
	for _, t := range built.Spec.Tools {
		if slices.Contains(WorkspaceToolNames, t.Name) {
			names = append(names, t.Name)
		}
	}
	slices.Sort(names)
	return names
}

func allWorkspaceTools() []string {
	names := slices.Clone(WorkspaceToolNames)
	slices.Sort(names)
	return names
}

func invoke(t *testing.T, built Built, name, args string) (string, error) {
	t.Helper()
	tools := toolsNamed(built, name)
	if len(tools) != 1 {
		t.Fatalf("%s tools = %d", name, len(tools))
	}
	return tools[0].Invoke(t.Context(), json.RawMessage(args))
}

// --- who has it ---

func TestAnAgentHasTheWorkspaceOnlyByItsGrant(t *testing.T) {
	for _, grant := range []bool{false, true} {
		f, ws := newWorkspaceFixture(t, grant)
		got := f.build(t)
		want := []string(nil)
		if grant {
			want = allWorkspaceTools()
		}
		if names := workspaceToolsIn(got); !slices.Equal(names, want) {
			t.Errorf("granted %v: tools %v, want %v", grant, names, want)
		}
		// Nothing is started until a call needs the room.
		if e, _ := ws.counts(); e != 0 {
			t.Errorf("granted %v: %d rooms ensured at build, want none", grant, e)
		}
	}
}

func TestATeamWithNoSandboxSaysWhyItsAgentHasNoWorkspace(t *testing.T) {
	f, _ := newWorkspaceFixture(t, true)
	f.store.settings.SandboxProvider = string(sandbox.None)
	got := f.build(t)
	if names := workspaceToolsIn(got); len(names) != 0 {
		t.Fatalf("tools %v with no sandbox", names)
	}
	for _, n := range WorkspaceToolNames {
		if !slices.ContainsFunc(got.Unavailable, func(u Unavailable) bool {
			return u.Name == n && strings.Contains(u.Reason, "choose one in Settings")
		}) {
			t.Errorf("%s is not said to be unavailable: %+v", n, got.Unavailable)
		}
	}
}

// A hub the team chose from its agents may act on its own, so granted the
// workspace it has the six tools, as any agent does.
func TestAChosenHubGrantedTheWorkspaceHasIt(t *testing.T) {
	f, _ := newWorkspaceFixture(t, true)
	f.asChosenHub()
	f.store.settings.SandboxProvider = string(sandbox.Sandboxd)
	got := f.build(t)
	if names := workspaceToolsIn(got); !slices.Equal(names, allWorkspaceTools()) {
		t.Errorf("the chosen hub has %v, want all six", names)
	}
}

// A chosen hub not granted it has none, and is not told it lacks one.
func TestAChosenHubNotGrantedTheWorkspaceHasNone(t *testing.T) {
	f, _ := newWorkspaceFixture(t, false)
	f.asChosenHub()
	got := f.build(t)
	if names := workspaceToolsIn(got); len(names) != 0 {
		t.Errorf("the hub has %v", names)
	}
	if slices.ContainsFunc(got.Unavailable, func(u Unavailable) bool { return slices.Contains(WorkspaceToolNames, u.Name) }) {
		t.Errorf("a hub without the grant is told it lacks a workspace: %+v", got.Unavailable)
	}
}

// A brain whose secret store cannot write keeps no room's token, so it makes
// no rooms: a granted agent has no workspace, and is told why.
func TestNoWorkspacesOnDeployNoWorkspaceAndSayWhy(t *testing.T) {
	f, _ := newWorkspaceFixture(t, true)
	f.builder.workspaces = nil
	got := f.build(t)
	if names := workspaceToolsIn(got); len(names) != 0 {
		t.Errorf("tools %v on a brain with no sandbox wiring", names)
	}
	for _, n := range WorkspaceToolNames {
		if !slices.ContainsFunc(got.Unavailable, func(u Unavailable) bool {
			return u.Name == n && strings.Contains(u.Reason, "secret store is read-only")
		}) {
			t.Errorf("%s is not said to be unavailable: %+v", n, got.Unavailable)
		}
	}
}

func TestTheWorkspaceNamesAreReservedOnlyOnAWorkspaceRun(t *testing.T) {
	for _, name := range WorkspaceToolNames {
		if reserved(name, reservedFor{hub: true, web: true, browse: true}) {
			t.Errorf("%s is reserved on a run with no workspace", name)
		}
		if !reserved(name, reservedFor{work: true}) {
			t.Errorf("%s is not reserved on a workspace run", name)
		}
	}
}

// --- what a call does ---

func TestTheFirstCallStartsTheSessionsRoomAndTheRestOnlyTouchIt(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	ws.room.answer("/exec", 200, `{"stdout":"hi\n"}`)
	ws.room.answer("/read", 200, `{"content":"     1\tx\n","lines":1}`)
	got := f.build(t)

	for range 2 {
		if out, err := invoke(t, got, BashToolName, `{"command":"echo hi"}`); err != nil || out != "hi\n" {
			t.Fatalf("Bash = %q, %v", out, err)
		}
	}
	if _, err := invoke(t, got, ReadToolName, `{"path":"a"}`); err != nil {
		t.Fatal(err)
	}
	if e, touch := ws.counts(); e != 1 || touch != 2 {
		t.Errorf("ensured %d, touched %d; want one start and a touch for each call after it", e, touch)
	}
	if ws.session != f.run.SessionID || ws.team != f.run.TeamID {
		t.Errorf("room for team %v session %v, want the run's", ws.team, ws.session)
	}
}

// A room found gone is not called again for the same call: an Edit sent
// twice could edit twice. The next call starts a fresh room.
func TestARoomFoundGoneIsNotRetriedAndTheNextCallStartsAnother(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	got := f.build(t)
	ws.room.answer("/edit", 404, `no such room`)

	_, err := invoke(t, got, EditToolName, `{"path":"f","old_string":"a","new_string":"b"}`)
	if err == nil || !strings.Contains(err.Error(), "did not reach it") {
		t.Fatalf("Edit = %v, want the model told the room was gone", err)
	}
	if n := ws.room.calls("/edit"); n != 1 {
		t.Errorf("the edit was sent %d times, want once", n)
	}

	ws.room.answer("/edit", 200, `{"path":"/workspace/f","replaced":1}`)
	if out, err := invoke(t, got, EditToolName, `{"path":"f","old_string":"a","new_string":"b"}`); err != nil || out != "Replaced 1 in /workspace/f." {
		t.Fatalf("Edit after = %q, %v", out, err)
	}
	if e, _ := ws.counts(); e != 2 {
		t.Errorf("ensured %d rooms, want a second after the first was gone", e)
	}
}

// A call sent and never answered is not said not to have run, and is not
// sent again: the model is told to look first.
func TestACallWithNoAnswerIsNotRetriedAndMayHaveRun(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	got := f.build(t)
	ws.room.answer("/exec", 502, `upstream`)

	_, err := invoke(t, got, BashToolName, `{"command":"git push"}`)
	if err == nil || !strings.Contains(err.Error(), "may or may not have run") {
		t.Fatalf("Bash = %v, want the model told the outcome is unknown", err)
	}
	if n := ws.room.calls("/exec"); n != 1 {
		t.Errorf("the command was sent %d times, want once", n)
	}
	ws.room.answer("/exec", 200, `{"stdout":"ok\n","stderr":"","exit_code":0}`)
	if _, err := invoke(t, got, BashToolName, `{"command":"git status"}`); err != nil {
		t.Fatalf("Bash after = %v", err)
	}
	if e, _ := ws.counts(); e != 2 {
		t.Errorf("ensured %d times, want the room found again after no answer", e)
	}
}

func TestAProblemIsSaidToTheModelInTheWorkspacesWords(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	ws.room.answer("/edit", 422, `{"error":"old_string is in f 2 times; give more of the text around it, or set replace_all"}`)
	_, err := invoke(t, f.build(t), EditToolName, `{"path":"f","old_string":"a","new_string":"b"}`)
	if err == nil || err.Error() != "old_string is in f 2 times; give more of the text around it, or set replace_all" {
		t.Fatalf("Edit = %v", err)
	}
	if e, _ := ws.counts(); e != 1 {
		t.Errorf("a problem replaced the room: ensured %d", e)
	}
}

// A workspace the keeper had to replace is said as it is: the call did not
// run, the files are gone, and the next call uses the fresh room.
func TestAReplacedWorkspaceIsSaidAndTheNextCallUsesIt(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	got := f.build(t)
	ws.ensureErr = sandbox.ErrReplaced
	_, err := invoke(t, got, BashToolName, `{"command":"ls"}`)
	if err == nil || err.Error() != sandbox.ErrReplaced.Error() {
		t.Fatalf("Bash = %v, want the model told its files are gone, in those words", err)
	}
	if n := ws.room.calls("/exec"); n != 0 {
		t.Fatalf("the command was sent %d times after the room was replaced", n)
	}
	ws.ensureErr = nil
	ws.room.answer("/exec", 200, `{"stdout":"","stderr":"","exit_code":0}`)
	if _, err := invoke(t, got, BashToolName, `{"command":"ls"}`); err != nil {
		t.Fatalf("Bash after = %v", err)
	}
}

func TestARoomThatCannotStartSaysWhy(t *testing.T) {
	f, ws := newWorkspaceFixture(t, true)
	ws.ensureErr = sandbox.ErrRefused
	_, err := invoke(t, f.build(t), GlobToolName, `{"pattern":"**"}`)
	if err == nil || !strings.Contains(err.Error(), "could not be started") || !strings.Contains(err.Error(), sandbox.ErrRefused.Error()) {
		t.Fatalf("Glob = %v", err)
	}
}

func TestWhatACallSaysBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, got, want string
	}{
		{"output and a failure", execText(execOf("out\n", "boom\n", 2, false)), "out\nstderr:\nboom\nexit code 2"},
		{"nothing", execText(execOf("", "", 0, false)), "(no output)"},
		{"out of time", execText(execOf("part", "", -1, true)), "The command ran out of time and was stopped, with everything it started.\npart\nexit code -1"},
		{"more to read", readText(readOf("     1\ta\n", 3), 1), "     1\ta\n(lines 1 to 1 of 3; read on from offset 2)"},
		{"fewer than asked", readText(readOf("     2\tb\n     3\tc\n", 5000), 2), "     2\tb\n     3\tc\n(lines 2 to 3 of 5000; read on from offset 4)"},
		{"read to the end", readText(readOf("     3\tc\n", 3), 3), "     3\tc\n"},
		{"empty file", readText(readOf("", 0), 0), "(the file is empty)"},
		{"past the end", readText(readOf("", 3), 9), "(the file has 3 lines, so nothing is at line 9)"},
		{"no matches", grepText(workroom.Grep{}), "No matches."},
		{"cut before a file was counted", grepText(workroom.Grep{Output: "a.txt:1:hit\n", Truncated: true}), "a.txt:1:hit\n(more match than are shown: narrow the pattern, the path or the glob)"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// --- the gate ---

// Bash can send anything to any public host; the rest touch only the room.
func TestBashAsksWhereAWriteWouldAndTheRestDoNot(t *testing.T) {
	t.Parallel()
	p := gatedBy(&desk{}, approval.Writes, approval.Actor{CanSelf: true})
	for _, n := range WorkspaceToolNames {
		want := approval.Allow
		if n == BashToolName {
			want = approval.Ask
		}
		if got := p.decide(target{name: n, class: workspaceClass(n)}); got.Verdict != want {
			t.Errorf("%s under writes: %v, want %v", n, got.Verdict, want)
		}
	}
	nobody := gatedBy(&desk{}, approval.Writes, approval.Actor{CanSelf: true, Unattended: true})
	if got := nobody.decide(target{name: BashToolName, class: workspaceClass(BashToolName)}); got.Verdict != approval.Deny {
		t.Errorf("Bash with nobody present under writes: %v, want deny", got.Verdict)
	}
}

func execOf(stdout, stderr string, code int, timedOut bool) workroom.Exec {
	return workroom.Exec{Stdout: stdout, Stderr: stderr, ExitCode: code, TimedOut: timedOut}
}

func readOf(content string, lines int) workroom.Read {
	return workroom.Read{Content: content, Lines: lines}
}

// A team that trusts its agents' commands says so with one rule.
func TestARuleAllowingBashLetsItRunUnasked(t *testing.T) {
	t.Parallel()
	p := gatedBy(&desk{}, approval.Writes, approval.Actor{CanSelf: true})
	p.rules = []approval.Rule{{Kind: "tool", Target: BashToolName, Verdict: approval.Allow}}
	if got := p.decide(target{name: BashToolName, class: workspaceClass(BashToolName)}); got.Verdict != approval.Allow {
		t.Errorf("Bash with a rule allowing it: %v, want allow", got.Verdict)
	}
}

// The model is told how a pipe hides a failure, so it keeps the status when
// it filters a command's output.
func TestBashSaysAPipeHidesAFailure(t *testing.T) {
	t.Parallel()

	var desc string
	for _, tl := range (&runWorkspace{}).tools() {
		if tl.Name == BashToolName {
			desc = tl.Description
		}
	}
	if !strings.Contains(desc, "set -o pipefail") || !strings.Contains(desc, "PIPESTATUS") {
		t.Errorf("Bash's description does not say how a pipe hides a failure: %q", desc)
	}
}

// What execText writes is what workroom reads back, so the improver tells a
// command that did not end well from the same text the model reads.
func TestACommandsEndReadsBackFromItsText(t *testing.T) {
	t.Parallel()

	for _, code := range []int{0, 1, 2, 127, -1} {
		for _, timedOut := range []bool{false, true} {
			for _, out := range [][2]string{{"", ""}, {"out\n", ""}, {"out", "boom\n"}, {"", "boom"}} {
				text := execText(execOf(out[0], out[1], code, timedOut))
				got, ok := workroom.ExitOf(text)
				if ok != (code != 0) || got != code && code != 0 {
					t.Errorf("exit %d, timed out %v, %q: read back %d, %v", code, timedOut, text, got, ok)
				}
				if workroom.TimedOut(text) != timedOut {
					t.Errorf("exit %d, timed out %v, %q: read back as timed out %v", code, timedOut, text, !timedOut)
				}
			}
		}
	}
}
