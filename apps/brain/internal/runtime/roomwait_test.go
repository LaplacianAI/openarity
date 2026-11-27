package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const waitsForTheSandbox = "Waiting for the sandbox."

// roomFixture is the fixture's run with the workspace, at a sandbox that has
// no room, and a sweep beside it whose resumes are recorded.
func roomFixture(t *testing.T, model *scripted) (*execFixture, *fakeWorkspaces, *Job, *wakes) {
	t.Helper()
	f := newExecFixture(t, model)
	f.mustExec(t, `UPDATE agents SET workspace = true WHERE id = $1`, f.agent)
	f.mustExec(t, `UPDATE team_settings SET sandbox_provider = 'sandboxd', sandbox_secret_id = gen_random_uuid(), sandbox_url = 'http://sandboxd.example:9090' WHERE team_id = $1`, f.team)
	ws := &fakeWorkspaces{room: newFakeRoom(t), ensureErr: fmt.Errorf("making the room: %w: memory", sandbox.ErrFull)}
	f.exec.builder.WithWorkspaces(&WorkspaceDeps{Rooms: ws, Client: f.roomClient(), Waits: f.store, Logger: quietLogger()})
	j := RunJob(f.exec, f.store, quietLogger())
	r := &wakes{}
	j.resume = r.add
	return f, ws, j, r
}

// roomClient reaches the fake room, which is on loopback.
func (*execFixture) roomClient() *http.Client { return http.DefaultClient }

// wakes are the runs the sweep queued again.
type wakes struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (r *wakes) add(id uuid.UUID, _ int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func (r *wakes) got() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.ids...)
}

