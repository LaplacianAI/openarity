// Command compaction runs a long review that would outgrow its budget, and
// shows the three things that keep it going: a large result previewed and
// read back in parts, older turns folded into a summary, and a wrap-up that
// gets an answer before the budget runs out.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// wrapAt is the spend past which the caller asks the run to finish. The brain
// does the same from the run's token budget; here it is a plain number.
const wrapAt = 60_000

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	diff := bigDiff()

	// The stub's turns, in order. A compaction's summary call is a model call
	// like any other, so the script answers it too; against a real gateway the
	// model writes it.
	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("get_diff", `{"pr":49}`),
		gateway.ToolCall(agent.ReadOutputToolName, `{"call":"call_1_1","match":"func checkDelegation"}`),
		gateway.ToolCall("read_file", `{"path":"apps/brain/internal/runtime/delegate.go"}`),
		gateway.ToolCall("read_file", `{"path":"apps/brain/internal/runtime/report.go"}`),
		gateway.ToolCall("read_file", `{"path":"apps/brain/internal/runtime/execute.go"}`),
		gateway.Answer("Goal: review pull request #49.\nFindings: checkDelegation counts the tree's tokens "+
			"before the depth.\nDone: delegate.go, report.go, execute.go.\nRemaining: events.go.\n"+
			"Next step: read events.go."),
		gateway.ToolCall("read_file", `{"path":"apps/brain/internal/runtime/events.go"}`),
		gateway.ToolCall("read_file", `{"path":"apps/brain/internal/runtime/build.go"}`),
		gateway.Answer("Two findings: checkDelegation reads the tree's spend before the depth, and "+
			"report.go cuts a report at 32 KiB without saying where the rest is."),
	)
	defer shutdown()

	seenBy := &recorder{}
	runner, err := agent.New(seenBy.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You review pull requests. Read what you need, then report your findings."),
		Tools:    []agent.Tool{getDiff(diff), readFile()},
		MaxSteps: 12,
		Compaction: &agent.Compaction{
			AfterMessages:    10,
			AfterInputTokens: 30_000,
			KeepTokens:       4_000,
		},
	}
	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Review pull request #49."}},
	}}

	events := make(chan agent.Event, 64)
	handle := make(chan *agent.Run, 1)
	done := make(chan report)
	go func() { done <- watch(events, handle) }()

	run := runner.Start(ctx, spec, msgs, endpoint, events)
	handle <- run
	result, err := run.Wait()
	close(events)
	seen := <-done
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println()
	fmt.Printf("input per call   %s\n", strings.Join(seen.inputs, " → "))
	fmt.Printf("the large result %s in the transcript; the model was shown %s of it,\n"+
		"                 and read back only the line it needed\n",
		size(len(diff)), size(seenBy.largest("call_1_1")))
	for _, c := range seen.compactions {
		fmt.Printf("compacted        %s\n", c)
	}
	if seen.wrapped != "" {
		fmt.Printf("wrap-up          %s\n", seen.wrapped)
	}
	fmt.Printf("refused          %d tool call(s) after the wrap-up\n", seen.refused)
	fmt.Printf("answer           %s\n", result.Output)

	// As if the worker died before its last call: the transcript as it was
	// saved then, its summary in it, handed to a new run.
	saved := result.Messages[:len(result.Messages)-1]
	for i, m := range saved {
		if m.Compaction != nil {
			fmt.Printf("in the transcript a summary, at message %d of %d\n", i, len(saved))
		}
	}
	calls, summaries, carried, err := seenBy.resumed(ctx, runner, spec, saved, endpoint)
	if err != nil {
		return err
	}
	fmt.Printf("resumed from it  %d model call(s), %d of them a summary; its first request opened\n"+
		"                 with the task and the summary it carried: %v\n", calls, summaries, carried)
	return nil
}

