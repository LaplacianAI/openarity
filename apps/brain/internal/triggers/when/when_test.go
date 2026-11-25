package when

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, kind, rule, zone string, anchor time.Time) Rule {
	t.Helper()
	r, err := Parse(kind, rule, zone, anchor)
	if err != nil {
		t.Fatalf("Parse(%s, %q, %s): %v", kind, rule, zone, err)
	}
	return r
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseRefusesWhatIsNotARule(t *testing.T) {
	cases := []struct{ kind, rule, zone, want string }{
		{"cron", "0 9 * * 1-5", "Local", "not an IANA name"},
		{"cron", "0 9 * * 1-5", "Mars/Olympus", "not an IANA name"},
		{"cron", "0 9 * *", "UTC", "does not parse"},
		{"cron", "0 0 9 * * 1-5", "UTC", "does not parse"}, // seconds are not ours
		{"cron", "TZ=UTC 0 9 * * *", "UTC", "names no zone"},
		{"cron", "CRON_TZ=UTC 0 9 * * *", "UTC", "names no zone"},
		{"cron", "@every 1h", "UTC", "as every"},
		{"every", "0", "UTC", "whole number"},
		{"every", "1h", "UTC", "whole number"},
		{"at", "tomorrow", "UTC", "RFC 3339"},
		{"sometimes", "x", "UTC", "not \"sometimes\""},
	}
	for _, c := range cases {
		_, err := Parse(c.kind, c.rule, c.zone, time.Time{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%s, %q, %s) = %v, want an error saying %q", c.kind, c.rule, c.zone, err, c.want)
		}
	}
}

func TestNextForEachKind(t *testing.T) {
	anchor := utc("2026-12-01T09:00:00Z")
	every := mustParse(t, "every", "3600", "UTC", anchor)
	cases := []struct {
		name  string
		r     Rule
		after string
		want  string
		ok    bool
	}{
		{"every before its anchor is the anchor", every, "2026-11-30T00:00:00Z", "2026-12-01T09:00:00Z", true},
		{"every at the anchor is the one after", every, "2026-12-01T09:00:00Z", "2026-12-01T10:00:00Z", true},
		{"every between fires", every, "2026-12-01T11:30:00Z", "2026-12-01T12:00:00Z", true},
		{"at before it", mustParse(t, "at", "2026-12-01T09:00:00Z", "UTC", anchor), "2026-12-01T08:00:00Z", "2026-12-01T09:00:00Z", true},
		{"at once past", mustParse(t, "at", "2026-12-01T09:00:00Z", "UTC", anchor), "2026-12-01T09:00:00Z", "2026-12-01T09:00:00Z", false},
		{"cron in its zone", mustParse(t, "cron", "0 9 * * 1-5", "Asia/Kolkata", anchor), "2026-12-05T00:00:00Z", "2026-12-07T03:30:00Z", true},
		{"a descriptor", mustParse(t, "cron", "@daily", "UTC", anchor), "2026-12-01T09:00:00Z", "2026-12-02T00:00:00Z", true},
	}
	for _, c := range cases {
		got, ok := c.r.Next(utc(c.after))
		if ok != c.ok || !got.Equal(utc(c.want)) {
			t.Errorf("%s: Next = %s %v, want %s %v", c.name, got.UTC().Format(time.RFC3339), ok, c.want, c.ok)
		}
	}
}

func TestCheckHoldsARuleToTheTeamsLimits(t *testing.T) {
	now := utc("2026-12-01T00:00:00Z")
	hour := time.Hour
	cases := []struct {
		name string
		r    Rule
		want string
	}{
		{"cron every five minutes", mustParse(t, "cron", "*/5 * * * *", "UTC", now), "at most once every 1 hour"},
		{"cron twice an hour", mustParse(t, "cron", "0,30 * * * *", "UTC", now), "at most once every 1 hour"},
		{"cron that never fires", mustParse(t, "cron", "0 0 30 2 *", "UTC", now), "within a year"},
		{"every half an hour", mustParse(t, "every", "1800", "UTC", now), "at most once every 1 hour"},
		{"every in seconds", mustParse(t, "every", "3601", "UTC", now), "whole minutes"},
		{"every two months", mustParse(t, "every", "5356800", "UTC", now), "at most 31 days"},
		{"at in the past", mustParse(t, "at", "2026-11-30T00:00:00Z", "UTC", now), "already passed"},
		{"at two years away", mustParse(t, "at", "2028-12-01T00:00:00Z", "UTC", now), "more than a year"},
	}
	for _, c := range cases {
		err := c.r.Check(now, hour)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Check = %v, want %q", c.name, err, c.want)
		}
	}
	for _, ok := range []Rule{
		mustParse(t, "cron", "0 9 * * 1-5", "UTC", now),
		mustParse(t, "cron", "0 * * * *", "UTC", now),
		mustParse(t, "every", "3600", "UTC", now),
		mustParse(t, "at", "2026-12-25T09:00:00Z", "UTC", now),
	} {
		if err := ok.Check(now, hour); err != nil {
			t.Errorf("%s %q: Check = %v, want nil", ok.Kind, ok.Stored(), err)
		}
	}
	if err := mustParse(t, "every", "1800", "UTC", now).Check(now, 30*time.Minute); err != nil {
		t.Errorf("a team that allows half-hourly: %v", err)
	}
}

