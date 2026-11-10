package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// testLoops is a brain that runs two built-in loops and one of its own.
var testLoops = []string{"react", "plan", "critic-pass"}

// valid is the smallest agent the brain accepts. Each test changes one thing.
func valid() Request {
	return Request{
		Name:     "triage",
		Model:    ModelRef{Name: "anthropic/claude-opus-5", MaxTokens: 4096},
		Pattern:  "react",
		MaxSteps: 8,
	}
}

func schema() *OutputSchema {
	return &OutputSchema{Name: "verdict", JSON: json.RawMessage(`{"type":"object"}`)}
}

func TestTheSmallestAgentIsValid(t *testing.T) {
	t.Parallel()

	if err := Validate(valid(), testLoops); err != nil {
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
	req.OutputSchema = &OutputSchema{Name: "verdict", Description: "d", JSON: json.RawMessage(" \n{\"type\":\"object\"}"), Strict: true}
	req.Parser = true
	req.ParserModel = &ParserModel{Name: "openai/gpt-5-mini", MaxTokens: ptr(int32(512)), Temperature: ptr(2.0)}
	req.MCPServers = []ServerGrant{{ID: uuid.New(), Allow: []string{"search_issues", "create-issue"}}, {ID: uuid.New()}}
	req.Skills = []uuid.UUID{uuid.New(), uuid.New()}

	if err := Validate(req, testLoops); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Each rule, broken alone, and the sentence the caller reads.
func TestEachRuleHasItsSentence(t *testing.T) {
	t.Parallel()

	server := uuid.New()
	skill := uuid.New()

	for name, tc := range map[string]struct {
		change func(*Request)
		want   string
	}{
		"no name":                    {func(r *Request) { r.Name = "" }, "name must be 1-200 bytes"},
		"a long name":                {func(r *Request) { r.Name = strings.Repeat("n", 201) }, "name must be 1-200 bytes"},
		"a padded name":              {func(r *Request) { r.Name = " triage" }, "no space at either end"},
		"a name ending in a newline": {func(r *Request) { r.Name = "triage\n" }, "no space at either end"},
		"the orchestrator's name":    {func(r *Request) { r.Name = "orchestrator" }, "orchestrator is the built-in hub's name"},
		"it in another case":         {func(r *Request) { r.Name = "Orchestrator" }, "orchestrator is the built-in hub's name"},
		"no pattern":                 {func(r *Request) { r.Pattern = "" }, "pattern is required"},
		"a loop the brain does not run": {
			func(r *Request) { r.Pattern = "code" }, "pattern must be a loop the brain runs: react, plan, critic-pass",
		},
		"a loop in another case":  {func(r *Request) { r.Pattern = "React" }, "pattern must be a loop the brain runs"},
		"zero steps":              {func(r *Request) { r.MaxSteps = 0 }, "max_steps must be greater than 0"},
		"negative steps":          {func(r *Request) { r.MaxSteps = -1 }, "max_steps must be greater than 0"},
		"negative steering":       {func(r *Request) { r.SteerContinuations = -1 }, "steer_continuations must not be negative"},
		"a budget of zero":        {func(r *Request) { r.TokenBudget = ptr(int64(0)) }, "token_budget must be between 1 and 100000000"},
		"a negative budget":       {func(r *Request) { r.TokenBudget = ptr(int64(-1)) }, "token_budget must be between 1 and 100000000"},
		"a budget past the cap":   {func(r *Request) { r.TokenBudget = ptr(int64(100_000_001)) }, "token_budget must be between 1 and 100000000"},
		"NUL in the name":         {func(r *Request) { r.Name = "tri\x00age" }, "NUL"},
		"NUL in the description":  {func(r *Request) { r.Description = "a\x00b" }, "NUL"},
		"NUL in the instructions": {func(r *Request) { r.Instructions = "a\x00b" }, "NUL"},

		"no model":               {func(r *Request) { r.Model.Name = "" }, "model.name must name a model"},
		"a padded model":         {func(r *Request) { r.Model.Name = "gpt " }, "model.name must name a model"},
		"NUL in the model":       {func(r *Request) { r.Model.Name = "g\x00pt" }, "model.name must name a model"},
		"zero max tokens":        {func(r *Request) { r.Model.MaxTokens = 0 }, "model.max_tokens must be greater than 0"},
		"negative max tokens":    {func(r *Request) { r.Model.MaxTokens = -5 }, "model.max_tokens must be greater than 0"},
		"temperature below zero": {func(r *Request) { r.Model.Temperature = ptr(-0.01) }, "model.temperature must be between 0 and 2"},
		"temperature above two":  {func(r *Request) { r.Model.Temperature = ptr(2.01) }, "model.temperature must be between 0 and 2"},

		"a parser with no schema":         {func(r *Request) { r.Parser = true }, "parser needs an output_schema"},
		"a parser model with no schema":   {func(r *Request) { r.ParserModel = &ParserModel{Name: "m"} }, "parser_model needs an output_schema"},
		"a parser model with no parser":   {func(r *Request) { r.OutputSchema = schema(); r.ParserModel = &ParserModel{Name: "m"} }, "parser is false"},
		"a schema with no name":           {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.Name = "" }, "output_schema.name is required"},
		"NUL in the schema's name":        {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.Name = "a\x00" }, "output_schema.name is required"},
		"NUL in the schema's description": {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.Description = "a\x00" }, "output_schema.name is required"},
		"a schema with no json":           {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.JSON = nil }, "output_schema.json must be a JSON object"},
		"a schema that is null":           {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`null`) }, "output_schema.json must be a JSON object"},
		"a schema that is a number":       {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`3`) }, "output_schema.json must be a JSON object"},
		"a schema that is an array":       {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`[{}]`) }, "output_schema.json must be a JSON object"},
		"a schema that is not JSON":       {func(r *Request) { r.OutputSchema = schema(); r.OutputSchema.JSON = json.RawMessage(`{"type":`) }, "output_schema.json must be a JSON object"},
		"NUL inside the schema": {func(r *Request) {
			r.OutputSchema = schema()
			r.OutputSchema.JSON = json.RawMessage(`{"d":"a\u0000b"}`)
		}, "output_schema.json must not contain a NUL"},
		"a parser model with no name": {func(r *Request) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &ParserModel{}
		}, "parser_model.name must name a model"},
		"a parser model with zero tokens": {func(r *Request) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &ParserModel{Name: "m", MaxTokens: ptr(int32(0))}
		}, "parser_model.max_tokens must be greater than 0"},
		"a parser model too hot": {func(r *Request) {
			r.OutputSchema, r.Parser, r.ParserModel = schema(), true, &ParserModel{Name: "m", Temperature: ptr(3.0)}
		}, "parser_model.temperature must be between 0 and 2"},

		"a server granted twice": {func(r *Request) { r.MCPServers = []ServerGrant{{ID: server}, {ID: server}} }, "grants " + server.String() + " twice"},
		"an empty allow":         {func(r *Request) { r.MCPServers = []ServerGrant{{ID: server, Allow: []string{}}} }, "empty allow"},
		"an allowed name that is not a tool name": {func(r *Request) {
			r.MCPServers = []ServerGrant{{ID: server, Allow: []string{"search issues"}}}
		}, "allows a name that is not 1-64"},
		"an allowed name that is empty": {func(r *Request) { r.MCPServers = []ServerGrant{{ID: server, Allow: []string{""}}} }, "allows a name that is not 1-64"},
		"an allowed name of 65": {func(r *Request) {
			r.MCPServers = []ServerGrant{{ID: server, Allow: []string{strings.Repeat("a", 65)}}}
		}, "allows a name that is not 1-64"},
		"a tool allowed twice":  {func(r *Request) { r.MCPServers = []ServerGrant{{ID: server, Allow: []string{"a", "b", "a"}}} }, "allows a twice"},
		"a skill granted twice": {func(r *Request) { r.Skills = []uuid.UUID{skill, uuid.New(), skill} }, "skills grants " + skill.String() + " twice"},
	} {
		req := valid()
		tc.change(&req)
		err := Validate(req, testLoops)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
	}
}

