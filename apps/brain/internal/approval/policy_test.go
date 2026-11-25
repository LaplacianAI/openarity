package approval

import (
	"fmt"
	"testing"
)

var person = Actor{CanSelf: true}

// The whole of the mode's table, read off the design, against every class.
func TestTheModeDecidesEachClassAsDesigned(t *testing.T) {
	t.Parallel()

	want := map[Mode]map[Class]Verdict{
		Off:             {Read: Allow, Write: Allow, Destructive: Allow},
		Writes:          {Read: Allow, Write: Ask, Destructive: Ask},
		DestructiveOnly: {Read: Allow, Write: Allow, Destructive: Ask},
		All:             {Read: Ask, Write: Ask, Destructive: Ask},
	}
	for mode, row := range want {
		for class, v := range row {
			got := Decide(Input{Tool: "github__create_issue", Server: "github", Class: class, Mode: mode, Actor: person})
			if got.Verdict != v || got.Why != ByMode {
				t.Errorf("%s/%s = %s by %s, want %s by mode", mode, class, got.Verdict, got.Why, v)
			}
		}
	}
}

// A setting this package does not know asks: a typo must not open the gate.
func TestAnUnknownModeOrRuleAsks(t *testing.T) {
	t.Parallel()

	if got := Decide(Input{Tool: "t", Class: Read, Mode: "wirtes", Actor: person}); got.Verdict != Ask {
		t.Errorf("an unknown mode on a read = %s, want ask", got.Verdict)
	}
	rules := []Rule{{Kind: "tool", Target: "t", Verdict: "always"}}
	if got := Decide(Input{Tool: "t", Class: Read, Mode: Writes, Rules: rules, Actor: person}); got.Verdict != Ask {
		t.Errorf("an unknown rule verdict = %s, want ask", got.Verdict)
	}
}

func TestAToolRuleBeatsAServerRuleWhichBeatsTheMode(t *testing.T) {
	t.Parallel()

	rules := []Rule{
		{Kind: "server", Target: "GitHub", Verdict: Deny},
		{Kind: "tool", Target: "github__GET_issue", Verdict: Allow},
		{Kind: "tool", Target: "slack__post", Verdict: Ask, RequireOther: true},
	}
	cases := []struct {
		tool, server string
		want         Verdict
		why          Why
		other        bool
	}{
		{"github__get_issue", "github", Allow, ByToolRule, false},
		{"github__delete_repo", "github", Deny, ByServerRule, false},
		{"slack__post", "slack", Ask, ByToolRule, true},
		{"slack__read", "slack", Allow, ByMode, false},
		// A built-in has no server, so a server rule that shares its name is
		// not its rule.
		{"github", "", Allow, ByMode, false},
	}
	for _, c := range cases {
		got := Decide(Input{Tool: c.tool, Server: c.server, Class: Read, Mode: Writes, Rules: rules, Actor: person})
		if got.Verdict != c.want || got.Why != c.why || got.RequireOther != c.other {
			t.Errorf("%s = %+v, want %s by %s, require other %v", c.tool, got, c.want, c.why, c.other)
		}
	}
}

// A guest cannot approve their own calls, so a call that would be made asks
// an approver instead, even under an allow rule. A read is still a read.
func TestAGuestIsAskedForWhatAnAllowWouldMake(t *testing.T) {
	t.Parallel()

	guest := Actor{}
	allow := []Rule{{Kind: "server", Target: "github", Verdict: Allow}}
	for _, c := range []struct {
		class Class
		rules []Rule
		mode  Mode
		want  Verdict
		why   Why
	}{
		{Write, allow, Writes, Ask, ByGuest},
		{Destructive, allow, All, Ask, ByGuest},
		{Write, nil, DestructiveOnly, Ask, ByGuest},
		{Read, allow, All, Allow, ByServerRule},
		{Read, nil, Writes, Allow, ByMode},
		{Read, nil, All, Ask, ByMode},
	} {
		got := Decide(Input{Tool: "github__x", Server: "github", Class: c.class, Mode: c.mode, Rules: c.rules, Actor: guest})
		if got.Verdict != c.want || got.Why != c.why {
			t.Errorf("guest %s under %s %v = %s by %s, want %s by %s", c.class, c.mode, c.rules, got.Verdict, got.Why, c.want, c.why)
		}
	}
}