func TestGraceIsHalfThePeriodWithinItsBounds(t *testing.T) {
	at := utc("2026-12-01T09:00:00Z")
	cases := []struct {
		r    Rule
		want time.Duration
	}{
		{mustParse(t, "every", "3600", "UTC", at), 30 * time.Minute},
		{mustParse(t, "every", "86400", "UTC", at), 2 * time.Hour}, // half a day, capped
		{mustParse(t, "every", "300", "UTC", at), 2*time.Minute + 30*time.Second},
		{mustParse(t, "every", "120", "UTC", at), 2 * time.Minute}, // a minute, raised
		{mustParse(t, "cron", "0 * * * *", "UTC", at), 30 * time.Minute},
		{mustParse(t, "at", "2026-12-01T09:00:00Z", "UTC", at), 24 * time.Hour},
	}
	for _, c := range cases {
		if got := Grace(c.r, at); got != c.want {
			t.Errorf("%s %q: Grace = %s, want %s", c.r.Kind, c.r.Stored(), got, c.want)
		}
	}
}

func TestDueCollapsesWhatWasMissed(t *testing.T) {
	anchor := utc("2026-12-01T09:00:00Z")
	hourly := mustParse(t, "every", "3600", "UTC", anchor)
	daily := mustParse(t, "cron", "0 9 * * *", "UTC", anchor)

	cases := []struct {
		name      string
		r         Rule
		from, now string
		fire      Outcome // "" for none
		fireAt    string
		missed    int
		next      string
		done      bool
	}{
		{"on time", hourly, "2026-12-01T09:00:00Z", "2026-12-01T09:00:20Z", Started, "2026-12-01T09:00:00Z", 0, "2026-12-01T10:00:00Z", false},
		{"late within the grace", hourly, "2026-12-01T09:00:00Z", "2026-12-01T09:20:00Z", Late, "2026-12-01T09:00:00Z", 0, "2026-12-01T10:00:00Z", false},
		{"one beyond the grace catches up", hourly, "2026-12-01T09:00:00Z", "2026-12-01T09:45:00Z", CatchUp, "2026-12-01T09:00:00Z", 0, "2026-12-01T10:00:00Z", false},
		{"several due: newest catches up, the rest missed", hourly, "2026-12-01T09:00:00Z", "2026-12-01T12:10:00Z", CatchUp, "2026-12-01T12:00:00Z", 3, "2026-12-01T13:00:00Z", false},
		{"a week of a daily rule down", daily, "2026-12-01T09:00:00Z", "2026-12-08T10:00:00Z", CatchUp, "2026-12-08T09:00:00Z", 7, "2026-12-09T09:00:00Z", false},
		{"nothing due yet", hourly, "2026-12-01T09:00:00Z", "2026-12-01T08:59:00Z", "", "", 0, "2026-12-01T09:00:00Z", false},
	}
	for _, c := range cases {
		p := Due(c.r, utc(c.from), utc(c.now), "")
		switch {
		case c.fire == "" && p.Fire != nil:
			t.Errorf("%s: fired %+v, want nothing", c.name, *p.Fire)
		case c.fire != "" && (p.Fire == nil || p.Fire.Outcome != c.fire || !p.Fire.ScheduledFor.Equal(utc(c.fireAt))):
			t.Errorf("%s: fire = %+v, want %s at %s", c.name, p.Fire, c.fire, c.fireAt)
		}
		if len(p.Missed) != c.missed {
			t.Errorf("%s: %d missed, want %d", c.name, len(p.Missed), c.missed)
		}
		if p.Done != c.done || !p.Next.Equal(utc(c.next)) {
			t.Errorf("%s: next %s done %v, want %s %v", c.name, p.Next.Format(time.RFC3339), p.Done, c.next, c.done)
		}
	}
}

func TestDueKeepsOnlyTheNewestMissed(t *testing.T) {
	r := mustParse(t, "every", "3600", "UTC", utc("2026-12-01T00:00:00Z"))
	p := Due(r, utc("2026-12-01T00:00:00Z"), utc("2026-12-04T00:30:00Z"), "")
	if len(p.Missed) != maxMissed {
		t.Fatalf("%d missed kept, want %d", len(p.Missed), maxMissed)
	}
	if want := utc("2026-12-03T23:00:00Z"); !p.Missed[len(p.Missed)-1].Equal(want) {
		t.Errorf("the last missed is %s, want %s", p.Missed[len(p.Missed)-1], want)
	}
	if p.Fire == nil || !p.Fire.ScheduledFor.Equal(utc("2026-12-04T00:00:00Z")) {
		t.Errorf("fire = %+v, want the newest", p.Fire)
	}
}

