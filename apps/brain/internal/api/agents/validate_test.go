package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// valid is the smallest agent the brain accepts. Each test changes one thing.
func valid() agentRequest {
	return agentRequest{
		Name:     "triage",
		Model:    modelRef{Name: "anthropic/claude-opus-5", MaxTokens: 4096},
		Pattern:  "react",
		MaxSteps: 8,
	}
}

func schema() *outputSchema {
	return &outputSchema{Name: "verdict", JSON: json.RawMessage(`{"type":"object"}`)}
}

func TestTheSmallestAgentIsValid(t *testing.T) {
	t.Parallel()

	if err := validate(valid()); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Everything the request can carry, set at once and correctly, so a check
// that refuses a valid combination shows up here rather than in production.
func TestAFullAgentIsValid(t *testing.T) {
	t.Parallel()

	req := valid()
	req.Description = "Sorts incoming issues"
	req.Kind = KindCustom
	req.Instructions = "You sort incoming issues."
	req.Model.Temperature = ptr(0.0)
	req.SteerContinuations = 2
	req.OutputSchema = &outputSchema{Name: "verdict", Description: "d", JSON: json.RawMessage(" \n{\"type\":\"object\"}"), Strict: true}
	req.Parser = true
	req.ParserModel = &parserModel{Name: "openai/gpt-5-mini", MaxTokens: ptr(int32(512)), Temperature: ptr(2.0)}
	req.MCPServers = []serverGrant{{ID: uuid.New(), Allow: []string{"search_issues", "create-issue"}}, {ID: uuid.New()}}
	req.Skills = []uuid.UUID{uuid.New(), uuid.New()}

	if err := validate(req); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Each rule, broken alone, and the sentence the caller reads.
func TestEachRuleHasItsSentence(t *testing.T) {
	t.Parallel()

	server := uuid.New()
	skill := uuid.New()

	for name, tc := range map[string]struct {
		change func(*agentRequest)
		want   string
	}{
		"no name":                    {func(r *agentRequest) { r.Name = "" }, "name must be 1-200 bytes"},
		"a long name":                {func(r *agentRequest) { r.Name = strings.Repeat("n", 201) }, "name must be 1-200 bytes"},
		"a padded name":              {func(r *agentRequest) { r.Name = " triage" }, "no space at either end"},
		"a name ending in a newline": {func(r *agentRequest) { r.Name = "triage\n" }, "no space at either end"},
		"no pattern":                 {func(r *agentRequest) { r.Pattern = "" }, "pattern is required"},
		"zero steps":                 {func(r *agentRequest) { r.MaxSteps = 0 }, "max_steps must be greater than 0"},
		"negative steps":             {func(r *agentRequest) { r.MaxSteps = -1 }, "max_steps must be greater than 0"},
		"negative steering":          {func(r *agentRequest) { r.SteerContinuations = -1 }, "steer_continuations must not be negative"},
		"NUL in the name":            {func(r *agentRequest) { r.Name = "tri\x00age" }, "NUL"},
		"NUL in the description":     {func(r *agentRequest) { r.Description = "a\x00b" }, "NUL"},
		"NUL in the instructions":    {func(r *agentRequest) { r.Instructions = "a\x00b" }, "NUL"},

		"no model":               {func(r *agentRequest) { r.Model.Name = "" }, "model.name must name a model"},
		"a padded model":         {func(r *agentRequest) { r.Model.Name = "gpt " }, "model.name must name a model"},
		"NUL in the model":       {func(r *agentRequest) { r.Model.Name = "g\x00pt" }, "model.name must name a model"},
		"zero max tokens":        {func(r *agentRequest) { r.Model.MaxTokens = 0 }, "model.max_tokens must be greater than 0"},
		"negative max tokens":    {func(r *agentRequest) { r.Model.MaxTokens = -5 }, "model.max_tokens must be greater than 0"},
		"temperature below zero": {func(r *agentRequest) { r.Model.Temperature = ptr(-0.01) }, "model.temperature must be between 0 and 2"},
		"temperature above two":  {func(r *agentRequest) { r.Model.Temperature = ptr(2.01) }, "model.temperature must be between 0 and 2"},

		"a parser with no schema":         {func(r *agentRequest) { r.Parser = true }, "parser needs an output_schema"},
		"a parser model with no schema":   {func(r *agentRequest) { r.ParserModel = &parserModel{Name: "m"} }, "parser_model needs an output_schema"},
		"a parser model with no parser":   {func(r *agentRequest) { r.OutputSchema = schema(); r.ParserModel = &parserModel{Name: "m"} }, "parser is false"},
		"a schema with no name":           {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.Name = "" }, "output_schema.name is required"},
		"NUL in the schema's name":        {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.Name = "a\x00" }, "output_schema.name is required"},
		"NUL in the schema's description": {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.Description = "a\x00" }, "output_schema.name is required"},
		"a schema with no json":           {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.JSON = nil }, "output_schema.json must be a JSON object"},
		"a schema that is null":           {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`null`) }, "output_schema.json must be a JSON object"},
		"a schema that is a number":       {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`3`) }, "output_schema.json must be a JSON object"},
		"a schema that is an array":       {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`[{}]`) }, "output_schema.json must be a JSON object"},
		"a schema that is not JSON":       {func(r *agentRequest) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`{"type":`) }, "output_schema.json must be a JSON object"},
		"NUL inside the schema": {func(r *agentRequest) {
			r.OutputSchema = schema()
			r.OutputSchema.JSON = json.RawMessage(`{"d":"a\u0000b"}`)
		}, "output_schema.json must not contain a NUL"},
		"a parser model with no name": {func(r *agentRequest) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &parserModel{}
		}, "parser_model.name must name a model"},
		"a parser model with zero tokens": {func(r *agentRequest) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &parserModel{Name: "m", MaxTokens: ptr(int32(0))}
		}, "parser_model.max_tokens must be greater than 0"},
		"a parser model too hot": {func(r *agentRequest) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &parserModel{Name: "m", Temperature: ptr(3.0)}
		}, "parser_model.temperature must be between 0 and 2"},

		"a server granted twice": {func(r *agentRequest) { r.MCPServers = []serverGrant{{ID: server}, {ID: server}} }, "grants " + server.String() + " twice"},
		"an empty allow":         {func(r *agentRequest) { r.MCPServers = []serverGrant{{ID: server, Allow: []string{}}} }, "empty allow"},
		"an allowed name that is not a tool name": {func(r *agentRequest) {
			r.MCPServers = []serverGrant{{ID: server, Allow: []string{"search issues"}}}
		}, "allows a name that is not 1-64"},
		"an allowed name that is empty": {func(r *agentRequest) { r.MCPServers = []serverGrant{{ID: server, Allow: []string{""}}} }, "allows a name that is not 1-64"},
		"an allowed name of 65": {func(r *agentRequest) {
			r.MCPServers = []serverGrant{{ID: server, Allow: []string{strings.Repeat("a", 65)}}}
		}, "allows a name that is not 1-64"},
		"a tool allowed twice":  {func(r *agentRequest) { r.MCPServers = []serverGrant{{ID: server, Allow: []string{"a", "b", "a"}}} }, "allows a twice"},
		"a skill granted twice": {func(r *agentRequest) { r.Skills = []uuid.UUID{skill, uuid.New(), skill} }, "skills grants " + skill.String() + " twice"},
	} {
		req := valid()
		tc.change(&req)
		err := validate(req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
	}
}

