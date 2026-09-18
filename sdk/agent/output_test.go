package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var findingSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"file": {"type": "string"},
		"line": {"type": "integer"}
	},
	"required": ["file", "line"],
	"additionalProperties": false
}`)

func finding() *OutputSchema {
	return &OutputSchema{Name: "finding", Description: "The failing line.", JSON: findingSchema}
}

type answeringPattern struct {
	answer string
	calls  int
}

func (*answeringPattern) Name() PatternName { return "answering" }

func (p *answeringPattern) Run(ctx context.Context, in Input) (Result, error) {
	msgs := append([]Message(nil), in.Messages...)

	for range max(p.calls, 1) {
		if _, err := in.Model.Complete(ctx, Request{Messages: msgs, Tools: in.Spec.Tools}); err != nil {
			return Result{}, err
		}
	}

	msgs = append(msgs, Message{
		Role:    RoleAssistant,
		Content: []Content{{Type: ContentText, Text: p.answer}},
	})
	return Result{Output: p.answer, Messages: msgs, Steps: 1}, nil
}

func runnerFor(t *testing.T, inner ModelClient, pattern Pattern) *Runner {
	t.Helper()

	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, pattern)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return runner
}

func TestASpecWithNoOutputIsUntouched(t *testing.T) {
	inner := &recordingClient{}
	runner := runnerFor(t, inner, &answeringPattern{answer: "plain prose"})

	result, err := runner.Run(t.Context(), Spec{Pattern: "answering", MaxSteps: 1}, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if result.Structured != nil {
		t.Errorf("Structured = %s on a run that asked for no schema", result.Structured)
	}
	if got := inner.requests()[0].OutputSchema; got != nil {
		t.Errorf("a request carried a Format nobody asked for: %+v", got)
	}
}

func TestEveryRequestInTheLoopCarriesTheSchema(t *testing.T) {
	inner := &recordingClient{}
	runner := runnerFor(t, inner, &answeringPattern{answer: `{"file":"vault.go","line":88}`, calls: 3})

	spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding()}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := inner.requests()
	if len(requests) != 3 {
		t.Fatalf("the pattern made %d requests, want 3", len(requests))
	}
	for i, req := range requests {
		if req.OutputSchema == nil {
			t.Errorf("request %d carried no Format, so the gateway would be told nothing", i)
			continue
		}
		if req.OutputSchema.Name != "finding" {
			t.Errorf("request %d named the schema %q, want %q", i, req.OutputSchema.Name, "finding")
		}
	}
}

func TestAJSONAnswerBecomesTheStructuredResult(t *testing.T) {
	inner := &recordingClient{}
	answer := `{"file":"vault.go","line":88}`
	runner := runnerFor(t, inner, &answeringPattern{answer: answer})

	spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding()}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if string(result.Structured) != answer {
		t.Errorf("Structured = %s, want %s", result.Structured, answer)
	}

	var into struct {
		File string `json:"file"`
		Line int    `json:"line"`
	}
	if err := json.Unmarshal(result.Structured, &into); err != nil {
		t.Fatalf("the caller could not unmarshal it: %v", err)
	}
	if into.File != "vault.go" || into.Line != 88 {
		t.Errorf("unmarshalled to %+v", into)
	}
}

func TestProseWhereASchemaWasAskedForIsNotAnError(t *testing.T) {
	inner := &recordingClient{}
	runner := runnerFor(t, inner, &answeringPattern{answer: "I need more context."})

	spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding()}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v, want nil — an unmet schema is reported by a nil Structured", err)
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil", result.Structured)
	}
	if result.Output != "I need more context." {
		t.Errorf("the prose was lost: %q", result.Output)
	}
}

func TestInParserModeTheLoopIsLeftAlone(t *testing.T) {
	var seen []Request
	inner := &recordingClient{}
	inner.reply = func(req Request) Response {
		seen = append(seen, req)
		if req.OutputSchema != nil {
			return Response{Message: Message{
				Role:    RoleAssistant,
				Content: []Content{{Type: ContentText, Text: `{"file":"vault.go","line":88}`}},
			}}
		}
		return Response{}
	}

	runner := runnerFor(t, inner, &answeringPattern{answer: "It fails at vault.go line 88.", calls: 2})
	spec := Spec{
		Pattern:      "answering",
		MaxSteps:     1,
		OutputSchema: finding(),
		Parser:       true,
		Model:        ModelRef{Name: "big"},
	}

	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if len(seen) != 3 {
		t.Fatalf("made %d calls, want 3 — two in the loop and one to parse", len(seen))
	}
	for i, req := range seen[:2] {
		if req.OutputSchema != nil {
			t.Errorf("loop request %d carried a schema; in parser mode only the parser call should", i)
		}
	}

	parse := seen[2]
	if parse.OutputSchema == nil {
		t.Fatal("the parser call carried no schema")
	}
	if len(parse.Tools) != 0 {
		t.Errorf("the parser call was given %d tools; it has nothing to call", len(parse.Tools))
	}
	if got := text(parse.Messages[len(parse.Messages)-1]); !strings.Contains(got, "vault.go line 88") {
		t.Errorf("the parser was not shown the answer, it saw: %q", got)
	}
	if string(result.Structured) != `{"file":"vault.go","line":88}` {
		t.Errorf("Structured = %s", result.Structured)
	}
}

func TestTheParserFallsBackToTheRunsOwnModel(t *testing.T) {
	var parsedWith string
	inner := &recordingClient{}
	inner.reply = func(req Request) Response {
		if req.OutputSchema != nil {
			parsedWith = req.Model.Name
		}
		return Response{}
	}

	runner := runnerFor(t, inner, &answeringPattern{answer: "prose"})
	spec := Spec{
		Pattern: "answering", MaxSteps: 1,
		Model:        ModelRef{Name: "the-big-one"},
		OutputSchema: finding(), Parser: true,
	}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if parsedWith != "the-big-one" {
		t.Errorf("the parser used model %q, want the run's own model", parsedWith)
	}
}

func TestAParserModelIsUsedForTheParseAndNothingElse(t *testing.T) {
	var loopModels []string
	var parsedWith string
	inner := &recordingClient{}
	inner.reply = func(req Request) Response {
		if req.OutputSchema != nil {
			parsedWith = req.Model.Name
		} else {
			loopModels = append(loopModels, req.Model.Name)
		}
		return Response{}
	}

	runner := runnerFor(t, inner, &answeringPattern{answer: "prose", calls: 2})
	spec := Spec{
		Pattern: "answering", MaxSteps: 1,
		Model:        ModelRef{Name: "the-big-one"},
		OutputSchema: finding(),
		Parser:       true,
		ParserModel:  ModelRef{Name: "the-cheap-one"},
	}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if parsedWith != "the-cheap-one" {
		t.Errorf("the parse used %q, want the parser model", parsedWith)
	}
	for i, name := range loopModels {
		if name == "the-cheap-one" {
			t.Errorf("loop call %d used the parser model; it is only for the parse", i)
		}
	}
}

func TestAParserThatCannotBeReachedIsAnError(t *testing.T) {
	boom := errors.New("gateway said no")
	inner := &recordingClient{}
	failing := &failOnFormat{inner: inner, err: boom}

	runner := runnerFor(t, failing, &answeringPattern{answer: "prose"})

	spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding(), Parser: true}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if !errors.Is(err, boom) {
		t.Errorf("Run() = %v, want the gateway's error", err)
	}
	if result.Output != "prose" {
		t.Errorf("the answer the run did produce was thrown away: %q", result.Output)
	}
}

type failOnFormat struct {
	inner ModelClient
	err   error
}

func (c *failOnFormat) Complete(ctx context.Context, req Request) (Response, error) {
	if req.OutputSchema != nil {
		return Response{}, c.err
	}
	return c.inner.Complete(ctx, req)
}

func (c *failOnFormat) Stream(ctx context.Context, req Request) (Stream, error) {
	return c.inner.Stream(ctx, req)
}

func TestTheParserCallIsPaidFor(t *testing.T) {
	inner := &recordingClient{}
	inner.reply = func(Request) Response {
		return Response{Usage: Usage{InputTokens: 10, OutputTokens: 5}}
	}

	runner := runnerFor(t, inner, &answeringPattern{answer: "prose"})
	spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding(), Parser: true}

	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if result.Usage.OutputTokens != 10 {
		t.Errorf("Usage counted %d output tokens over two calls of 5, want 10 — the parser\n"+
			"call is spend like any other", result.Usage.OutputTokens)
	}
}

func TestSpecsThatCouldNotDoWhatTheyAskFor(t *testing.T) {
	for name, spec := range map[string]Spec{
		"a parser with nothing to parse into": {
			Pattern: "answering", MaxSteps: 1, Parser: true,
		},
		"a parser model with nothing to parse into": {
			Pattern: "answering", MaxSteps: 1, ParserModel: ModelRef{Name: "cheap"},
		},
		"a parser model that would never be called": {
			Pattern: "answering", MaxSteps: 1, OutputSchema: finding(),
			ParserModel: ModelRef{Name: "cheap"},
		},
		"an output with no name": {
			Pattern: "answering", MaxSteps: 1,
			OutputSchema: &OutputSchema{JSON: findingSchema},
		},
		"an output with no schema": {
			Pattern: "answering", MaxSteps: 1,
			OutputSchema: &OutputSchema{Name: "finding"},
		},
		"an output whose schema is not JSON": {
			Pattern: "answering", MaxSteps: 1,
			OutputSchema: &OutputSchema{Name: "finding", JSON: json.RawMessage(`{not json`)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			inner := &recordingClient{}
			runner := runnerFor(t, inner, &answeringPattern{answer: "prose"})

			if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err == nil {
				t.Error("the spec was accepted")
			}
			if len(inner.requests()) != 0 {
				t.Error("a refused spec still reached the model")
			}
		})
	}
}

func TestStrictIsCarriedThroughOnlyWhenAsked(t *testing.T) {
	for _, strict := range []bool{false, true} {
		inner := &recordingClient{}
		runner := runnerFor(t, inner, &answeringPattern{answer: "{}"})

		out := finding()
		out.Strict = strict
		spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: out}
		if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
			t.Fatalf("Run() = %v", err)
		}

		if got := inner.requests()[0].OutputSchema.Strict; got != strict {
			t.Errorf("Strict = %v, want %v", got, strict)
		}
	}
}

func TestAStreamingPatternIsGivenTheSchemaToo(t *testing.T) {
	inner := &recordingClient{}
	client := &outputClient{inner: inner, schema: finding()}

	if _, err := client.Stream(t.Context(), Request{}); err != nil {
		t.Fatalf("Stream() = %v", err)
	}

	if got := inner.requests()[0].OutputSchema; got == nil {
		t.Error("Stream() sent no Format, so the default streaming patterns would ignore the schema")
	}
}

func TestJSONInAMarkdownFenceIsStillJSON(t *testing.T) {
	for name, answer := range map[string]string{
		"tagged fence":    "```json\n{\"file\":\"vault.go\"}\n```",
		"bare fence":      "```\n{\"file\":\"vault.go\"}\n```",
		"no newlines":     "```{\"file\":\"vault.go\"}```",
		"leading space":   "\n\n```json\n{\"file\":\"vault.go\"}\n```\n",
		"no fence at all": `{"file":"vault.go"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := structured(answer)
			if got == nil {
				t.Fatalf("structured(%q) = nil — a model that fences its JSON still answered", answer)
			}
			var into struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(got, &into); err != nil {
				t.Fatalf("does not unmarshal: %v\n%s", err, got)
			}
			if into.File != "vault.go" {
				t.Errorf("file = %q", into.File)
			}
		})
	}
}

func TestJSONAfterASentenceIsStillFound(t *testing.T) {
	// Verbatim from cc/claude-opus-4-8 through OmniRoute with a schema set: it
	// answered correctly and put the answer in a fence after a sentence.
	answer := "Based on my investigation, the failing test relates to `renewLease` in " +
		"`vault.go` at line 88, where the function should return a nil lease when the " +
		"token is already expired.\n\n```json\n{\n  \"file\": \"vault.go\",\n  " +
		"\"line\": 88,\n  \"severity\": \"high\"\n}\n```"

	got := structured(answer)
	if got == nil {
		t.Fatal("structured() = nil — the model answered in the schema, just not only in it")
	}

	var into struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(got, &into); err != nil {
		t.Fatalf("does not unmarshal: %v\n%s", err, got)
	}
	if into.File != "vault.go" || into.Line != 88 || into.Severity != "high" {
		t.Errorf("got %+v", into)
	}
}

func TestTheWholeAnswerWinsOverAFenceInsideIt(t *testing.T) {
	// A JSON answer that quotes a fenced block in one of its strings must not
	// be mistaken for a preamble wrapping that block.
	answer := "{\"file\":\"vault.go\",\"note\":\"see ```json\\n{}\\n```\"}"

	got := structured(answer)
	var into struct {
		File string `json:"file"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(got, &into); err != nil {
		t.Fatalf("does not unmarshal: %v\n%s", err, got)
	}
	if into.File != "vault.go" {
		t.Errorf("the inner fence was preferred to the answer itself: %s", got)
	}
}

func TestAFenceWithNoJSONInItYieldsNothing(t *testing.T) {
	for _, answer := range []string{"```\nnot json\n```", "```json\n```", "```"} {
		if got := structured(answer); got != nil {
			t.Errorf("structured(%q) = %s, want nil", answer, got)
		}
	}
}

func TestAnAnswerThatIsNotJSONYieldsNothing(t *testing.T) {
	for _, answer := range []string{"", "I need more context.", `{"file":`, "null"} {
		got := structured(answer)
		if answer == "null" {
			if string(got) != "null" {
				t.Errorf("structured(%q) = %s, want it kept — null is valid JSON", answer, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("structured(%q) = %s, want nil", answer, got)
		}
	}
}

func TestOutputIsTheAnswerInTheFormThatWasAskedFor(t *testing.T) {
	const prose = "It fails at vault.go line 88."
	const raw = `{"file":"vault.go","line":88}`

	t.Run("no schema", func(t *testing.T) {
		inner := &recordingClient{}
		runner := runnerFor(t, inner, &answeringPattern{answer: prose})

		result, err := runner.Run(t.Context(), Spec{Pattern: "answering", MaxSteps: 1},
			nil, Endpoint{}, nil)
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if result.Output != prose || result.Plain != prose {
			t.Errorf("Output = %q, Plain = %q, want both %q", result.Output, result.Plain, prose)
		}
		if result.Structured != nil {
			t.Errorf("Structured = %s, want nil", result.Structured)
		}
	})

	t.Run("inline", func(t *testing.T) {
		inner := &recordingClient{}
		runner := runnerFor(t, inner, &answeringPattern{answer: raw})

		spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding()}
		result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if result.Output != raw {
			t.Errorf("Output = %q, want %q", result.Output, raw)
		}
		if result.Plain != raw {
			t.Errorf("Plain = %q — inline, the model's own words already were the JSON", result.Plain)
		}
	})

	t.Run("parser", func(t *testing.T) {
		inner := &recordingClient{}
		inner.reply = func(req Request) Response {
			if req.OutputSchema == nil {
				return Response{}
			}
			return Response{Message: Message{
				Role:    RoleAssistant,
				Content: []Content{{Type: ContentText, Text: raw}},
			}}
		}
		runner := runnerFor(t, inner, &answeringPattern{answer: prose})

		spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding(), Parser: true}
		result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if result.Output != raw {
			t.Errorf("Output = %q, want the schema you asked for: %q", result.Output, raw)
		}
		if result.Plain != prose {
			t.Errorf("Plain = %q, want what the model actually said: %q", result.Plain, prose)
		}
	})

	t.Run("a schema that went unmet", func(t *testing.T) {
		inner := &recordingClient{}
		runner := runnerFor(t, inner, &answeringPattern{answer: prose})

		spec := Spec{Pattern: "answering", MaxSteps: 1, OutputSchema: finding()}
		result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if result.Output != prose {
			t.Errorf("Output = %q — with no Structured to promote, it stays the prose", result.Output)
		}
		if result.Plain != prose {
			t.Errorf("Plain = %q, want %q", result.Plain, prose)
		}
	})
}

func TestTheParserIsShownWhatTheRunLearned(t *testing.T) {
	var saw string
	inner := &recordingClient{}
	inner.reply = func(req Request) Response {
		if req.OutputSchema == nil {
			return Response{}
		}
		saw = text(req.Messages[len(req.Messages)-1])
		return Response{Message: Message{
			Role:    RoleAssistant,
			Content: []Content{{Type: ContentText, Text: `{"file":"vault.go","line":88}`}},
		}}
	}

	runner := runnerFor(t, inner, &toolUsingPattern{})
	spec := Spec{Pattern: "tooluser", MaxSteps: 2, OutputSchema: finding(), Parser: true}

	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if !strings.Contains(saw, "vault.go:88") {
		t.Errorf("the parser never saw the tool's output, which is where the answer was:\n%s", saw)
	}
	if !strings.Contains(saw, "Let me look deeper") {
		t.Errorf("the parser never saw what the assistant said:\n%s", saw)
	}
	if result.Structured == nil {
		t.Error("Structured = nil")
	}
}

func TestATranscriptLeavesOutWhatNobodySaid(t *testing.T) {
	got := transcript([]Message{
		{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "why?"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "search"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "vault.go:88"}}},
	})

	want := "user: why?\n\ntool: vault.go:88"
	if got != want {
		t.Errorf("transcript() =\n%q\nwant\n%q — a message with no text is not a line", got, want)
	}
}

type toolUsingPattern struct{}

func (*toolUsingPattern) Name() PatternName { return "tooluser" }

func (*toolUsingPattern) Run(ctx context.Context, in Input) (Result, error) {
	msgs := []Message{
		{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "Why is the test failing?"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "search"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "vault.go:88 renewLease returns nil"}}},
		{Role: RoleAssistant, Content: []Content{{Type: ContentText, Text: "Let me look deeper."}}},
	}
	if _, err := in.Model.Complete(ctx, Request{Messages: msgs}); err != nil {
		return Result{}, err
	}
	return Result{Output: "Let me look deeper.", Messages: msgs, Steps: 1}, nil
}