// The boundaries are inclusive where the rule says "between" and exclusive
// where it says "greater than".
func TestTheEdgesThatAreAllowed(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*Request){
		"a 200-byte name":            func(r *Request) { r.Name = strings.Repeat("n", 200) },
		"one step":                   func(r *Request) { r.MaxSteps = 1 },
		"one token":                  func(r *Request) { r.Model.MaxTokens = 1 },
		"no steering":                func(r *Request) { r.SteerContinuations = 0 },
		"a budget of one":            func(r *Request) { r.TokenBudget = ptr(int64(1)) },
		"a budget at the cap":        func(r *Request) { r.TokenBudget = ptr(int64(100_000_000)) },
		"temperature zero":           func(r *Request) { r.Model.Temperature = ptr(0.0) },
		"temperature two":            func(r *Request) { r.Model.Temperature = ptr(2.0) },
		"a name with a space inside": func(r *Request) { r.Name = "issue triage" },
		"a 64-character allow": func(r *Request) {
			r.MCPServers = []ServerGrant{{ID: uuid.New(), Allow: []string{strings.Repeat("a", 64)}}}
		},
		"no allow at all":                 func(r *Request) { r.MCPServers = []ServerGrant{{ID: uuid.New()}} },
		"a schema without a parser":       func(r *Request) { r.OutputSchema = schema() },
		"a parser with the default model": func(r *Request) { r.OutputSchema, r.Parser = schema(), true },
		"a schema with an escaped backslash-u": func(r *Request) {
			r.OutputSchema = schema()
			r.OutputSchema.JSON = json.RawMessage(`{"pattern":"\\d+"}`)
		},
		"a deployment's own loop": func(r *Request) { r.Pattern = "critic-pass" },
		"any kind string":         func(r *Request) { r.Kind = "orchestrator" },
	} {
		req := valid()
		change(&req)
		if err := Validate(req, testLoops); err != nil {
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
	if err := Validate(req, testLoops); err == nil || strings.Contains(err.Error(), "evil") {
		t.Errorf("err = %v, want a refusal that does not repeat the name", err)
	}
}
