// Command sessions steers a run that nothing in the process is holding, and
// then resumes it in a runner that never produced any of it.
//
// Three runners share one SQLite file and nothing else — not a channel, not a
// mutex, not a *Run. That is the whole point: Run.Steer is a method on a
// handle, so it cannot serve a steer that arrives at a different replica, and
// it cannot serve a run whose process has gone.
//
//	go run ./examples/sessions
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/sqlite"
)

const conversation = "conversation-1"

func main() {
	// os.Exit skips deferred calls, so the signal handler is released in
	// attempt rather than in a defer here that would never run.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	// Ctrl-C matters here because a streaming run holds an open connection.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := os.MkdirTemp("", "openarity-sessions")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a temp directory on the way out

	path := filepath.Join(dir, "sessions.db")
	store, err := sqlite.Open(path)
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // same

	// Replica B. It holds no run, and there is no run yet to hold — which is
	// the case a method on a *Run cannot serve at all.
	elsewhere, err := agent.Open(conversation, store)
	if err != nil {
		return err
	}
	if err := elsewhere.Steer(ctx, "check the lease expiry, not the token"); err != nil {
		return err
	}
	// Replica A. It was never told about replica B.
	//
	// The run starts from a real question. A steer is a correction to work in
	// progress, so a run with nothing in progress gives the model only the
	// correction and no task — and it answers by asking what you are talking
	// about, which is fair.
	session, err := agent.Open(conversation, store)
	if err != nil {
		return err
	}
	asking := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "why does renewLease return a nil lease?"}},
	}}
	result, err := run(ctx, session, asking)
	if err != nil {
		return err
	}

	at := -1
	for i, m := range result.Messages {
		if strings.Contains(m.Text(), "check the lease expiry") {
			at = i
		}
	}
	// Replica C. A different runner, no session at all — it reads the
	// transcript and carries on. This is what a brain does when the next
	// message arrives in a thread somebody else was handling.
	//
	// The new message matters: the saved transcript ends with the assistant's
	// answer, and a provider refuses a conversation that does not end with
	// something to respond to — "this model does not support assistant message
	// prefill". Resuming a run that died mid-turn needs no such thing, because
	// that transcript ends with a tool result.
	kept, err := store.Messages(ctx, conversation)
	if err != nil {
		return err
	}
	asked := append(slices.Clone(kept), agent.Message{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "and which line returns the token error?"}},
	})
	resumed, err := run(ctx, nil, asked)
	if err != nil {
		return err
	}
	// The steer was handed over once. A store that read without removing would
	// give it to every turn, and the model would re-read it as a fresh
	// instruction and never conclude.
	left, err := session.TakeSteers(ctx)
	if err != nil {
		return err
	}

	// One block at the end: the run output above ends mid-line, and these are
	// the lines worth reading.
	fmt.Printf("\n\nreplica B   left a steer in %s, holding no run — there was none yet\n",
		filepath.Base(path))
	fmt.Printf("replica A   asked its own question, ran %d steps, and was never told\n"+
		"            replica B existed\n", result.Steps)
	fmt.Printf("transcript  %d messages, and the steer is message %d of them\n",
		len(result.Messages), at)
	fmt.Printf("replica C   took over %d messages it never produced, was asked one more\n"+
		"            thing, and answered %q\n", len(kept), summarise(resumed.Output))
	fmt.Printf("steers      %d left in the store: it was handed over exactly once\n", len(left))

	return nil
}

// run builds its own runner every time, so the three replicas share the file
// and nothing else. session is nil for the one that is only resuming.
func run(ctx context.Context, session agent.Session, msgs []agent.Message) (agent.Result, error) {
	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("read_file", `{"path":"vault.go"}`),
		gateway.Answer("renewLease returns a nil lease when the lease has expired, not the token."),
	)
	defer shutdown()

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return agent.Result{}, err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You read code and answer in one sentence."),
		MaxSteps: 4,
		Session:  session,
		Tools: []agent.Tool{{
			Name:        "read_file",
			Description: "Read a file from the repository.",
			Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},` +
				`"required":["path"]}`),
			Invoke: func(context.Context, json.RawMessage) (string, error) {
				return "vault.go:88  renewLease returns a nil lease when the lease is expired", nil
			},
		}},
	}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() { defer close(done); gateway.Report(events) }()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	return result, err
}

func summarise(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:57] + "…"
}
