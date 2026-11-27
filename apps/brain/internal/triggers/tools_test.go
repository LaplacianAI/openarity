package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// chat is a conversation of the maker's on the dashboard, with a message of
// theirs and the run it started: what an agent's tools are called from.
type chat struct {
	session, channel uuid.UUID
	origin           Origin
}

func (w *world) chat(t *testing.T) chat {
	t.Helper()
	// A proposal lapses by the database's clock, so the service reads the same one.
	w.clock = time.Now().Truncate(time.Second)
	var c chat
	w.scan(t, &c.channel, `SELECT id FROM channels WHERE team_id = $1 AND provider = 'dashboard'`, w.team)
	w.scan(t, &c.session, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id) VALUES ($1, $2, 'chat', 'direct', $3) RETURNING id`,
		w.team, c.channel, w.maker)
	var msg uuid.UUID
	w.scan(t, &msg, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at) VALUES ($1, $2, $4, 'm1', 'remind me', $3) RETURNING id`,
		c.channel, c.session, w.clock.Add(-time.Minute), w.maker)
	c.origin = Origin{Run: w.chatRun(t, c.session, msg), Session: c.session}
	return c
}

// yes is a later message from the maker in c, and the run it started.
func (w *world) yes(t *testing.T, c chat) Origin {
	t.Helper()
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE session_id = $1 AND finished_at IS NULL`, c.session)
	var msg uuid.UUID
	w.scan(t, &msg, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at)
		VALUES ($1, $2, $3, gen_random_uuid()::text, 'yes', now() + interval '1 day') RETURNING id`, c.channel, c.session, w.maker)
	return Origin{Run: w.chatRun(t, c.session, msg), Session: c.session}
}

// tools are the run's tools, as the maker's run of the hub holds them.
func (w *world) tools(t *testing.T, o Origin) map[string]agent.Tool {
	t.Helper()
	tools, why, err := w.service.Tools(t.Context(), w.toolRun(o))
	if err != nil || why != "" {
		t.Fatalf("Tools = %q, %v", why, err)
	}
	out := map[string]agent.Tool{}
	for _, tl := range tools {
		out[tl.Name] = tl
	}
	return out
}

func (w *world) toolRun(o Origin) ToolRun {
	return ToolRun{
		Team: w.team, User: w.maker, Run: o.Run, Session: o.Session, Dashboard: "https://openarity.example",
		Agent: func(context.Context, string) (uuid.UUID, error) { return uuid.Nil, errors.New("no such agent") },
	}
}

// call invokes the tool named in o's run with args, failing on an error.
func (w *world) call(t *testing.T, o Origin, tool string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.tools(t, o)[tool].Invoke(t.Context(), raw)
	if err != nil {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	return out
}

func says(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("the tool said %q, want it to say %q", got, w)
		}
	}
}

// A schedule an agent makes is a proposal: confirmed in the run that made it,
// it is refused; a later message from the maker confirms it.
func TestAToolProposesAndOnlyALaterMessageConfirms(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	out := w.call(t, c.origin, ScheduleToolName, map[string]any{
		"action": "create", "name": "summary", "objective": "Summarise what changed.", "every": "1d",
	})
	says(t, out, `Proposed "summary"`, "every 1d", "in UTC", "Ask the person to confirm")
	var status string
	w.scan(t, &status, `SELECT status FROM schedules WHERE name = 'summary'`)
	if status != Proposed {
		t.Fatalf("status %s, want proposed", status)
	}

	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "not in the run that proposed it")
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		`Confirmed "summary"`)
	w.scan(t, &status, `SELECT status FROM schedules WHERE name = 'summary'`)
	if status != Active {
		t.Errorf("status %s after a later yes, want active", status)
	}
}

