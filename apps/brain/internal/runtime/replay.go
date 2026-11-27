package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/memory"
)

// A replay runs a past run again on a harness that is not yet the agent's,
// to see what a proposed change would have done to it. It acts on nothing:
// a call the past run made, with the same arguments, gets what it got then;
// a call it never made runs only when it only reads; anything else is not
// made. It writes no run, event, message or reply, and
// asks no person anything: what came of it is returned, for the caller to
// keep.

// Recorded is a call a past run made, and what it returned.
type Recorded struct {
	Name, Arguments, Output, Error string
}

// RecordedOf is what a run's calls returned, from its events oldest first:
// each call with the result its id names. A call with no result is left
// out, as there is nothing to serve back. Read here, beside where the
// events are written, so the two cannot drift.
func RecordedOf(events []db.RunEvent) ([]Recorded, error) {
	results := map[string]toolResultPayload{}
	var calls []toolCallPayload
	for _, e := range events {
		switch e.Kind {
		case "tool_call":
			var c toolCallPayload
			if err := json.Unmarshal(e.Payload, &c); err != nil {
				return nil, fmt.Errorf("reading call event %d: %w", e.Seq, err)
			}
			calls = append(calls, c)
		case "tool_result":
			var r toolResultPayload
			if err := json.Unmarshal(e.Payload, &r); err != nil {
				return nil, fmt.Errorf("reading result event %d: %w", e.Seq, err)
			}
			results[r.ID] = r
		}
	}
	out := make([]Recorded, 0, len(calls))
	for _, c := range calls {
		if r, ok := results[c.ID]; ok {
			out = append(out, Recorded{Name: c.Name, Arguments: c.Arguments, Output: r.Output, Error: r.Error})
		}
	}
	return out, nil
}

// How a replay served a call.
const (
	ServedRecorded = "recorded"
	ServedLive     = "live"
	ServedNotMade  = "not_made"
)

