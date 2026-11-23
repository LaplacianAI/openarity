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
)

// A replay runs a past run again on a harness that is not yet the agent's,
// to see what a proposed change would have done to it. It acts on nothing:
// a call the past run made, with the same arguments, gets what it got then;
// a call it never made runs only when it reads what has not moved on since;
// anything else is not made. It writes no run, event, message or reply, and
// asks no person anything: what came of it is returned, for the caller to
// keep.

// Recorded is a call a past run made, and what it returned.
type Recorded struct {
	Name, Arguments, Output, Error string
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
	objects  Objects
	mcp      MCPClients
	graph    *GraphDeps
	search   *SearchDeps
	fetch    *FetchDeps
	runner   *agent.Runner
	endpoint agent.Endpoint
	now      func() time.Time
}

func NewReplayer(o Objects, m MCPClients, g *GraphDeps, search *SearchDeps, fetch *FetchDeps,
	r *agent.Runner, endpoint agent.Endpoint,
) *Replayer {
	return &Replayer{objects: o, mcp: m, graph: g, search: search, fetch: fetch, runner: r, endpoint: endpoint, now: time.Now}
}

// ReplayInput is one past run to replay, and the harness to replay it on.
type ReplayInput struct {
	// Store answers the agent's reads with the harness being tried.
	Store BuildStore
	// Texts are the files of a skill being tried that are not stored yet,
	// by the object key Store names them with.
	Texts map[string]string
	Run   db.Run
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
		store: in.Store, objects: textObjects{texts: in.Texts, inner: p.objects},
		mcp: p.mcp, graph: p.graph, search: p.search, fetch: p.fetch,
		connectTimeout: connectTimeout, replay: true,
	}
	// Built as the past run was, under an id of its own that names no row.
	run.ID = uuid.New()
	session, err := agent.Open("replay:"+run.ID.String(), &memoryStore{msgs: map[string][]agent.Message{}})
	if err != nil {
		return Replayed{}, err
	}
	built, err := b.Build(ctx, run, session, Lineage{})
	if err != nil {
		return Replayed{}, err
	}
	defer built.Close()
	built.Spec = forChannel(run, built.Spec)

	r := newReplaying(in.Recorded, built.reads)
	built.Spec.Tools = r.wrapAll(built.Spec.Tools)
	if built.Spec.Discovery != nil {
		built.Spec.Discovery.Tools = r.wrapAll(built.Spec.Discovery.Tools)
	}
	out := Replayed{Tools: heldNames(built.Spec)}

	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if lim.Time > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(runCtx, lim.Time, ErrReplayTime)
		defer cancel()
	}
	events := make(chan agent.Event, 16)
	watched := make(chan struct{})
	var spent int
	go func() {
		defer close(watched)
		for e := range events {
			if u, ok := e.(agent.UsageEvent); ok {
				spent += u.Usage.InputTokens + u.Usage.OutputTokens
				if lim.Tokens > 0 && spent >= lim.Tokens {
					stop(ErrReplayTokens)
				}
			}
		}
	}()
	start := p.now()
	res, err := p.runner.Run(withSession(runCtx, run.SessionID), built.Spec, msgs, p.endpoint, events)
	close(events)
	<-watched
	out.Elapsed = p.now().Sub(start)
	out.Output, out.Usage, out.Steps, out.Calls = res.Output, res.Usage, res.Steps, r.served()
	if err != nil {
		if cause := context.Cause(runCtx); errors.Is(cause, ErrReplayTokens) || errors.Is(cause, ErrReplayTime) {
			out.Over = cause
			return out, nil
		}
		return out, err
	}
	return out, nil
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

func callKey(name, args string) string { return name + "\x00" + Canonical(args) }

// Canonical is a call's arguments as one text however they were written:
// object keys in order, no space between, numbers as they were sent. Text
// that is not JSON is kept as it is, trimmed.
func Canonical(args string) string {
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
	return append([]Served(nil), r.calls...)
}

func (r *replaying) wrapAll(ts []agent.Tool) []agent.Tool {
	out := make([]agent.Tool, len(ts))
	for i, t := range ts {
		out[i] = r.wrap(t)
	}
	return out
}

// wrap serves a tool's calls in a replay. A tool that is not a read cannot
// be made at all: its own calls are replaced before anything else, so no
// mistake in the matching can reach what it would act on.
func (r *replaying) wrap(t agent.Tool) agent.Tool {
	read := r.reads[t.Name]
	live := t
	if !read {
		live = refusing(t, notMade)
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
		if !read {
			r.note(Served{Name: t.Name, How: ServedNotMade, Arguments: string(args), Error: notMade})
			return agent.ToolResult{}, toModel(notMade)
		}
		res, ok, err := live.Call(ctx, args)
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

// textObjects reads a candidate skill's files that are not stored yet from
// their text, and every other object from the store.
type textObjects struct {
	texts map[string]string
	inner Objects
}

func (o textObjects) Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error) {
	if t, ok := o.texts[key]; ok {
		return []byte(t), nil
	}
	return o.inner.Get(ctx, teamID, key)
}

// memoryStore keeps a replay's transcript in memory: nothing of it is saved.
type memoryStore struct {
	mu   sync.Mutex
	msgs map[string][]agent.Message
}

func (m *memoryStore) Save(_ context.Context, session string, msgs []agent.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs[session] = append([]agent.Message(nil), msgs...)
	return nil
}

func (m *memoryStore) Messages(_ context.Context, session string) ([]agent.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agent.Message(nil), m.msgs[session]...), nil
}

func (m *memoryStore) AddSteer(context.Context, string, string) error { return nil }

func (m *memoryStore) TakeSteers(context.Context, string) ([]string, error) { return nil, nil }
