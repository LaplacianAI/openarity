package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	maxNameBytes   = 200
	maxTokenBudget = 100_000_000
)

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validate(req agentRequest, loops []string) error {
	switch {
	case req.Name == "" || len(req.Name) > maxNameBytes || strings.TrimSpace(req.Name) != req.Name:
		return errors.New("name must be 1-200 bytes with no space at either end")
	case strings.EqualFold(req.Name, "orchestrator"):
		return errors.New("orchestrator is the built-in hub's name; choose another")
	case req.Pattern == "":
		return errors.New("pattern is required")
	case !slices.Contains(loops, req.Pattern):
		return fmt.Errorf("pattern must be a loop the brain runs: %s", strings.Join(loops, ", "))
	case req.MaxSteps <= 0:
		return errors.New("max_steps must be greater than 0")
	case req.SteerContinuations < 0:
		return errors.New("steer_continuations must not be negative")
	case req.TokenBudget != nil && (*req.TokenBudget <= 0 || *req.TokenBudget > maxTokenBudget):
		return fmt.Errorf("token_budget must be between 1 and %d", maxTokenBudget)
	case hasNUL(req.Name, req.Description, req.Instructions):
		return errors.New("name, description and instructions must not contain a NUL character")
	}

	if err := checkModel("model", req.Model.Name, &req.Model.MaxTokens, req.Model.Temperature); err != nil {
		return err
	}
	if err := checkOutput(req); err != nil {
		return err
	}
	if err := checkGrants(req); err != nil {
		return err
	}
	return checkDelegation(req)
}

func hasNUL(values ...string) bool {
	for _, v := range values {
		if strings.ContainsRune(v, 0) {
			return true
		}
	}
	return false
}

func checkModel(field, name string, maxTokens *int32, temperature *float64) error {
	switch {
	case name == "" || strings.TrimSpace(name) != name || hasNUL(name):
		return fmt.Errorf("%s.name must name a model", field)
	case maxTokens != nil && *maxTokens <= 0:
		return fmt.Errorf("%s.max_tokens must be greater than 0", field)
	case temperature != nil && (*temperature < 0 || *temperature > 2):
		return fmt.Errorf("%s.temperature must be between 0 and 2", field)
	}
	return nil
}

func checkOutput(req agentRequest) error {
	o := req.OutputSchema
	if o == nil {
		switch {
		case req.Parser:
			return errors.New("parser needs an output_schema to parse into")
		case req.ParserModel != nil:
			return errors.New("parser_model needs an output_schema, or nothing would be parsed")
		}
		return nil
	}

	switch {
	case !req.Parser && req.ParserModel != nil:
		return errors.New("parser_model is set but parser is false, so it would never be called")
	case o.Name == "" || hasNUL(o.Name, o.Description):
		return errors.New("output_schema.name is required, and names the schema to the model")
	case !isObject(o.JSON):
		return errors.New("output_schema.json must be a JSON object: the schema the answer has to fit")
	case bytes.Contains(o.JSON, []byte(`\u0000`)):
		return errors.New("output_schema.json must not contain a NUL character")
	}

	if req.ParserModel == nil {
		return nil
	}
	return checkModel("parser_model", req.ParserModel.Name, req.ParserModel.MaxTokens, req.ParserModel.Temperature)
}

func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func checkGrants(req agentRequest) error {
	servers := map[uuid.UUID]bool{}
	for _, g := range req.MCPServers {
		if servers[g.ID] {
			return fmt.Errorf("mcp_servers grants %s twice", g.ID)
		}
		servers[g.ID] = true

		if g.Allow != nil && len(g.Allow) == 0 {
			return fmt.Errorf("mcp_servers %s has an empty allow: leave allow out to grant every tool", g.ID)
		}

		tools := map[string]bool{}
		for _, name := range g.Allow {
			if !toolName.MatchString(name) {
				return fmt.Errorf("mcp_servers %s allows a name that is not 1-64 letters, digits, hyphens or underscores", g.ID)
			}
			if tools[name] {
				return fmt.Errorf("mcp_servers %s allows %s twice", g.ID, name)
			}
			tools[name] = true
		}
	}

	skills := map[uuid.UUID]bool{}
	for _, id := range req.Skills {
		if skills[id] {
			return fmt.Errorf("skills grants %s twice", id)
		}
		skills[id] = true
	}
	return nil
}

func delegationOf(req agentRequest) string {
	if req.Delegation == nil {
		return "none"
	}
	return *req.Delegation
}

func checkDelegation(req agentRequest) error {
	switch d := delegationOf(req); {
	case d != "team" && d != "listed" && d != "none":
		return errors.New("delegation must be team, listed or none")
	case d != "listed" && len(req.Delegates) > 0:
		return errors.New("delegates are only read when delegation is listed")
	}

	seen := map[uuid.UUID]bool{}
	for _, id := range req.Delegates {
		if seen[id] {
			return fmt.Errorf("delegates lists %s twice", id)
		}
		seen[id] = true
	}
	return nil
}
