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

// errAnswer is a reply that came back and is not what was asked for: not
// JSON of the schema's shape. Asking again would not help.
type errAnswer struct{ msg string }

func (e errAnswer) Error() string { return "the model's answer was unusable: " + e.msg }

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
	resp, err := client.Complete(ctx, agent.Request{
		Model:        a.model,
		System:       []agent.Content{{Type: agent.ContentText, Text: a.system, Cacheable: true}},
		Messages:     []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: a.user}}}},
		OutputSchema: a.schema,
	})
	if err != nil {
		return agent.Usage{}, err
	}
	text := strings.TrimSpace(resp.Message.Text())
	// Some models fence JSON even when told the schema.
	text = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), out); err != nil {
		return resp.Usage, errAnswer{err.Error()}
	}
	return resp.Usage, nil
}

// doRetrying asks once more after a failure that was not the answer's own:
// a dropped connection, a busy endpoint.
func (j Judge) doRetrying(ctx context.Context, a ask, out any) (agent.Usage, error) {
	usage, err := j.do(ctx, a, out)
	var bad errAnswer
	if err == nil || errors.As(err, &bad) || ctx.Err() != nil {
		return usage, err
	}
	return j.do(ctx, a, out)
}
