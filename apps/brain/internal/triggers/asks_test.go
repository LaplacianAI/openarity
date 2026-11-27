package triggers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A watcher that runs is asked of as a schedule is: a delete, a look now or
// a resume waits for the yes, and nothing changes before it. A pause acts at
// once, as does deleting one never confirmed.
func TestWhatActsOnAWatcherThatRunsWaitsForTheYes(t *testing.T) {
	w := newWorld(t)
	v, err := w.service.CreateWatcher(t.Context(), w.who(), Origin{}, WatcherInput{
		Name: "prices", Objective: "Tell me what moved.", Source: SourceWebPage, URL: "https://example.com/prices", Every: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := w.chat(t)
	status := func() string {
		var s string
		w.scan(t, &s, `SELECT status FROM watchers WHERE id = $1`, v.ID)
		return s
	}
	watch := func(o Origin, action string) string {
		return w.call(t, o, WatchToolName, map[string]any{"action": action, "ref": "prices"})
	}

	t.Run("run now", func(t *testing.T) {
		w.exec(t, `UPDATE watchers SET next_check_at = now() + interval '1 day' WHERE id = $1`, v.ID)
		says(t, watch(w.yes(t, c), askRunNow), `"prices" waits for the person's yes to run it now`, "reply with the code")
		var due bool
		w.scan(t, &due, `SELECT next_check_at <= $2 FROM watchers WHERE id = $1`, v.ID, w.clock)
		if due {
			t.Fatal("it looks before the yes")
		}
		says(t, watch(w.yes(t, c), "confirm"), `Confirmed "prices"`)
		w.scan(t, &due, `SELECT next_check_at <= $2 FROM watchers WHERE id = $1`, v.ID, w.clock)
		if !due {
			t.Error("it does not look after the yes")
		}
	})

	t.Run("pause", func(t *testing.T) {
		says(t, watch(w.yes(t, c), "pause"), `Paused "prices"`)
		if got := status(); got != Paused {
			t.Fatalf("status %s after a pause, want paused at once", got)
		}
	})

	t.Run("resume", func(t *testing.T) {
		says(t, watch(w.yes(t, c), askResume), "waits for the person's yes to resume it")
		if got := status(); got != Paused {
			t.Fatalf("status %s before the yes, want paused", got)
		}
		says(t, watch(w.yes(t, c), "confirm"), `Confirmed "prices"`)
		if got := status(); got != Active {
			t.Fatalf("status %s after the yes, want active", got)
		}
	})

	t.Run("delete", func(t *testing.T) {
		says(t, watch(w.yes(t, c), askDelete), "waits for the person's yes to delete it")
		if got := status(); got != Active {
			t.Fatalf("status %s before the yes, want active", got)
		}
		says(t, watch(w.yes(t, c), "confirm"), `Deleted "prices"`)
		if got := status(); got != Deleted {
			t.Errorf("status %s after the yes, want deleted", got)
		}
	})

	t.Run("a proposal is deleted at once", func(t *testing.T) {
		p := w.yes(t, c)
		says(t, w.call(t, p, WatchToolName, map[string]any{"action": "create", "name": "draft", "objective": "Watch.", "source": "feed", "url": "https://example.com/feed"}),
			`Proposed "draft"`)
		says(t, w.call(t, p, WatchToolName, map[string]any{"action": "delete", "ref": "draft"}), `Deleted the proposal "draft"`)
		var s string
		w.scan(t, &s, `SELECT status FROM watchers WHERE name = 'draft'`)
		if s != Deleted {
			t.Errorf("status %s, want the proposal deleted", s)
		}
	})
}

// A deleted watcher drops what an agent asked of it.
func TestDeletingAWatcherDropsWhatWaits(t *testing.T) {
	w := newWorld(t)
	v, err := w.service.CreateWatcher(t.Context(), w.who(), Origin{}, WatcherInput{
		Name: "prices", Objective: "Tell me what moved.", Source: SourceWebPage, URL: "https://example.com/prices", Every: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := w.chat(t)
	w.call(t, c.origin, WatchToolName, map[string]any{"action": "update", "ref": "prices", "every": "3h"})
	if err := w.service.DeleteWatcher(t.Context(), w.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	var waits bool
	w.scan(t, &waits, `SELECT pending IS NOT NULL OR yes_code IS NOT NULL FROM watchers WHERE id = $1`, v.ID)
	if waits {
		t.Error("a deleted watcher still waits on an agent's change")
	}
}

// Two yeses act once: the ask is taken by the first.
func TestASecondYesActsOnNothing(t *testing.T) {
	w := newWorld(t)
	w.make(t, hourly("summary"))
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": askRunNow, "ref": "summary"})
	first, second := w.yes(t, c), w.yes(t, c)
	says(t, w.call(t, first, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
	says(t, w.call(t, second, ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), "Refused:", "not waiting to be confirmed")
	if n := w.runs.count(); n != 1 {
		t.Errorf("%d runs from two yeses, want 1", n)
	}
}

// An ask that no longer fits by the time of the yes is taken and refused,
// to be asked for again: nothing is done twice or half.
func TestAnAskOvertakenBeforeTheYesIsRefused(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	if _, err := w.service.Pause(t.Context(), w.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": askResume, "ref": "summary"})
	// The person resumes it themselves first.
	if _, err := w.service.Resume(t.Context(), w.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "confirmed, but it could not resume", "ask for it again")
	var waits bool
	w.scan(t, &waits, `SELECT pending IS NOT NULL FROM schedules WHERE id = $1`, v.ID)
	if waits {
		t.Error("the overtaken ask still waits")
	}
}

// A yes counts only in the conversation the ask was made in, from its
// maker, with its code; an action no agent may ask for is refused.
func TestAYesIsTheMakersOwnReplyWithItsCode(t *testing.T) {
	w := newWorld(t)
	w.make(t, hourly("summary"))
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": askDelete, "ref": "summary"})
	code := w.codes(t)

	t.Run("a reply without the code", func(t *testing.T) {
		says(t, w.call(t, w.say(t, c, "thanks!"), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
			"Refused:", "does not carry this change's code")
	})
	t.Run("another conversation", func(t *testing.T) {
		elsewhere := c
		w.scan(t, &elsewhere.session, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id) VALUES ($1, $2, 'elsewhere', 'direct', $3) RETURNING id`,
			w.team, c.channel, w.maker)
		says(t, w.call(t, w.say(t, elsewhere, "yes "+code), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
			"Refused:", "confirmed in the conversation it was proposed in")
	})
	t.Run("an action no agent asks for", func(t *testing.T) {
		_, err := w.service.ProposeScheduleAction(t.Context(), w.who(), c.origin, Ref{Name: "summary"}, "explode")
		kindIs(t, err, Invalid, "an agent asks to delete, resume or run one now")
	})
	t.Run("the code", func(t *testing.T) {
		says(t, w.call(t, w.say(t, c, "ok "+code+" do it"), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
			`Deleted "summary"`)
	})
}

// A revision restarts the hour: a yes 70 minutes after the proposal and 20
// after its revision confirms it, and one an hour after the revision does
// not.
func TestARevisionRestartsTheHour(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		ok    bool
	}{{"within the hour of the revision", 20 * time.Minute, true}, {"an hour after it", 61 * time.Minute, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			c := w.chat(t)
			w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
			w.clock = w.clock.Add(50 * time.Minute)
			says(t, w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Summarise more."}), "Revised")
			w.clock = w.clock.Add(tc.after)
			out := w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"})
			if tc.ok {
				says(t, out, `Confirmed "summary"`)
			} else {
				says(t, out, "Refused:", "lapsed")
			}
		})
	}
}

func TestAYesCarriesTheCodeAsANumberOfItsOwn(t *testing.T) {
	for text, want := range map[string]bool{
		"yes 4821": true, "4821": true, "ok, 4821!": true, "<@U12345> yes 4821": true, "yes4821": true,
		"yes": false, "yes 14821": false, "yes 48210": false, "thanks": false, "yes 482 1": false,
	} {
		if got := carriesCode(text, "4821"); got != want {
			t.Errorf("carriesCode(%q) = %v, want %v", text, got, want)
		}
	}
	if c := *newYesCode(); len(c) != yesCodeDigits {
		t.Errorf("a code %q is not %d digits", c, yesCodeDigits)
	}
}

// Arguments are read exactly as written: a key spelled otherwise than the
// tool's own, or one it does not take, is refused rather than guessed.
func TestTheToolsReadOnlyTheirOwnKeys(t *testing.T) {
	for raw, want := range map[string]string{
		`{"Action":"list"}`:                 `no field "Action"`,
		`{"action":"create","secret":"x"}`:  `no field "secret"`,
		`["list"]`:                          "not this tool's",
		`{"action":"list","action":"list"}`: "",
	} {
		_, err := parseToolArgs([]byte(raw))
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", raw, err)
			}
			continue
		}
		kindIs(t, err, Invalid, want)
	}
}

// A revision has a code of its own: the one given before no longer confirms.
func TestARevisionHasANewCode(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	before := w.codes(t)
	// A new code is the old one by chance once in ten thousand; five
	// revisions that all keep it are a revision that keeps it.
	var after string
	for range 5 {
		out := w.call(t, w.yes(t, c), ScheduleToolName, map[string]any{"action": "update", "ref": "summary", "objective": "Summarise more."})
		after = w.codes(t)
		says(t, out, "Revised", "reply with the code "+after)
		if after != before {
			break
		}
	}
	if after == before {
		t.Fatalf("five revisions kept the code %s", before)
	}
	says(t, w.call(t, w.say(t, c, "yes "+before), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}),
		"Refused:", "does not carry")
	says(t, w.call(t, w.say(t, c, "yes "+after), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
}

// The person's own edit of a proposal keeps its code, so their reply still
// confirms it.
func TestAPersonsEditKeepsTheProposalsCode(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	code := w.codes(t)
	objective := "Summarise briefly."
	if _, err := w.service.UpdateSchedule(t.Context(), w.who(), Ref{Name: "summary"}, SchedulePatch{Objective: &objective}); err != nil {
		t.Fatal(err)
	}
	says(t, w.call(t, w.say(t, c, "yes "+code), ScheduleToolName, map[string]any{"action": "confirm", "ref": "summary"}), `Confirmed "summary"`)
}

// The list gives the code of what waits, for a person who lost it.
func TestTheListGivesTheCode(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "create", "name": "summary", "objective": "Summarise.", "every": "1h"})
	says(t, w.call(t, c.origin, ScheduleToolName, map[string]any{"action": "list"}), "the person confirms with the code "+w.codes(t))
}

// A confirmed watcher has no code left to confirm it with.
func TestAConfirmedWatcherKeepsNoCode(t *testing.T) {
	w := newWorld(t)
	c := w.chat(t)
	w.call(t, c.origin, WatchToolName, map[string]any{"action": "create", "name": "feed", "objective": "Watch.", "source": "feed", "url": "https://example.com/feed"})
	says(t, w.call(t, w.yes(t, c), WatchToolName, map[string]any{"action": "confirm", "ref": "feed"}), `Confirmed "feed"`)
	var left bool
	w.scan(t, &left, `SELECT yes_code IS NOT NULL FROM watchers WHERE name = 'feed'`)
	if left {
		t.Error("a confirmed watcher kept its code")
	}
}

// An act that fails after its ask was taken is said as one to ask for again;
// the failure is not lost behind a confirm that did nothing.
func TestAFailedActAfterTheTakeSaysToAskAgain(t *testing.T) {
	w := newWorld(t)
	err := w.service.confirmedButNot(t.Context(), uuid.New(), errors.New("the database is down"), askRunNow)
	kindIs(t, err, Conflict, "confirmed, but it could not run now just now; ask for it again")
	if strings.Contains(err.Error(), "database") {
		t.Errorf("the failure reached the model: %v", err)
	}
	if err := w.service.confirmedButNot(t.Context(), uuid.New(), nil, askRunNow); err != nil {
		t.Errorf("nothing failed, yet %v", err)
	}
}