// resumed runs spec again from msgs: the calls it made, and whether the first
// one opened with the task and a summary.
func (r *recorder) resumed(ctx context.Context, runner *agent.Runner, spec agent.Spec,
	msgs []agent.Message, endpoint agent.Endpoint,
) (calls, summaries int, carried bool, err error) {
	r.mu.Lock()
	from := len(r.sent)
	r.mu.Unlock()
	if _, err := runner.Run(ctx, spec, msgs, endpoint, nil); err != nil {
		return 0, 0, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if first := r.sent[from].Messages; len(first) > 1 {
		carried = strings.HasPrefix(first[1].Text(), "This run was compacted.")
	}
	for _, req := range r.sent[from:] {
		calls++
		if last := req.Messages[len(req.Messages)-1]; strings.HasPrefix(last.Text(), "Pause the task.") {
			summaries++
		}
	}
	return calls, summaries, carried, nil
}

type report struct {
	inputs      []string
	compactions []string
	wrapped     string
	refused     int
}

// watch prints the run as it goes and asks for the wrap-up once the run has
// spent wrapAt, as the brain does from a run's budget.
func watch(events <-chan agent.Event, handle <-chan *agent.Run) report {
	var r report
	run := <-handle
	spent := 0
	for e := range events {
		switch ev := e.(type) {
		case agent.StepEvent:
			fmt.Printf("\n[step %d] ", ev.Step)
		case agent.TextEvent:
			fmt.Print(ev.Delta)
		case agent.ToolCallEvent:
			fmt.Printf("\n  → %s(%s)", ev.Name, ev.Arguments)
		case agent.ToolResultEvent:
			if ev.Err != "" {
				r.refused++
				fmt.Printf("\n  ← %s refused: %s", ev.Name, ev.Err)
				continue
			}
			fmt.Printf("\n  ← %s: %s", ev.Name, size(len(ev.Output)))
		case agent.CompactedEvent:
			line := fmt.Sprintf("%s: %d message(s) folded, %d result(s) cleared, about %s → %s tokens",
				ev.Reason, ev.Folded, ev.Cleared, thousands(ev.Before), thousands(ev.After))
			r.compactions = append(r.compactions, line)
			fmt.Printf("\n  ↧ compacted · %s", line)
		case agent.UsageEvent:
			spent += ev.Usage.InputTokens + ev.Usage.OutputTokens
			if ev.Summary {
				fmt.Printf("\n  [summary: %d in, %d out]", ev.Usage.InputTokens, ev.Usage.OutputTokens)
				continue
			}
			r.inputs = append(r.inputs, thousands(ev.Usage.InputTokens))
			if spent > wrapAt && r.wrapped == "" {
				if err := run.WrapUp("Your token budget is nearly spent. Stop calling tools and " +
					"answer now with what you have."); err == nil {
					r.wrapped = fmt.Sprintf("asked at %s tokens spent", thousands(spent))
					fmt.Printf("\n  ⚠ wrapping up · %s", r.wrapped)
				}
			}
		}
	}
	return r
}

// recorder keeps the requests the model was actually sent, so the example
// prints what reached it rather than what it should have been.
type recorder struct {
	mu   sync.Mutex
	sent []agent.Request
}

func (r *recorder) wrap(f agent.ClientFactory) agent.ClientFactory {
	return func(e agent.Endpoint) (agent.ModelClient, error) {
		inner, err := f(e)
		if err != nil {
			return nil, err
		}
		return recording{inner: inner, r: r}, nil
	}
}

// largest is the most of one tool result any request carried.
func (r *recorder) largest(call string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	most := 0
	for _, req := range r.sent {
		for _, m := range req.Messages {
			if m.Role == agent.RoleTool && m.ToolCallID == call {
				most = max(most, len(m.Text()))
			}
		}
	}
	return most
}

type recording struct {
	inner agent.ModelClient
	r     *recorder
}

func (c recording) keep(req agent.Request) {
	c.r.mu.Lock()
	c.r.sent = append(c.r.sent, req)
	c.r.mu.Unlock()
}

func (c recording) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	c.keep(req)
	return c.inner.Complete(ctx, req)
}

func (c recording) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	c.keep(req)
	return c.inner.Stream(ctx, req)
}

func getDiff(diff string) agent.Tool {
	return agent.Tool{
		Name:        "get_diff",
		Description: "The full diff of a pull request.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"pr":{"type":"integer"}},"required":["pr"]}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			return diff, nil
		},
	}
}

func readFile() agent.Tool {
	return agent.Tool{
		Name:        "read_file",
		Description: "Read one file of the repository.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Invoke: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return strings.Repeat("// "+a.Path+": a line of Go source\n", 900), nil
		},
	}
}

// bigDiff is a diff of about 340 KiB with one line worth finding in the middle.
func bigDiff() string {
	var b strings.Builder
	for i := range 9000 {
		if i == 4500 {
			b.WriteString("+func checkDelegation(ctx context.Context, s DelegateStore) error {\n")
			continue
		}
		fmt.Fprintf(&b, "+\tline %d of a generated file\n", i)
	}
	return b.String()
}

func size(n int) string {
	if n >= 1<<10 {
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func thousands(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