// The boundaries are inclusive where the rule says "between" and exclusive
// where it says "greater than".
func TestTheEdgesThatAreAllowed(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*agentRequest){
		"a 200-byte name":            func(r *agentRequest) { r.Name = strings.Repeat("n", 200) },
		"one step":                   func(r *agentRequest) { r.MaxSteps = 1 },
		"one token":                  func(r *agentRequest) { r.Model.MaxTokens = 1 },
		"no steering":                func(r *agentRequest) { r.SteerContinuations = 0 },
		"temperature zero":           func(r *agentRequest) { r.Model.Temperature = ptr(0.0) },
		"temperature two":            func(r *agentRequest) { r.Model.Temperature = ptr(2.0) },
		"a name with a space inside": func(r *agentRequest) { r.Name = "issue triage" },
		"a 64-character allow": func(r *agentRequest) {
			r.MCPServers = []serverGrant{{ID: uuid.New(), Allow: []string{strings.Repeat("a", 64)}}}
		},
		"no allow at all":                 func(r *agentRequest) { r.MCPServers = []serverGrant{{ID: uuid.New()}} },
		"a schema without a parser":       func(r *agentRequest) { r.OutputSchema = schema() },
		"a parser with the default model": func(r *agentRequest) { r.OutputSchema, r.Parser = schema(), true },
		"a schema with an escaped backslash-u": func(r *agentRequest) {
			r.OutputSchema = schema()
			r.OutputSchema.JSON = json.RawMessage(`{"pattern":"\\d+"}`)
		},
		"any pattern string": func(r *agentRequest) { r.Pattern = "something-new" },
		"any kind string":    func(r *agentRequest) { r.Kind = "orchestrator" },
	} {
		req := valid()
		change(&req)
		if err := validate(req); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A refused name is not repeated back: it may be the reason it was refused,
// and a NUL or a control character in a reply is a second problem.
func TestARefusalDoesNotEchoTheValue(t *testing.T) {
	t.Parallel()

	req := valid()
	req.Name = "\x00evil"
	if err := validate(req); err == nil || strings.Contains(err.Error(), "evil") {
		t.Errorf("err = %v, want a refusal that does not repeat the name", err)
	}
}
