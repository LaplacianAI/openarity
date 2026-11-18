// Package approval decides what is done about a tool call before it is made:
// allowed, asked of a person, or denied. It is pure: the runtime gathers what
// it needs (the call's class, the team's mode and rules, who the run is for)
// and gets back a verdict, and nothing here reads a database or knows a tool
// by name.
package approval

import (
	"encoding/json"
	"strings"
)

// A Class is what a call can do. It is the policy's only view of a tool.
type Class string

const (
	// Read changes nothing.
	Read Class = "read"
	// Write changes something, not destructively.
	Write Class = "write"
	// Destructive may destroy or send, and is what a call is when nothing is
	// known about it.
	Destructive Class = "destructive"
)

// ClassOf is an MCP tool's class, from the hints its server gave at
// discovery. A server's hints are believed only when an admin trusts it: a
// server that lies about readOnlyHint is exactly the one to fear, so an
// untrusted server's every tool is destructive, whatever it says.
//
// go-sdk decodes an omitted readOnlyHint as false and leaves an omitted
// destructiveHint out, so missing hints read as "may write, may destroy".
func ClassOf(hints []byte, trusted bool) Class {
	if !trusted || len(hints) == 0 {
		return Destructive
	}
	var h struct {
		ReadOnly    *bool `json:"readOnlyHint"`
		Destructive *bool `json:"destructiveHint"`
	}
	if err := json.Unmarshal(hints, &h); err != nil {
		return Destructive
	}
	switch {
	case h.ReadOnly != nil && *h.ReadOnly:
		return Read
	case h.Destructive != nil && !*h.Destructive:
		return Write
	}
	return Destructive
}

// A Builtin is how the runtime describes one of the brain's own tools. The
// runtime owns their names; this package owns what they mean.
type Builtin string

const (
	// Looks reads what the team or the run already holds: the graph, the
	// conversation, its files, an earlier output.
	Looks Builtin = "looks"
	// Egress reaches the internet, where a query or a URL can carry data out.
	Egress Builtin = "egress"
)

// An Actor is who a run's calls are made for.
type Actor struct {
	// CanSelf is whether the run's person holds approval:self. A guest does
	// not.
	CanSelf bool
	// Unattended is a run nobody is waiting on: a schedule's, a watcher's.
	Unattended bool
}

// ClassFor is a built-in tool's class. Reaching the internet is a read for a
// person, and a write for a guest or a run with nobody present: a search
// query or a fetched URL is a way to carry data out, and they are who it
// would be carried out for unseen.
func ClassFor(b Builtin, a Actor) Class {
	switch b {
	case Looks:
		return Read
	case Egress:
		if a.CanSelf && !a.Unattended {
			return Read
		}
		return Write
	default:
		return Destructive
	}
}

// A Mode is a team's standing answer, before any rule.
type Mode string

const (
	// Off turns the gate off: every call is made, as before approvals.
	Off Mode = "off"
	// Writes asks for every call that is not a read.
	Writes Mode = "writes"
	// DestructiveOnly asks only for destructive calls.
	DestructiveOnly Mode = "destructive"
	// All asks for every call.
	All Mode = "all"
)

// A Verdict is what is done about one call.
type Verdict string

const (
	Allow Verdict = "allow"
	Ask   Verdict = "ask"
	Deny  Verdict = "deny"
)

// A Rule is a team's answer for one tool or for every tool of one server.
type Rule struct {
	// Kind is "tool" or "server".
	Kind string
	// Target is the server's name, or a tool as <server>__<tool> (its name on
	// the server, whether or not the server is bare), or a built-in's own
	// name. Matched without regard to case, as the table's unique key is.
	Target       string
	Verdict      Verdict
	RequireOther bool
}

// Input is everything one decision rests on.
type Input struct {
	// Tool is the call's rule target: <server>__<tool>, or a built-in's name.
	Tool string
	// Server is the MCP server's name; empty for a built-in.
	Server string
	Class  Class
	Mode   Mode
	Rules  []Rule
	Actor  Actor
}

// A Decision is a verdict and what goes with it.
type Decision struct {
	Verdict Verdict
	// RequireOther is a rule's demand that the run's own person may not
	// approve this call; enforced when the request is decided.
	RequireOther bool
	// Why says what decided it, for the trace and, on a denial, the model.
	Why Why
}

// Why is what a decision came from.
type Why string

const (
	ByToolRule   Why = "tool_rule"
	ByServerRule Why = "server_rule"
	ByMode       Why = "mode"
	// ByGuest is a call that would have been made, asked of an approver
	// because its person cannot approve their own.
	ByGuest Why = "guest"
	// ByUnattended is a call that would have been asked, denied because
	// nobody is there to answer.
	ByUnattended Why = "unattended"
)

// Decide is the policy: the most specific rule, else the team's mode against
// the call's class; then two floors a rule cannot loosen. A guest's calls
// that are not reads ask, and a call that asks in a run nobody is present for
// is denied. The floors only ever tighten, allow to ask and ask to deny, so a
// deny is final.
//
// Off is the gate switched off, floors and rules included: a team that has
// not turned approvals on sees every call made as before.
func Decide(in Input) Decision {
	if in.Mode == Off {
		return Decision{Verdict: Allow, Why: ByMode}
	}

	d := governing(in)
	if d.Verdict == Allow && in.Class != Read && !in.Actor.CanSelf {
		d.Verdict, d.Why = Ask, ByGuest
	}
	if d.Verdict == Ask && in.Actor.Unattended {
		d.Verdict, d.Why = Deny, ByUnattended
	}
	return d
}

func governing(in Input) Decision {
	if r, ok := find(in.Rules, "tool", in.Tool); ok {
		return Decision{Verdict: verdictOf(r.Verdict), RequireOther: r.RequireOther, Why: ByToolRule}
	}
	if in.Server != "" {
		if r, ok := find(in.Rules, "server", in.Server); ok {
			return Decision{Verdict: verdictOf(r.Verdict), RequireOther: r.RequireOther, Why: ByServerRule}
		}
	}
	return Decision{Verdict: byMode(in.Mode, in.Class), Why: ByMode}
}

func find(rules []Rule, kind, target string) (Rule, bool) {
	if target == "" {
		return Rule{}, false
	}
	for _, r := range rules {
		if r.Kind == kind && strings.EqualFold(r.Target, target) {
			return r, true
		}
	}
	return Rule{}, false
}

// verdictOf reads a stored rule's verdict; one it does not know asks, the
// cautious way round.
func verdictOf(v Verdict) Verdict {
	switch v {
	case Allow, Ask, Deny:
		return v
	default:
		return Ask
	}
}

func byMode(m Mode, c Class) Verdict {
	switch m {
	case Off:
		return Allow
	case Writes:
		if c == Read {
			return Allow
		}
		return Ask
	case DestructiveOnly:
		if c == Read || c == Write {
			return Allow
		}
		return Ask
	case All:
		return Ask
	default:
		// A mode this package does not know asks for everything: a typo in
		// the settings must not turn the gate off.
		return Ask
	}
}