// Nobody can answer a run nobody is present for, so what would ask is
// denied. An explicit allow still allows, for a person's run; a guest's
// unattended write is asked by the guest floor and then denied.
func TestAnUnattendedRunIsDeniedWhatItWouldAsk(t *testing.T) {
	t.Parallel()

	schedule := Actor{CanSelf: true, Unattended: true}
	allow := []Rule{{Kind: "tool", Target: "mail__send", Verdict: Allow}}

	if got := Decide(Input{Tool: "mail__send", Server: "mail", Class: Destructive, Mode: Writes, Actor: schedule}); got.Verdict != Deny || got.Why != ByUnattended {
		t.Errorf("unattended ask = %s by %s, want deny by unattended", got.Verdict, got.Why)
	}
	if got := Decide(Input{Tool: "mail__send", Server: "mail", Class: Destructive, Mode: Writes, Rules: allow, Actor: schedule}); got.Verdict != Allow {
		t.Errorf("unattended under an allow rule = %s, want allow", got.Verdict)
	}
	guestSchedule := Actor{Unattended: true}
	if got := Decide(Input{Tool: "mail__send", Server: "mail", Class: Write, Mode: Writes, Rules: allow, Actor: guestSchedule}); got.Verdict != Deny || got.Why != ByUnattended {
		t.Errorf("a guest's unattended write under allow = %s by %s, want deny by unattended", got.Verdict, got.Why)
	}
	if got := Decide(Input{Tool: "graph", Class: Read, Mode: Writes, Actor: schedule}); got.Verdict != Allow {
		t.Errorf("an unattended read = %s, want allow", got.Verdict)
	}
}

// A deny is final: no floor and no actor turns it into anything else.
func TestADenyIsFinal(t *testing.T) {
	t.Parallel()

	deny := []Rule{{Kind: "tool", Target: "t", Verdict: Deny}}
	for _, a := range []Actor{{}, person, {Unattended: true}, {CanSelf: true, Unattended: true}} {
		for _, c := range []Class{Read, Write, Destructive} {
			if got := Decide(Input{Tool: "t", Class: c, Mode: Writes, Rules: deny, Actor: a}); got.Verdict != Deny {
				t.Errorf("deny rule for %+v on %s = %s", a, c, got.Verdict)
			}
		}
	}
}

// Off is the gate switched off: no rule and no floor applies, so a team that
// never turned approvals on sees every call made, guests' included.
func TestOffMakesEveryCall(t *testing.T) {
	t.Parallel()

	deny := []Rule{{Kind: "server", Target: "mail", Verdict: Deny}}
	for _, a := range []Actor{{}, {Unattended: true}} {
		got := Decide(Input{Tool: "mail__send", Server: "mail", Class: Destructive, Mode: Off, Rules: deny, Actor: a})
		if got.Verdict != Allow {
			t.Errorf("off for %+v = %s, want allow", a, got.Verdict)
		}
	}
}

func TestAServersHintsAreBelievedOnlyWhenItIsTrusted(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		hints   string
		trusted bool
		want    Class
	}{
		{`{"readOnlyHint":true}`, false, Destructive},
		{`{"readOnlyHint":true}`, true, Read},
		{`{"readOnlyHint":true,"destructiveHint":true}`, true, Read},
		{`{"destructiveHint":false}`, true, Write},
		{`{"readOnlyHint":false,"destructiveHint":false}`, true, Write},
		{`{"readOnlyHint":false}`, true, Destructive},
		{`{"destructiveHint":true}`, true, Destructive},
		{`{}`, true, Destructive},
		{``, true, Destructive},
		{`not json`, true, Destructive},
		{`{"readOnlyHint":"yes"}`, true, Destructive},
		{`[true]`, true, Destructive},
	} {
		if got := ClassOf([]byte(c.hints), c.trusted); got != c.want {
			t.Errorf("ClassOf(%s, trusted %v) = %s, want %s", c.hints, c.trusted, got, c.want)
		}
	}
}

