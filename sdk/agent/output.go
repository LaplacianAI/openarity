package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const parserSystem = "You turn a transcript into structured data. It is a run that has " +
	"finished: what the user asked, what the assistant said, and what its tools returned. " +
	"Fill every field the schema requires from anything in it — a tool's output counts as " +
	"much as the assistant's own words. Take the values from the transcript and do not " +
	"invent any; leave an optional field out rather than guessing at it."

type OutputSchema struct {
	Name        string
	Description string
	JSON        json.RawMessage
	Strict      bool
}

func (o *OutputSchema) validate() error {
	if o.Name == "" {
		return errors.New("Spec.OutputSchema has no Name, and the gateway requires one to name the schema")
	}
	if len(o.JSON) == 0 {
		return fmt.Errorf("Spec.OutputSchema %q has no JSON", o.Name)
	}
	if !json.Valid(o.JSON) {
		return fmt.Errorf("Spec.OutputSchema %q has a JSON schema that is not valid JSON", o.Name)
	}
	return nil
}

func checkOutput(spec Spec) error {
	if spec.OutputSchema == nil {
		if spec.Parser {
			return errors.New(
				"Spec.Parser is set but Spec.OutputSchema is nil, so there is no schema to parse into")
		}
		if spec.ParserModel.Name != "" {
			return errors.New(
				"Spec.ParserModel is set but Spec.OutputSchema is nil, so nothing would be parsed")
		}
		return nil
	}

	if !spec.Parser && spec.ParserModel.Name != "" {
		return errors.New(
			"Spec.ParserModel is set but Spec.Parser is false, so the model running the loop " +
				"would produce the structured output and the parser model would never be called")
	}

	return spec.OutputSchema.validate()
}

type outputClient struct {
	inner  ModelClient
	schema *OutputSchema
}

func (c *outputClient) Complete(ctx context.Context, req Request) (Response, error) {
	req.OutputSchema = c.schema
	return c.inner.Complete(ctx, req)
}

func (c *outputClient) Stream(ctx context.Context, req Request) (Stream, error) {
	req.OutputSchema = c.schema
	return c.inner.Stream(ctx, req)
}

func structured(answer string) json.RawMessage {
	for _, candidate := range candidates(answer) {
		if raw := json.RawMessage(candidate); json.Valid(raw) {
			return raw
		}
	}
	return nil
}

func candidates(answer string) []string {
	trimmed := strings.TrimSpace(answer)
	found := []string{trimmed}

	for rest := trimmed; ; {
		open := strings.Index(rest, "```")
		if open < 0 {
			return found
		}
		rest = rest[open+3:]

		if line := strings.IndexByte(rest, '\n'); line >= 0 &&
			!strings.Contains(strings.TrimSpace(rest[:line]), " ") {
			rest = rest[line+1:]
		}

		shut := strings.Index(rest, "```")
		if shut < 0 {
			return append(found, strings.TrimSpace(rest))
		}
		found = append(found, strings.TrimSpace(rest[:shut]))
		rest = rest[shut+3:]
	}
}

func answered(result Result) Result {
	result.Plain = result.Output
	if result.Structured != nil {
		result.Output = string(result.Structured)
	}
	return result
}

func parseIntoSchema(ctx context.Context, client ModelClient, model ModelRef,
	out *OutputSchema, answer string,
) (json.RawMessage, error) {
	resp, err := client.Complete(ctx, Request{
		Model:        model,
		System:       []Content{{Type: ContentText, Text: parserSystem}},
		Messages:     []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: answer}}}},
		OutputSchema: out,
	})
	if err != nil {
		return nil, fmt.Errorf("parsing the answer into %s: %w", out.Name, err)
	}
	return structured(resp.Message.Text()), nil
}

var _ ModelClient = (*outputClient)(nil)
