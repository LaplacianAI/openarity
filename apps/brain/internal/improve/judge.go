// Package improve judges a team's finished runs and digests the ones its
// agents could learn from: the evidence the improver reads. It runs in the
// background, only for teams with improvement on, never on a path a person
// waits on.
package improve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Judge makes one structured call on a model through the brain's model
// endpoint: the same client the runtime builds runs with.
type Judge struct {
	clients  agent.ClientFactory
	endpoint agent.Endpoint
}

func NewJudge(clients agent.ClientFactory, endpoint agent.Endpoint) Judge {
	return Judge{clients: clients, endpoint: endpoint}
}

// callTimeout bounds one model call, so a call that hangs gives its queue
// slot back and is tried again.
const callTimeout = 2 * time.Minute

// errFinal is a failure asking again would not mend: an answer that is not
// what was asked for, or a team with no model to ask. It is recorded;
// anything else is the model or the brain being unreachable for now, and
// the step is tried again.
type errFinal struct{ msg string }

func (e errFinal) Error() string { return e.msg }

func unusable(format string, a ...any) error {
	return errFinal{"the model's answer was unusable: " + fmt.Sprintf(format, a...)}
}

func final(err error) bool {
	var f errFinal
	return errors.As(err, &f)
}

type ask struct {
	model  agent.ModelRef
	system string
	user   string
	schema *agent.OutputSchema
}

// do makes the call and decodes its JSON into out.
func (j Judge) do(ctx context.Context, a ask, out any) (agent.Usage, error) {
	client, err := j.clients(j.endpoint)
	if err != nil {
		return agent.Usage{}, fmt.Errorf("building the model client: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := client.Complete(ctx, agent.Request{
		Model:        a.model,
		System:       []agent.Content{{Type: agent.ContentText, Text: a.system, Cacheable: true}},
		Messages:     []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: a.user}}}},
		OutputSchema: a.schema,
	})
	if err != nil {
		return agent.Usage{}, fmt.Errorf("asking %s: %w", a.model.Name, err)
	}
	if err := json.Unmarshal([]byte(stripFence(resp.Message.Text())), out); err != nil {
		return resp.Usage, unusable("%v", err)
	}
	return resp.Usage, nil
}

// stripFence takes off the ```json fence some models put round JSON even
// when told the schema.
func stripFence(text string) string {
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		text = strings.TrimSuffix(strings.TrimSpace(rest), "```")
	}
	return strings.TrimSpace(text)
}