// A person who may not schedule, a team that does not let agents, and a run a
// schedule started hold neither tool, and are told why.
func TestTheToolsAreWithheldWhereTheyMayNotBeUsed(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	why := func() string {
		t.Helper()
		tools, why, err := w.service.Tools(t.Context(), w.toolRun(c.origin))
		if err != nil {
			t.Fatal(err)
		}
		if why != "" && len(tools) != 0 {
			t.Errorf("tools %v given with %q", tools, why)
		}
		return why
	}
	if got := why(); got != "" {
		t.Fatalf("a member's run has no tools: %q", got)
	}

	w.exec(t, `UPDATE team_members SET role = 'guest' WHERE user_id = $1`, w.maker)
	if got := why(); !strings.Contains(got, "schedule:write") {
		t.Errorf("a guest's run: %q, want refused for schedule:write", got)
	}
	w.exec(t, `UPDATE team_members SET role = 'member' WHERE user_id = $1`, w.maker)

	w.exec(t, `INSERT INTO team_settings (team_id, agents_may_schedule) VALUES ($1, false)
		ON CONFLICT (team_id) DO UPDATE SET agents_may_schedule = false`, w.team)
	if got := why(); !strings.Contains(got, "team's settings") {
		t.Errorf("the team's switch off: %q", got)
	}
	w.exec(t, `UPDATE team_settings SET agents_may_schedule = true WHERE team_id = $1`, w.team)

	// A run in a schedule's own conversation is one it started.
	v := w.make(t, hourly("summary"))
	_, got, err := w.service.Tools(t.Context(), w.toolRun(Origin{Run: c.origin.Run, Session: v.SessionID}))
	if err != nil || !strings.Contains(got, "a schedule or watcher started") {
		t.Errorf("a fired run: %q, %v", got, err)
	}
}

// A webhook watcher an agent proposes waits for its secret, made on the
// dashboard from a link: no secret is in the tool's answer.
func TestAWebhookFromAToolGivesALinkAndNoSecret(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	out := w.call(t, c.origin, WatchToolName, map[string]any{
		"action": "create", "name": "releases", "objective": "Tell me what was released.", "source": "webhook",
	})
	var id uuid.UUID
	var status string
	w.scan(t, &id, `SELECT id FROM watchers WHERE name = 'releases'`)
	w.scan(t, &status, `SELECT status FROM watchers WHERE id = $1`, id)
	says(t, out, "waiting for its secret", "https://openarity.example/ui/schedules/watchers/"+id.String(), "Never ask them to paste the secret")
	if status != WaitingSecret || strings.Contains(out, "oawh_") {
		t.Errorf("status %s, answer %q: want waiting for its secret, with none shown", status, out)
	}
	says(t, w.call(t, w.yes(t, c), WatchToolName, map[string]any{"action": "confirm", "ref": "releases"}),
		"starts once the person makes its secret", "/ui/schedules/watchers/"+id.String())
}

// An agent's change to a schedule that runs waits for the maker's yes: it
// runs as it was until a later message confirms the change.
func TestAnAgentsChangeWaitsForTheMakersYes(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	c := w.chat(t)
	out := w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Delete every file."})
	says(t, out, `"summary" waits for the person's yes to change its objective`, "runs as it was")
	got, err := w.service.Schedule(t.Context(), w.who(), Ref{ID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != v.Objective || got.Status != Active || got.Pending == nil {
		t.Fatalf("objective %q status %s pending %s: want it running as it was, the change waiting", got.Objective, got.Status, got.Pending)
	}
	if says, since := got.WaitingChange(); says != "to change its objective" || since == nil {
		t.Errorf("waiting change %q since %v", says, since)
	}

	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "not in the run that proposed it")
	// Nor one who manages everyone's: the change runs as its maker.
	_, err = w.service.Confirm(t.Context(), Who{Team: w.team, User: w.other, Manage: true}, Origin{}, Ref{ID: v.ID})
	kindIs(t, err, Forbidden, "only the person who made it")
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
	got, err = w.service.Schedule(t.Context(), w.who(), Ref{ID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != "Delete every file." || got.Pending != nil || got.Status != Active {
		t.Errorf("after the yes: objective %q pending %s status %s", got.Objective, got.Pending, got.Status)
	}
}

// A change nobody confirmed within the hour is dropped by the sweep, and the
// schedule runs on as it was.
func TestALapsedChangeIsDroppedAndTheScheduleRunsOn(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "every": "2h"})
	w.exec(t, `UPDATE schedules SET pending_at = now() - interval '2 hours' WHERE id = $1`, v.ID)
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), "Refused:", "lapsed")

	j := NewJob(w.store, nil, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	j.now = time.Now
	if err := j.sweepOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, rule string
	var pending bool
	w.scan(t, &status, `SELECT status FROM schedules WHERE id = $1`, v.ID)
	w.scan(t, &rule, `SELECT rule FROM schedules WHERE id = $1`, v.ID)
	w.scan(t, &pending, `SELECT pending IS NOT NULL FROM schedules WHERE id = $1`, v.ID)
	if status != Active || rule != "3600" || pending {
		t.Errorf("status %s rule %s pending %v: want it running hourly with nothing waiting", status, rule, pending)
	}
}

