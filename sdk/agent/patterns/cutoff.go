package patterns

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// continuations is how many times a reply cut off at MaxTokens is asked to
// go on before the run gives up on it.
const continuations = 3

// goOn is the turn that asks a cut-off reply to go on. It is recognised by
// its text when an answer is replaced, so it is never reworded in place.
const goOn = "Your reply was cut off at the token limit. Continue exactly where you stopped, without repeating anything."

// cutCall is what a tool call cut off at MaxTokens is answered with: its
// arguments are incomplete, so it is never run.
const cutCall = "Not run: your reply was cut off at max_tokens while writing this call, so its arguments are incomplete. " +
	"Call it again with less in it, or split the work across several calls."

// carried is a reply, and every continuation it took: each reply and each
// goOn between them, in order, and what they said joined.
type carried struct {
	last agent.Response
	msgs []agent.Message
	text string
}

type caller func(ctx context.Context, in agent.Input, msgs []agent.Message) (agent.Response, error)

// carryOn asks a reply cut off while writing text to go on, up to
// continuations times. A reply cut off while calling a tool comes back as it
// is, with Finish still FinishLength: only a loop that runs tools can answer
// it. Each reply keeps its own message, reasoning and all, since a provider's
// reasoning fields are signed per reply and cannot be merged.
func carryOn(ctx context.Context, in agent.Input, msgs []agent.Message, resp agent.Response, call caller) (carried, error) {
	c := carried{last: resp, msgs: []agent.Message{resp.Message}, text: resp.Message.Text()}
	for range continuations {
		if !cutWriting(c.last) {
			return c, nil
		}
		asked := append(slices.Clone(msgs), c.msgs...)
		asked = append(asked, userText(goOn))
		next, err := call(ctx, in, asked)
		if err != nil {
			return c, err
		}
		in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: next.Usage})
		c.msgs = append(c.msgs, userText(goOn), next.Message)
		c.text += next.Message.Text()
		c.last = next
	}
	if cutWriting(c.last) {
		return c, agent.ErrTruncated
	}
	return c, nil
}

func cutWriting(r agent.Response) bool {
	return r.Finish == agent.FinishLength && len(r.Message.ToolCalls) == 0
}

// answerCut empties the arguments of a reply's tool calls, cut off part-way
// through: a provider refuses a history holding half-written JSON. It
// returns the turns that answer each call, which a provider wants before
// anything else is said.
func answerCut(msg *agent.Message) []agent.Message {
	calls := slices.Clone(msg.ToolCalls)
	answers := make([]agent.Message, 0, len(calls))
	for i := range calls {
		calls[i].Arguments = json.RawMessage(`{}`)
		answers = append(answers, agent.Message{
			Role:       agent.RoleTool,
			ToolCallID: calls[i].ID,
			Content:    []agent.Content{{Type: agent.ContentText, Text: cutCall}},
		})
	}
	msg.ToolCalls = calls
	return answers
}

// answerStart is where the answer that ends msgs begins: its last message,
// or the first of the replies a goOn carried on.
func answerStart(msgs []agent.Message) int {
	i := len(msgs) - 1
	for i >= 2 && msgs[i-1].Role == agent.RoleUser && msgs[i-1].Text() == goOn {
		i -= 2
	}
	return i
}

func userText(s string) agent.Message {
	return agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: s}}}
}