func TestDueStartsAgainFromNowPastTheCount(t *testing.T) {
	r := mustParse(t, "every", "300", "UTC", utc("2026-01-01T00:00:00Z"))
	p := Due(r, utc("2026-01-01T00:00:00Z"), utc("2026-12-01T00:02:00Z"), "")
	if p.Fire == nil || p.Fire.Outcome != CatchUp {
		t.Fatalf("fire = %+v, want a catch-up", p.Fire)
	}
	if want := utc("2026-12-01T00:05:00Z"); !p.Next.Equal(want) {
		t.Errorf("next = %s, want %s: the fire after now, not the one after the last counted", p.Next, want)
	}
}

func TestDueForAOneShot(t *testing.T) {
	r := mustParse(t, "at", "2026-12-01T09:00:00Z", "UTC", time.Time{})
	late := Due(r, r.At, utc("2026-12-01T20:00:00Z"), "")
	if late.Fire == nil || late.Fire.Outcome != Late || !late.Done || !late.Next.IsZero() {
		t.Errorf("11 hours late: %+v, want it run late and done", late)
	}
	gone := Due(r, r.At, utc("2026-12-02T09:00:01Z"), "")
	if gone.Fire != nil || len(gone.Missed) != 1 || !gone.Done {
		t.Errorf("past a day: %+v, want it missed and done", gone)
	}
	early := Due(r, r.At, utc("2026-12-01T08:00:00Z"), "")
	if early.Fire != nil || early.Done || !early.Next.Equal(r.At) {
		t.Errorf("not yet due: %+v, want it waiting", early)
	}
}

// The clocks change in each zone a schedule may name: a local time that does
// not exist that day is skipped, and one that happens twice fires once.
func TestDaylightSaving(t *testing.T) {
	t.Run("spring forward skips the hour that does not exist", func(t *testing.T) {
		// New York, 8 March 2026: 02:00 becomes 03:00.
		r := mustParse(t, "cron", "30 2 * * *", "America/New_York", time.Time{})
		fires := r.Upcoming(utc("2026-03-07T00:00:00Z"), 3)
		for _, f := range fires {
			if f.In(r.Zone).Format("2006-01-02") == "2026-03-08" {
				t.Errorf("fired at %s, a local time that did not exist", f.In(r.Zone))
			}
		}
	})
	t.Run("fall back fires the repeated hour once", func(t *testing.T) {
		// London, 25 October 2026: 02:00 BST becomes 01:00 GMT, so 01:30 happens twice.
		r := mustParse(t, "cron", "30 1 * * *", "Europe/London", time.Time{})
		first, _ := r.Next(utc("2026-10-24T12:00:00Z"))
		p := Due(r, first, utc("2026-10-25T02:00:00Z"), "")
		if p.Fire == nil {
			t.Fatal("nothing fired on the day the clocks went back")
		}
		if len(p.Missed) != 0 {
			t.Errorf("%d missed: the repeated 01:30 must be dropped, not missed", len(p.Missed))
		}
		// The tick that fired the first 01:30 keeps its wall time; the second
		// one, an hour later, is the same wall time and must not fire.
		again := Due(r, p.Next, utc("2026-10-25T02:00:00Z"), p.Fire.Local)
		if again.Fire != nil && again.Fire.Local == p.Fire.Local {
			t.Errorf("01:30 fired twice: %s and %s", p.Fire.ScheduledFor, again.Fire.ScheduledFor)
		}
	})
	t.Run("upcoming names the new offset after a change", func(t *testing.T) {
		r := mustParse(t, "cron", "0 9 * * *", "Europe/London", time.Time{})
		fires := r.Upcoming(utc("2026-10-23T12:00:00Z"), 2)
		if len(fires) != 2 || fires[0].UTC().Hour() != 8 || fires[1].UTC().Hour() != 9 {
			t.Errorf("09:00 London either side of the change = %v, want 08:00Z then 09:00Z", fires)
		}
	})
}

func TestStoredRoundTrips(t *testing.T) {
	for _, r := range []Rule{
		mustParse(t, "at", "2026-12-01T09:00:00+05:30", "UTC", time.Time{}),
		mustParse(t, "every", "7200", "UTC", time.Time{}),
		mustParse(t, "cron", "0 9 * * 1-5", "UTC", time.Time{}),
	} {
		back := mustParse(t, string(r.Kind), r.Stored(), "UTC", time.Time{})
		if back.Stored() != r.Stored() {
			t.Errorf("%s: %q came back %q", r.Kind, r.Stored(), back.Stored())
		}
	}
}

func TestWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "1 minute", 5 * time.Minute: "5 minutes", time.Hour: "1 hour",
		90 * time.Minute: "90 minutes", 48 * time.Hour: "2 days",
	} {
		if got := words(d); got != want {
			t.Errorf("words(%s) = %q, want %q", d, got, want)
		}
	}
}