// The maker's own change replaces an agent's waiting one, so a later yes
// cannot apply what they changed past.
func TestTheMakersOwnChangeReplacesAWaitingOne(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Something else."})
	name := "daily summary"
	if _, err := w.service.UpdateSchedule(t.Context(), w.who(), Ref{ID: v.ID}, SchedulePatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": name}),
		"Refused:", "not waiting to be confirmed")
	var objective string
	w.scan(t, &objective, `SELECT objective FROM schedules WHERE id = $1`, v.ID)
	if objective != v.Objective {
		t.Errorf("objective %q: the replaced change was applied", objective)
	}
}

// A proposal a later run revises is confirmed by a message after that run,
// not by the run that revised it.
func TestARevisedProposalIsNotConfirmedByTheRunThatRevisedIt(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	second := w.yes(t, c)
	says(t, w.call(t, second, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Summarise briefly."}),
		`Revised the proposal "summary"`)
	says(t, w.call(t, second, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "not in the run that proposed it")
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
}

// A watcher's change waits for the same yes as a schedule's.
func TestAnAgentsChangeToAWatcherWaitsForTheYes(t *testing.T) {
	w := newWorld(t)
	v, err := w.service.CreateWatcher(t.Context(), w.who(), Origin{}, WatcherInput{
		Name: "prices", Objective: "Tell me what moved.", Source: SourceWebPage, URL: "https://example.com/prices", Every: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := w.chat(t)
	says(t, w.call(t, c.origin, WatchToolName, map[string]any{"action": "update", "ref": "prices", "every": "3h"}),
		`"prices" waits for the person's yes to change how often it looks, to every 3h`)
	var every int32
	w.scan(t, &every, `SELECT every_seconds FROM watchers WHERE id = $1`, v.ID)
	if every != 7200 {
		t.Fatalf("every %d before the yes, want 7200", every)
	}
	says(t, w.call(t, w.yes(t, c), WatchToolName, map[string]any{"action": "confirm", "ref": "prices"}), `Confirmed "prices"`)
	w.scan(t, &every, `SELECT every_seconds FROM watchers WHERE id = $1`, v.ID)
	if every != 3*3600 {
		t.Errorf("every %d after the yes, want %d", every, 3*3600)
	}
}

// On the dashboard, here is a conversation of the schedule's own, and the
// person's direct messages are not a place to ask for.
func TestOnTheDashboardHereIsAConversationOfItsOwn(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{
		"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h", "target": "me",
	}), "Refused:", "here is already the person's alone")
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{
		"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h",
	}), "in a conversation of its own on the dashboard")
}

