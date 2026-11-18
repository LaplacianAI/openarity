// Command approval stops a run on a tool call until a person says yes, and
// carries on from where it stopped.
//
// The agent looks an invoice up and emails its reminder in one turn. The
// lookup just runs. The email asks first: the tool returns agent.Suspend, the
// run ends with the call still open, and the transcript is all there is to
// keep. Once someone has approved, the same transcript is run again; the open
// call is made first, with the call id it had, and only then is the model
// asked anything.
//
// Nothing here is the SDK's idea of an approval. The approvals map, the
// question and the "yes" are this program's; the SDK only stops cleanly and
// picks up again.
//
//	go run ./examples/approval
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

func main() {
	// os.Exit skips deferred calls, so the signal handler is released in
	// attempt rather than in a defer here that would never run.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// desk is this program's approvals: what is waiting, and what was decided,
// by call id. A real one is a table, a Slack thread, a page with two buttons.
type desk struct {
	waiting  map[string]json.RawMessage
	decided  map[string]bool
	lookups  int
	asked    int
	sent     int
	answered []string
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCalls(
			gateway.Call{Name: "find_invoice", Args: `{"number":"1042"}`},
			gateway.Call{Name: "send_email", Args: `{"to":"billing@acme.example","subject":"Invoice 1042 is overdue"}`},
		).Thinking("Find the invoice, then send the reminder."),
		gateway.Answer("Sent the reminder for invoice 1042 to billing@acme.example."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n", endpoint.BaseURL, gateway.Model())

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	d := &desk{waiting: map[string]json.RawMessage{}, decided: map[string]bool{}}
	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You chase overdue invoices. Use the tools you are given."),
		Tools:    []agent.Tool{d.findInvoice(), d.sendEmail()},
		MaxSteps: 5,
	}
	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Chase invoice 1042."}},
	}}

	fmt.Print("\n── first run")
	first, err := run(ctx, runner, spec, msgs, endpoint)
	if !errors.Is(err, agent.ErrSuspended) {
		return fmt.Errorf("the first run should have stopped for approval, and ended with %w", err)
	}
	fmt.Printf("\n\nsuspended  %v\nkept       %d messages, the last turn's call still open\n", err, len(first.Messages))

	// A person, somewhere else and some time later. Nothing of the run is in
	// memory but its transcript: it could have been saved to a Store and the
	// process restarted.
	for id, args := range d.waiting {
		fmt.Printf("\napprover   send_email %s? yes\n", args)
		d.decided[id] = true
	}

	fmt.Print("\n── second run, the same transcript")
	second, err := run(ctx, runner, spec, first.Messages, endpoint)
	if err != nil {
		return err
	}
	gateway.Summary(second)

	fmt.Printf("calls    find_invoice made %d time(s); send_email asked %d time(s) for %v, sent %d\n",
		d.lookups, d.asked, d.answered, d.sent)
	return nil
}

func run(ctx context.Context, runner *agent.Runner, spec agent.Spec, msgs []agent.Message, endpoint agent.Endpoint) (agent.Result, error) {
	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = gateway.Report(events)
	}()
	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done
	return result, err
}

func (d *desk) findInvoice() agent.Tool {
	return agent.Tool{
		Name:        "find_invoice",
		Description: "Look an invoice up by its number.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"number":{"type":"string"}},"required":["number"]}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			d.lookups++
			return "invoice 1042: 1,200 EUR, due 2026-10-31, billing@acme.example", nil
		},
	}
}

// sendEmail asks before it sends. It is called again with the same call id
// once the run is resumed, and looks the decision up by it.
func (d *desk) sendEmail() agent.Tool {
	return agent.Tool{
		Name:        "send_email",
		Description: "Send an email. A person approves every one before it goes.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"to":{"type":"string"},"subject":{"type":"string"}},"required":["to","subject"]}`),
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			id := agent.ToolCallID(ctx)
			d.asked++
			d.answered = append(d.answered, id)

			approved, decided := d.decided[id]
			switch {
			case !decided:
				d.waiting[id] = args
				return "", agent.Suspend("waiting for someone to approve the email")
			case !approved:
				return "", errors.New("a person declined this email; it was not sent")
			}
			d.sent++
			return "sent", nil
		},
	}
}