// Served is one call of a replay, as it was served, its text clipped as a
// run's events are.
type Served struct {
	Name      string `json:"name"`
	How       string `json:"how"`
	Arguments string `json:"arguments"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

// notMade is what the model reads of a call a replay did not make.
const notMade = "Not made: this is a replay of a past run, and this call was not in it. " +
	"Carry on as if it had succeeded only if you must, and say what you would have done."

// Replayed is what came of a replay.
type Replayed struct {
	Output  string
	Usage   agent.Usage
	Steps   int
	Tools   []string
	Calls   []Served
	Elapsed time.Duration
	// Over is why the replay was stopped short by its limits; nil when it
	// was not.
	Over error
}

// ReplayLimits bound one replay, above the harness's own.
type ReplayLimits struct {
	Tokens int
	Time   time.Duration
}

var (
	ErrReplayTokens = errors.New("the try spent the tokens it may")
	ErrReplayTime   = errors.New("the try ran out of time")
)

// Replayer runs replays with what a run is built with, less what acts on
// the world: no approvals, workspaces, browsers or delegates, whose tools a
// replay holds as stand-ins.
type Replayer struct {
	mcp      MCPClients
	rules    ReplayRules
	graph    *GraphDeps
	search   *SearchDeps
	fetch    *FetchDeps
	runner   *agent.Runner
	endpoint agent.Endpoint
	now      func() time.Time
	// scheduling names the schedule and watch tools a run held, for their
	// stand-ins.
	scheduling Scheduling
}

// WithScheduling has a replay hold the schedule and watch tools, as
// stand-ins, where the run would.
func (p *Replayer) WithScheduling(s Scheduling) *Replayer {
	p.scheduling = s
	return p
}

// NewReplayer replays with rules telling who each past run acted for and
// what the team's approvals make of a read.
func NewReplayer(m MCPClients, rules ReplayRules, g *GraphDeps, search *SearchDeps, fetch *FetchDeps,
	r *agent.Runner, endpoint agent.Endpoint,
) *Replayer {
	return &Replayer{mcp: m, rules: rules, graph: g, search: search, fetch: fetch, runner: r, endpoint: endpoint, now: time.Now}
}

// ReplayInput is one past run to replay, and the harness to replay it on.
type ReplayInput struct {
	// Store answers the agent's reads with the harness being tried, and
	// Objects reads the files of its skills as tried.
	Store   BuildStore
	Objects Objects
	Run     db.Run
	// Messages are the run's transcript up to and including the message it
	// was started with.
	Messages []agent.Message
	// Recorded is what the run's calls returned.
	Recorded []Recorded
	Limits   ReplayLimits
}

// Replay runs a past run again on the harness its input names.
func (p *Replayer) Replay(ctx context.Context, in ReplayInput) (Replayed, error) {
	run, msgs, lim := in.Run, in.Messages, in.Limits
	b := &Builder{
		store: in.Store, objects: in.Objects, rules: p.rules,
		mcp: p.mcp, graph: p.graph, search: p.search, fetch: p.fetch,
		connectTimeout: connectTimeout, replay: true, scheduling: p.scheduling,
	}
	// Built as the past run was, under an id of its own that names no row.
	run.ID = uuid.New()
	// The transcript is kept in memory: nothing of it is saved.
	session, err := agent.Open("replay:"+run.ID.String(), memory.New())
	if err != nil {
		return Replayed{}, err
	}
	built, err := b.Build(ctx, run, session, Lineage{})
	if err != nil {
		return Replayed{}, err
	}
	defer built.Close()
	built.Spec = forChannel(run, built.Spec)

	out := Replayed{Tools: heldNames(built.Spec)}
	// reads is by name: two tools of one name could make a write a read.
	if dup := duplicate(out.Tools); dup != "" {
		return Replayed{}, fmt.Errorf("the run holds two tools named %s, so a replay cannot tell them apart", dup)
	}
	r := newReplaying(in.Recorded, built.reads)
	built.Spec.Tools = r.wrapAll(built.Spec.Tools)
	if built.Spec.Discovery != nil {
		built.Spec.Discovery.Tools = r.wrapAll(built.Spec.Discovery.Tools)
	}

	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if lim.Time > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(runCtx, lim.Time, ErrReplayTime)
		defer cancel()
	}
	// The token cap is checked as each model reply's usage arrives, beside
	// the run: the reply that crosses it is spent, and the try is stopped.
	// A try may so spend one reply past the cap, and a read that reply asked
	// for may still be made; a call that is not a read never is.
	events := make(chan agent.Event, 16)
	watched := make(chan struct{})
	var spent int
	// used is what the try spent, as its usage events say: a run stopped
	// short may report none of it.
	var used agent.Usage
	go func() {
		defer close(watched)
		for e := range events {
			if u, ok := e.(agent.UsageEvent); ok {
				used.Add(u.Usage)
				spent += u.Usage.InputTokens + u.Usage.OutputTokens
				if lim.Tokens > 0 && spent >= lim.Tokens {
					stop(ErrReplayTokens)
				}
			}
		}
	}()
	start := p.now()
	res, err := func() (agent.Result, error) {
		defer func() {
			close(events)
			<-watched
		}()
		return p.runner.Run(withSession(runCtx, run.SessionID), built.Spec, msgs, p.endpoint, events)
	}()
	out.Elapsed = p.now().Sub(start)
	out.Output, out.Usage, out.Steps, out.Calls = res.Output, res.Usage, res.Steps, r.served()
	if used.InputTokens+used.OutputTokens > out.Usage.InputTokens+out.Usage.OutputTokens {
		out.Usage = used
	}
	if err != nil {
		if cause := context.Cause(runCtx); errors.Is(cause, ErrReplayTokens) || errors.Is(cause, ErrReplayTime) {
			out.Over = cause
			return out, nil
		}
		return out, err
	}
	return out, nil
}

func duplicate(names []string) string {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			return n
		}
		seen[n] = true
	}
	return ""
}

func heldNames(s agent.Spec) []string {
	names := make([]string, 0, len(s.Tools))
	for _, t := range s.Tools {
		names = append(names, t.Name)
	}
	if s.Discovery != nil {
		for _, t := range s.Discovery.Tools {
			names = append(names, t.Name)
		}
	}
	return names
}

// replaying serves one replay's calls.
type replaying struct {
	reads map[string]bool
	mu    sync.Mutex
	// queue is the past run's calls, oldest first, by tool and arguments.
	queue map[string][]Recorded
	calls []Served
}

func newReplaying(recorded []Recorded, reads map[string]bool) *replaying {
	r := &replaying{reads: reads, queue: map[string][]Recorded{}}
	for _, c := range recorded {
		k := callKey(c.Name, c.Arguments)
		r.queue[k] = append(r.queue[k], c)
	}
	return r
}

func callKey(name, args string) string { return name + "\x00" + canonical(args) }

// canonical is a call's arguments as one text however they were written:
// object keys in order, no space between, numbers as they were sent. Text
// that is not JSON is kept as it is, trimmed.
func canonical(args string) string {
	d := json.NewDecoder(strings.NewReader(args))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil || d.More() {
		return strings.TrimSpace(args)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(args)
	}
	return string(b)
}

func (r *replaying) take(name, args string) (Recorded, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := callKey(name, args)
	q := r.queue[k]
	if len(q) == 0 {
		return Recorded{}, false
	}
	r.queue[k] = q[1:]
	return q[0], true
}

func (r *replaying) note(s Served) {
	s.Arguments, _ = clip(s.Arguments)
	s.Output, _ = clip(s.Output)
	s.Error, _ = clip(s.Error)
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *replaying) served() []Served {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Served{}, r.calls...)
}

func (r *replaying) wrapAll(ts []agent.Tool) []agent.Tool {
	out := make([]agent.Tool, len(ts))
	for i, t := range ts {
		out[i] = r.wrap(t)
	}
	return out
}

// wrap serves a tool's calls in a replay. A tool that is not a read cannot
// be made at all: its own call is not kept, so no mistake in the matching
// can reach what it would act on.
func (r *replaying) wrap(t agent.Tool) agent.Tool {
	read := r.reads[t.Name]
	var live func(ctx context.Context, args json.RawMessage) (agent.ToolResult, bool, error)
	if read {
		live = t.Call
	}
	t.Invoke = nil
	t.Result = func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
		if c, ok := r.take(t.Name, string(args)); ok {
			r.note(Served{Name: t.Name, How: ServedRecorded, Arguments: string(args), Output: c.Output, Error: c.Error})
			if c.Error != "" {
				return agent.ToolResult{}, toModel(c.Error)
			}
			return agent.ToolResult{Output: c.Output}, nil
		}
		if live == nil {
			r.note(Served{Name: t.Name, How: ServedNotMade, Arguments: string(args), Error: notMade})
			return agent.ToolResult{}, toModel(notMade)
		}
		// A try stopped at its limits makes no new read.
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		res, ok, err := live(ctx, args)
		if !ok {
			err = fmt.Errorf("%s cannot be called", t.Name)
		}
		s := Served{Name: t.Name, How: ServedLive, Arguments: string(args), Output: res.Output}
		if err != nil {
			s.Error = err.Error()
		}
		r.note(s)
		return res, err
	}
	return t
}

// browserStandIns are the browser's tools as a run would hold them, for a
// replay: none of them can be made.
func browserStandIns() []agent.Tool {
	out := make([]agent.Tool, len(browserTools))
	for i, t := range browserTools {
		out[i] = refusing(agent.Tool{Name: t.name, Description: t.description, Schema: t.schema}, notMade)
	}
	return out
}