// A watcher's waiting change is replaced by its maker's own, and one left
// unconfirmed is dropped by the sweep.
func TestAWatchersWaitingChangeIsReplacedOrDropped(t *testing.T) {
	w := newWorld(t)
	v, err := w.service.CreateWatcher(t.Context(), w.who(), Origin{}, WatcherInput{
		Name: "prices", Objective: "Tell me what moved.", Source: SourceWebPage, URL: "https://example.com/prices", Every: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := w.chat(t)
	pending := func() bool {
		var p bool
		w.scan(t, &p, `SELECT pending IS NOT NULL FROM watchers WHERE id = $1`, v.ID)
		return p
	}
	w.call(t, c.origin, WatchToolName, map[string]any{"action": "update", "ref": "prices", "every": "3h"})
	objective := "Tell me what moved, briefly."
	if _, err := w.service.UpdateWatcher(t.Context(), w.who(), Ref{ID: v.ID}, WatcherPatch{Objective: &objective}); err != nil {
		t.Fatal(err)
	}
	if pending() {
		t.Error("the maker's own change left the agent's waiting")
	}

	w.call(t, c.origin, WatchToolName, map[string]any{"action": "update", "ref": "prices", "every": "3h"})
	w.exec(t, `UPDATE watchers SET pending_at = now() - interval '2 hours' WHERE id = $1`, v.ID)
	j := NewJob(w.store, nil, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	j.now = time.Now
	if err := j.sweepOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	w.scan(t, &status, `SELECT status FROM watchers WHERE id = $1`, v.ID)
	if pending() || status != Active {
		t.Errorf("after the sweep: pending %v status %s, want it dropped and the watcher active", pending(), status)
	}
}

// A message the person sent while the proposing run still worked was not an
// answer to it, and confirms nothing.
func TestAMessageBeforeTheProposingRunFinishedConfirmsNothing(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	var msg uuid.UUID
	w.scan(t, &msg, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at)
		VALUES ($1, $2, $3, 'early', 'yes', now() + interval '1 minute') RETURNING id`, c.channel, c.session, w.maker)
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() + interval '2 minutes' WHERE id = $1`, c.origin.Run)
	early := Origin{Run: w.chatRun(t, c.session, msg), Session: c.session}
	says(t, w.call(t, early, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "after the run that proposed it finished")
}

// A proposal revised is confirmed only by a message after the revision.
func TestARevisionNeedsAYesAfterIt(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, c.origin.Run)
	// A message sent after the proposal, whose run waits.
	var between uuid.UUID
	w.scan(t, &between, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at)
		VALUES ($1, $2, $3, 'between', 'hmm', now() + interval '1 minute') RETURNING id`, c.channel, c.session, w.maker)
	waiting := Origin{Run: w.chatRun(t, c.session, between), Session: c.session}
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE id = $1`, waiting.Run)
	// Another later run revises it, an hour on.
	w.clock = w.clock.Add(30 * time.Minute)
	reviser := w.yes(t, c)
	w.exec(t, `UPDATE messages SET received_at = now() + interval '2 minutes' WHERE id = (SELECT message_id FROM runs WHERE id = $1)`, reviser.Run)
	says(t, w.call(t, reviser, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Delete things."}), "Revised")
	says(t, w.call(t, waiting, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "sent after the proposal")
}

// A confirm in a run that has read anything is refused: what it read could
// be what says to confirm. A call recorded before a wait counts too.
func TestAConfirmComesBeforeAnyRead(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	yes := w.yes(t, c)
	invoke := func(r ToolRun) string {
		t.Helper()
		tools, _, err := w.service.Tools(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		out, err := tools[0].Invoke(t.Context(), json.RawMessage(`{"action":"confirm","ref":"summary"}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := w.toolRun(yes)
	read.ReadOutside = func() bool { return true }
	says(t, invoke(read), "Refused:", "a confirm comes before anything else")

	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, 1, 'tool_call', '{"name":"web_fetch"}')`, yes.Run)
	says(t, invoke(w.toolRun(yes)), "Refused:", "a confirm comes before anything else")

	// Its own plan and a list are not reads.
	clean := w.yes(t, c)
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, 1, 'tool_call', '{"name":"plan"}'),
		($1, 2, 'tool_call', '{"name":"schedule"}')`, clean.Run)
	says(t, invoke(w.toolRun(clean)), `Confirmed "summary"`)
}

// A delete, a resume and a run now of one that runs wait for the yes; a
// pause acts at once; a proposal is deleted at once.
func TestWhatActsOnOneThatRunsWaitsForTheYes(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	c := w.chat(t)
	status := func() string {
		var s string
		w.scan(t, &s, `SELECT status FROM schedules WHERE id = $1`, v.ID)
		return s
	}

	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "run_now", "ref": "summary"}),
		`"summary" waits for the person's yes to run it now`)
	if n := w.runs.count(); n != 0 {
		t.Fatalf("%d runs started before the yes", n)
	}
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
	if n := w.runs.count(); n != 1 {
		t.Errorf("%d runs after the yes, want 1", n)
	}
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE finished_at IS NULL`)

	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "resume", "ref": "summary"}), "Refused:", "only a paused schedule")
	var waits bool
	w.scan(t, &waits, `SELECT pending IS NOT NULL FROM schedules WHERE id = $1`, v.ID)
	if waits {
		t.Fatal("a refused resume of one that runs was held for a yes")
	}

	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "pause", "ref": "summary"}), `Paused "summary"`)
	if got := status(); got != Paused {
		t.Fatalf("status %s after a pause, want paused at once", got)
	}
	o := w.yes(t, c)
	says(t, w.call(t, o, ScheduleToolName, map[string]any{"action": "resume", "ref": "summary"}), "waits for the person's yes to resume it")
	if got := status(); got != Paused {
		t.Fatalf("status %s before the yes, want paused", got)
	}
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
	if got := status(); got != Active {
		t.Fatalf("status %s after the yes, want active", got)
	}

	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "delete", "ref": "summary"}), "waits for the person's yes to delete it")
	if got := status(); got != Active {
		t.Fatalf("status %s before the yes, want active", got)
	}
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Deleted "summary"`)
	if got := status(); got != Deleted {
		t.Errorf("status %s after the yes, want deleted", got)
	}

	p := w.yes(t, c)
	w.call(t, p, ScheduleToolName, map[string]any{"action": "create", "name": "draft", "objective": "Summarise.", "every": "1h"})
	says(t, w.call(t, p, ScheduleToolName, map[string]any{"action": "delete", "ref": "draft"}), `Deleted the proposal "draft"`)
}

// Taken over, a schedule drops what an agent asked of its old maker.
func TestTakingOverDropsWhatWasAskedOfTheOldMaker(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Something else."})
	w.exec(t, `UPDATE schedules SET created_by = NULL, status = 'paused', paused_reason = 'maker_gone', next_fire_at = NULL WHERE id = $1`, v.ID)
	if _, err := w.service.TakeOver(t.Context(), Who{Team: w.team, User: w.other, Manage: true}, Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	var pending bool
	w.scan(t, &pending, `SELECT pending IS NOT NULL FROM schedules WHERE id = $1`, v.ID)
	if pending {
		t.Error("the old maker's waiting change survived a take-over")
	}
}

// The list says what each is and what waits on it.
func TestTheListSaysWhatWaits(t *testing.T) {
	w := newWorld(t)
	w.make(t, hourly("summary"))
	c := w.chat(t)
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "list"}), `"summary" (active)`, "every 1h", "in UTC")
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "delete", "ref": "summary"})
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "list"}), "waiting for the person's yes to delete it")
	says(t, w.call(t, c.origin, WatchToolName, map[string]any{"action": "list"}), "no watchers")
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "secret": "x"}), "Refused:", "not this tool's")
}