func TestBuiltinsAreJudgedByWhoIsThere(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		b    Builtin
		a    Actor
		want Class
	}{
		{Looks, Actor{}, Read},
		{Looks, Actor{Unattended: true}, Read},
		{Egress, person, Read},
		{Egress, Actor{}, Write},
		{Egress, Actor{CanSelf: true, Unattended: true}, Write},
		// Acting on a page can submit or buy, for a person who is there too.
		{Acts, person, Write},
		{Acts, Actor{}, Write},
		{"teleport", person, Destructive},
	} {
		if got := ClassFor(c.b, c.a); got != c.want {
			t.Errorf("ClassFor(%s, %+v) = %s, want %s", c.b, c.a, got, c.want)
		}
	}
}

func ExampleDecide() {
	d := Decide(Input{
		Tool: "mail__send", Server: "mail", Class: ClassOf([]byte(`{"destructiveHint":true}`), true),
		Mode: Writes, Actor: Actor{CanSelf: true},
	})
	fmt.Println(d.Verdict, d.Why)
	// Output: ask mode
}

// Handing the person a file is judged as reaching out is: a read only for a
// person who is there and may approve their own calls.
func TestSendingAFileIsAReadOnlyForAPersonWhoIsThere(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		a    Actor
		want Class
	}{
		{"a person present", person, Read},
		{"a guest", Actor{}, Write},
		{"nobody present", Actor{CanSelf: true, Unattended: true}, Write},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassFor(Shares, c.a); got != c.want {
				t.Errorf("ClassFor(%s, %+v) = %s, want %s", Shares, c.a, got, c.want)
			}
		})
	}
}

// What each mode does with send_file, by who the run is for: what the docs
// say of it, pinned.
func TestWhatEachModeDoesWithAFileSent(t *testing.T) {
	t.Parallel()

	nobody := Actor{CanSelf: true, Unattended: true}
	for _, c := range []struct {
		mode  Mode
		actor Actor
		rules []Rule
		want  Verdict
	}{
		{Off, Actor{}, []Rule{{Kind: "tool", Target: "send_file", Verdict: Deny}}, Allow},
		{Writes, person, nil, Allow},
		{Writes, Actor{}, nil, Ask},
		{Writes, nobody, nil, Deny},
		{All, person, nil, Ask},
		{All, nobody, nil, Deny},
		{DestructiveOnly, person, nil, Allow},
		{DestructiveOnly, Actor{}, nil, Ask},
		// A rule decides for a person; a guest's write still asks however
		// a rule allows it, and nobody present is still refused.
		{Writes, person, []Rule{{Kind: "tool", Target: "SEND_FILE", Verdict: Deny}}, Deny},
		{Writes, person, []Rule{{Kind: "tool", Target: "send_file", Verdict: Ask}}, Ask},
		{Writes, Actor{}, []Rule{{Kind: "tool", Target: "send_file", Verdict: Allow}}, Ask},
		{Writes, nobody, []Rule{{Kind: "tool", Target: "send_file", Verdict: Allow}}, Allow},
	} {
		got := Decide(Input{Tool: "send_file", Class: ClassFor(Shares, c.actor), Mode: c.mode, Rules: c.rules, Actor: c.actor})
		if got.Verdict != c.want {
			t.Errorf("%s, %+v, rules %+v: %s, want %s", c.mode, c.actor, c.rules, got.Verdict, c.want)
		}
	}
}
