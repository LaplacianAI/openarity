// Command skills runs an agent that is offered two skills and opens one.
//
// It exists to make progressive disclosure visible. Both skills cost their
// description in the prompt whether they are used or not; only the one the
// model asks for costs its body, and the counter at the end proves it — the
// other body is never read from disk at all.
//
//	go run ./examples/skills
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/loops"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The stub asks for the same skill twice on purpose. A real model rarely
	// does, but a looping one does, and the second call must not pay for the
	// body again — it is already in the messages the loop is accumulating.
	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall(agent.SkillToolName, `{"name":"commit-style"}`),
		gateway.ToolCall(agent.SkillToolName, `{"name":"commit-style"}`),
		gateway.Answer("feat(brain): rate-limit the webhook listener"),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	runner, err := agent.New(openaicompat.Factory(), loops.ReActStreaming())
	if err != nil {
		return err
	}

	// A counter per skill, so the run can report which bodies were actually
	// read. In the brain these closures would read an object store under the
	// owning team's key.
	var commitReads, pdfReads atomic.Int32

	spec := agent.Spec{
		Model:  agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Loop:   agent.LoopReAct,
		System: agent.System("You are a terse assistant. Load a skill before doing work it covers."),
		// Skills, not Tools. They cost one tool between them however many
		// there are, which is the whole reason they are a separate field.
		Skills: []agent.Skill{
			{
				Name:        "commit-style",
				Description: "How to write a commit message this repository will accept. Use before writing any commit message.",
				Body: func(context.Context) (string, error) {
					commitReads.Add(1)
					return commitStyle, nil
				},
			},
			{
				Name:        "pdf-forms",
				Description: "Fill in the fields of a PDF form. Use when asked to complete a PDF.",
				Body: func(context.Context) (string, error) {
					pdfReads.Add(1)
					return "# PDF forms\n\nRun `pdftk form.pdf dump_data_fields`.", nil
				},
			},
		},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{{
			Type: agent.ContentText,
			Text: "Write the commit message for adding a rate limiter to the webhook listener.",
		}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	if err != nil {
		return err
	}

	gateway.Summary(result)
	fmt.Printf("bodies   commit-style read %d time(s), pdf-forms read %d time(s)\n",
		commitReads.Load(), pdfReads.Load())
	return nil
}

// commitStyle is what a SKILL.md body looks like once the brain has read it.
// Long on purpose: this is the cost that only the run that asked for it pays.
const commitStyle = `# Commit style

Write the subject as a Conventional Commit:

    <type>(<scope>): <what changed>

- type is one of feat, fix, docs, chore, refactor, test, ci
- scope is the app or module, omitted for root-level changes
- imperative mood, no trailing period, under 72 characters
- the subject says what changed, the body says why

Do not describe the diff. A reviewer can already read it.`