func (f *execFixture) roomWaits(t *testing.T) []db.RoomWait {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT * FROM room_waits WHERE root_run_id = $1 ORDER BY since, id`, f.run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.RoomWait
	for rows.Next() {
		var w db.RoomWait
		if err := rows.Scan(&w.ID, &w.TeamID, &w.RunID, &w.RootRunID, &w.SessionID, &w.Reason,
			&w.Since, &w.RetryAt, &w.Ended, &w.EndedAt, &w.ToldAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, w)
	}
	return out
}

// due makes every open wait of the tree due a try, as if it began ago.
func (f *execFixture) due(t *testing.T, ago time.Duration) {
	t.Helper()
	f.mustExec(t, `UPDATE room_waits SET retry_at = now(), since = now() - make_interval(secs => $2)
		WHERE root_run_id = $1 AND ended IS NULL`, f.run, ago.Seconds())
}

func waitingEvent(t *testing.T, events []db.RunEvent) waitingPayload {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "waiting" {
			var p waitingPayload
			if err := json.Unmarshal(events[i].Payload, &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	t.Fatal("no waiting event")
	return waitingPayload{}
}

// A run whose Bash the sandbox has no room for does not go on without it:
// the command is not run, the model says it waits, and the run parks as
// waiting for the sandbox, holding no worker, its wait kept for the sweep.
func TestARunTheSandboxHasNoRoomForParksToWait(t *testing.T) {
	f, ws, _, _ := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))

	f.mustExecute(t)

	row := f.row(t)
	if row.Status != "waiting" || waitsFor(deref(row.WaitingFor)) != waitsForRoom || deref(row.Output) != waitsForTheSandbox {
		t.Fatalf("run is %s for %q saying %q (%s), want waiting for the sandbox in its words",
			row.Status, deref(row.WaitingFor), deref(row.Output), deref(row.Error))
	}
	if p := waitingEvent(t, f.events(t)); p.Reason != string(waitsForRoom) || p.Said != waitsForTheSandbox {
		t.Errorf("waiting event = %+v", p)
	}
	waits := f.roomWaits(t)
	if len(waits) != 1 || waits[0].Ended != nil || waits[0].RunID != f.run || waits[0].Reason != "it is full" {
		t.Fatalf("waits = %+v, want one open for the run, with its refusal in a few words", waits)
	}
	if n := ws.room.calls("/exec"); n != 0 {
		t.Errorf("the command was sent %d times", n)
	}
	reqs := f.model.requests()
	if said := resultOf(reqs[len(reqs)-1].Messages, "c1"); !strings.Contains(said, "(it is full)") {
		t.Errorf("c1 reads %q, want the wait", said)
	}
}

// Once the sweep makes the room, the run is queued again, told there is
// room, and its call is made then.
func TestTheSweepMakesTheRoomAndTheRunCarriesOn(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
		callTool("c2", BashToolName, `{"command":"make"}`),
		answer("Built it.", 10, 5),
	))
	f.mustExecute(t)

	ws.ensureErr = nil
	ws.room.answer("/exec", 200, `{"stdout":"ok\n","stderr":"","exit_code":0}`)
	f.due(t, 2*time.Minute)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if got := resumed.got(); len(got) != 1 || got[0] != f.run {
		t.Fatalf("resumed %v, want the run", got)
	}
	if row := f.row(t); row.Status != "queued" || row.Attempt != 1 {
		t.Fatalf("run is %s at attempt %d, want queued again", row.Status, row.Attempt)
	}
	if w := f.roomWaits(t); len(w) != 1 || deref(w[0].Ended) != roomEndedRoom {
		t.Fatalf("waits = %+v, want ended with room", w)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := ws.room.calls("/exec"); n != 1 {
		t.Errorf("the command was sent %d times, want once, after the room", n)
	}
	reqs := f.model.requests()
	if told := lastUser(reqs[2].Messages); !strings.Contains(told, roomCame) {
		t.Errorf("the resumed run was told %q, want that there is room", told)
	}
	if w := f.roomWaits(t); w[0].ToldAt == nil {
		t.Error("the wait was not marked told")
	}
}

// Still no room: the wait is tried later, after as long as it has waited,
// at least a minute and at most ten, and the run stays parked.
func TestStillNoRoomPutsTheTryOff(t *testing.T) {
	for _, c := range []struct {
		waited time.Duration
		next   time.Duration
	}{
		{10 * time.Second, time.Minute},
		{5 * time.Minute, 5 * time.Minute},
		{40 * time.Minute, 10 * time.Minute},
	} {
		t.Run(c.waited.String(), func(t *testing.T) {
			f, ws, j, resumed := roomFixture(t, newScripted(
				callTool("c1", BashToolName, `{"command":"make"}`),
				answer(waitsForTheSandbox, 10, 5),
			))
			f.mustExecute(t)
			f.due(t, c.waited)

			if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
				t.Fatalf("wake: %v", failed)
			}
			if e, _ := ws.counts(); e != 2 {
				t.Errorf("ensured %d times, want the sweep to try once", e)
			}
			if len(resumed.got()) != 0 || f.row(t).Status != "waiting" {
				t.Fatalf("run is %s, resumed %v; want it left waiting", f.row(t).Status, resumed.got())
			}
			var in float64
			f.mustScan(t, `SELECT extract(epoch FROM retry_at - now()) FROM room_waits WHERE root_run_id = $1`,
				[]any{f.run}, &in)
			if d := time.Duration(in * float64(time.Second)); d < c.next-5*time.Second || d > c.next+5*time.Second {
				t.Errorf("next try in %v, want %v", d, c.next)
			}
		})
	}
}

// An hour after the first refusal the wait is given up without another
// try, and the run is told the calls were not made and why.
func TestAWaitOfAnHourIsGivenUp(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
		answer("The sandbox is busy; I could not build it.", 10, 5),
	))
	f.mustExecute(t)
	f.due(t, roomGiveUpAfter)

	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if e, _ := ws.counts(); e != 1 {
		t.Errorf("ensured %d times, want no try once it has waited an hour", e)
	}
	if got := resumed.got(); len(got) != 1 {
		t.Fatalf("resumed %v", got)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	reqs := f.model.requests()
	told := lastUser(reqs[2].Messages)
	if !strings.Contains(told, "after an hour") || !strings.Contains(told, "(it is full)") || strings.Contains(told, "memory") ||
		!strings.Contains(told, "were not made") {
		t.Errorf("told %q, want the wait given up with its refusal", told)
	}
}

// Just short of an hour is still tried.
func TestAWaitShortOfAnHourIsTried(t *testing.T) {
	f, ws, j, _ := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))
	f.mustExecute(t)
	f.due(t, roomGiveUpAfter-time.Minute)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if e, _ := ws.counts(); e != 2 {
		t.Errorf("ensured %d times, want a try short of an hour", e)
	}
}

// A sandbox that answers with something waiting cannot end ends the wait,
// and the run's call meets that refusal itself.
func TestAnotherRefusalEndsTheWait(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))
	f.mustExecute(t)
	ws.ensureErr = sandbox.ErrRefused
	f.due(t, 2*time.Minute)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if w := f.roomWaits(t); deref(w[0].Ended) != roomEndedRoom || len(resumed.got()) != 1 {
		t.Fatalf("waits %+v, resumed %v; want ended and the run queued", w, resumed.got())
	}
}

// A run queued for another reason waits still while its tree waits for room.
func TestARunWithAnOpenWaitIsNotQueued(t *testing.T) {
	f, _, _, _ := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))
	f.mustExecute(t)
	if _, err := f.store.RequeueAnswered(t.Context(), f.run); err == nil {
		t.Fatal("a run whose wait is open was queued again")
	}
	f.mustExec(t, `UPDATE room_waits SET ended = 'room', ended_at = now() WHERE root_run_id = $1`, f.run)
	if _, err := f.store.RequeueAnswered(t.Context(), f.run); err != nil {
		t.Fatalf("RequeueAnswered once the wait ended: %v", err)
	}
}

// A run that ends while it waits leaves nothing for the sweep to make.
func TestARunCancelledWhileItWaitsEndsItsWait(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))
	f.mustExecute(t)
	if _, err := f.store.RequestRunCancel(t.Context(), f.run); err != nil {
		t.Fatalf("RequestRunCancel: %v", err)
	}
	if w := f.roomWaits(t); deref(w[0].Ended) != roomEndedCancelled {
		t.Fatalf("waits = %+v, want cancelled", w)
	}
	f.due(t, 2*time.Minute)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if e, _ := ws.counts(); e != 1 || len(resumed.got()) != 0 {
		t.Errorf("ensured %d, resumed %v after the run ended", e, resumed.got())
	}
}

// A person who writes again ends the wait at once, and the run reads it.
func TestAWaitEndedByThePersonIsToldSo(t *testing.T) {
	f, _, _, _ := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
		answer("Sure, skipping the build.", 10, 5),
	))
	f.mustExecute(t)
	if n, err := f.store.EndTreeRoomWaits(t.Context(), db.EndTreeRoomWaitsParams{RootRunID: f.run, Ended: roomEndedSuperseded}); err != nil || n != 1 {
		t.Fatalf("EndTreeRoomWaits = %d, %v", n, err)
	}
	if _, err := f.store.RequeueAnswered(t.Context(), f.run); err != nil {
		t.Fatalf("RequeueAnswered: %v", err)
	}
	f.mustExecute(t)
	if told := lastUser(f.model.requests()[2].Messages); !strings.Contains(told, roomSuperseded) {
		t.Errorf("told %q", told)
	}
}

// A delegate the sandbox has no room for waits as its own: its parent is
// told it waits for the sandbox, not for a person, and parks on it; the
// sweep queues the root, whose resume carries the child on to its call.
func TestADelegateWaitsForRoomAndCarriesOnWithItsParent(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("call_1", "delegate", delegateArgs),
		callTool("c1", BashToolName, `{"command":"refund 4411"}`),
		answer(waitsForTheSandbox, 10, 5),
		answer("Billing waits for the sandbox.", 10, 5),
		callTool("c2", BashToolName, `{"command":"refund 4411"}`),
		answer("re_118", 10, 5),
		answer("refunded: re_118", 10, 5),
	))
	billing := f.teamLead(t)
	f.mustExec(t, `UPDATE agents SET workspace = true WHERE id = $1`, billing)

	f.mustExecute(t)

	child := f.onlyChild(t)
	if child.Status != "waiting" || waitsFor(deref(child.WaitingFor)) != waitsForRoom {
		t.Fatalf("billing is %s for %q", child.Status, deref(child.WaitingFor))
	}
	if root := f.row(t); root.Status != "waiting" || waitsFor(deref(root.WaitingFor)) != waitsForRoom {
		t.Fatalf("triage is %s for %q, want waiting for the sandbox", root.Status, deref(root.WaitingFor))
	}
	reqs := f.model.requests()
	if said := resultOf(reqs[3].Messages, "call_1"); !strings.Contains(said, "billing is waiting for room in the team's sandbox") {
		t.Errorf("triage's call_1 reads %q", said)
	}

	ws.ensureErr = nil
	ws.room.answer("/exec", 200, `{"stdout":"re_118\n","stderr":"","exit_code":0}`)
	f.due(t, 2*time.Minute)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if got := resumed.got(); len(got) != 1 || got[0] != f.run {
		t.Fatalf("resumed %v, want the root", got)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if got := deref(f.row(t).Output); got != "refunded: re_118" {
		t.Errorf("output = %q", got)
	}
	if n := ws.room.calls("/exec"); n != 1 {
		t.Errorf("the refund ran %d times", n)
	}
}

func lastUser(msgs []agent.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleUser {
			return msgs[i].Text()
		}
	}
	return ""
}

// A top-level run that parks only for room tells its conversation what it
// said, as a step is sent, whatever the channel's reply mode, and asks
// nobody anything; one that said nothing says it waits.
func TestARunWaitingForRoomTellsItsConversation(t *testing.T) {
	for said, want := range map[string]string{
		"Waiting for the sandbox to free up.": "Waiting for the sandbox to free up.",
		"":                                    waitingQuietly,
	} {
		a := waitingIn(t, "member", false)
		if _, err := a.raw.Exec(t.Context(), `DELETE FROM approvals WHERE run_id = $1`, a.run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.WaitForRoom(t.Context(), a.run.ID, "it is full", 60); err != nil {
			t.Fatal(err)
		}
		parked, again, asked, err := parkAndAsk(t.Context(), a.store, a.run.ID, worker, false, &said, waitsForRoom, []byte(`{}`))
		if err != nil || !parked || again != nil || asked == 0 {
			t.Fatalf("park = parked %v, queued %v, asked %d, %v; want parked, waiting, its conversation told", parked, again, asked, err)
		}
		var kind, text string
		var seq int32
		if err := a.raw.QueryRow(t.Context(), `SELECT kind, text, event_seq FROM replies WHERE run_id = $1`, a.run.ID).
			Scan(&kind, &text, &seq); err != nil {
			t.Fatalf("read the reply: %v", err)
		}
		if kind != "event" || text != want || seq != asked {
			t.Errorf("reply = %s %q at %d, want an event saying %q at %d", kind, text, seq, want, asked)
		}
		if got := a.prompts(t); len(got) != 0 {
			t.Errorf("prompts = %+v, want nobody asked", got)
		}
		row, err := a.store.GetRun(t.Context(), a.run.ID)
		if err != nil || waitsFor(deref(row.WaitingFor)) != waitsForRoom {
			t.Errorf("waiting for %q, %v", deref(row.WaitingFor), err)
		}
	}
}

// slowRooms is a sandbox whose every room takes its time to make.
type slowRooms struct {
	*fakeWorkspaces
	delay time.Duration
}

func (s slowRooms) Ensure(ctx context.Context, team, session uuid.UUID) (sandbox.Endpoint, error) {
	time.Sleep(s.delay)
	return s.fakeWorkspaces.Ensure(ctx, team, session)
}

// A sweep starts tries for its budget and no longer: a wait it does not
// reach stays due, for the next sweep, and a wait given up is still ended.
func TestASweepStartsTriesOnlyWithinItsBudget(t *testing.T) {
	f, ws, j, resumed := roomFixture(t, newScripted(
		callTool("c1", BashToolName, `{"command":"make"}`),
		answer(waitsForTheSandbox, 10, 5),
	))
	f.mustExecute(t)
	f.exec.builder.workspaces.Rooms = slowRooms{ws, 50 * time.Millisecond}
	j.wakeBudget = 0
	f.due(t, 2*time.Minute)

	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if e, _ := ws.counts(); e != 1 {
		t.Errorf("ensured %d times, want no try past the budget", e)
	}
	if w := f.roomWaits(t); w[0].Ended != nil || len(resumed.got()) != 0 {
		t.Fatalf("waits %+v, resumed %v; want the wait left due", w, resumed.got())
	}

	f.due(t, roomGiveUpAfter)
	if failed := j.wakeRoomWaits(t.Context()); len(failed) != 0 {
		t.Fatalf("wake: %v", failed)
	}
	if w := f.roomWaits(t); deref(w[0].Ended) != roomEndedGaveUp || len(resumed.got()) != 1 {
		t.Fatalf("waits %+v, resumed %v; want a given-up wait ended past the budget", w, resumed.got())
	}
}