func TestTheToolsReadLengthsAndTimes(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "2d": 48 * time.Hour, "1w": 7 * 24 * time.Hour, "90s": 90 * time.Second} {
		if got, err := durationOf(in); err != nil || got != want {
			t.Errorf("durationOf(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0h", "-1h", "1x", "d", "0d"} {
		if _, err := durationOf(in); KindOf(err) != Invalid {
			t.Errorf("durationOf(%q) = %v, want refused", in, err)
		}
	}
	at, err := atOf("2026-12-01 09:00", "Asia/Kolkata")
	if err != nil || !at.Equal(time.Date(2026, 12, 1, 3, 30, 0, 0, time.UTC)) {
		t.Errorf("a wall time in Kolkata = %v, %v", at, err)
	}
	if at, err := atOf("2026-12-01T09:00:00Z", "Asia/Kolkata"); err != nil || !at.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339 = %v, %v", at, err)
	}
	if _, err := atOf("tomorrow", "UTC"); KindOf(err) != Invalid {
		t.Errorf("a time not given as one: %v", err)
	}
	for d, want := range map[time.Duration]string{time.Hour: "1h", 90 * time.Minute: "90m", 48 * time.Hour: "2d", 90 * time.Second: "1m30s"} {
		if got := lengthSays(d); got != want {
			t.Errorf("lengthSays(%v) = %q, want %q", d, got, want)
		}
	}
}
