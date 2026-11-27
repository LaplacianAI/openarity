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
	says(t, out, `Proposed a change to "summary"`, "its objective", "runs as it was")
	got, err := w.service.Schedule(t.Context(), w.who(), Ref{ID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != v.Objective || got.Status != Active || got.Pending == nil {
		t.Fatalf("objective %q status %s pending %s: want it running as it was, the change waiting", got.Objective, got.Status, got.Pending)
	}
	if says, since := got.WaitingChange(); says != "its objective" || since == nil {
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
		`Proposed a change to "prices"`, "every 3h")
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
