// Package openaicompat talks to anything speaking the OpenAI chat completions
// API: a LiteLLM proxy, an OmniRoute gateway, a provider directly.
//
// It is the only package in this module with a third-party dependency, and that
// is the point — agent.ModelClient is what keeps openai-go's types out of the
// loop, the Spec and the brain.
package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Client is safe for concurrent use.
type Client struct{ oai openai.Client }

// New points a client at a base URL ending in /v1.
//
// The gateway is a deployment choice rather than a code path: LiteLLM,
// OmniRoute and the providers themselves all answer the same endpoint, so there
// is nothing here to switch on.
func New(baseURL, apiKey string, opts ...option.RequestOption) *Client {
	all := append([]option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	}, opts...)
	return &Client{oai: openai.NewClient(all...)}
}

func (c *Client) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	params, err := toParams(req)
	if err != nil {
		return agent.Response{}, err
	}

	completion, err := c.oai.Chat.Completions.New(ctx, params)
	if err != nil {
		return agent.Response{}, err
	}
	return fromCompletion(*completion)
}

func (c *Client) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	params, err := toParams(req)
	if err != nil {
		return nil, err
	}

	// Without this the final chunk carries no usage, and every run reports zero
	// tokens — which looks like a free model rather than a missing field.
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	return &stream{raw: c.oai.Chat.Completions.NewStreaming(ctx, params)}, nil
}

// stream turns chunks into text deltas and one final response.
//
// Tool calls arrive as fragments — the name in one chunk, the arguments JSON
// split across many more — and nothing can be dispatched until the last one
// lands. The accumulator does that reassembly, which is why this package exists
// rather than the loop parsing SSE itself.
type stream struct {
	raw *ssestream.Stream[openai.ChatCompletionChunk]
	acc openai.ChatCompletionAccumulator

	cur  agent.StreamEvent
	err  error
	done bool
}

func (s *stream) Next() bool {
	if s.done {
		return false
	}

	for s.raw.Next() {
		chunk := s.raw.Current()
		s.acc.AddChunk(chunk)

		// Chunks carrying only tool-call fragments or usage produce no delta.
		// Skipping them rather than emitting empty events keeps a consumer from
		// having to filter.
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			s.cur = agent.StreamEvent{Text: chunk.Choices[0].Delta.Content}
			return true
		}
	}

	if err := s.raw.Err(); err != nil {
		s.err = err
		return false
	}

	final, err := fromCompletion(s.acc.ChatCompletion)
	if err != nil {
		s.err = err
		return false
	}

	s.cur = agent.StreamEvent{Final: &final}
	s.done = true
	return true
}

func (s *stream) Event() agent.StreamEvent { return s.cur }
func (s *stream) Err() error               { return s.err }
func (s *stream) Close() error             { return s.raw.Close() }

func toParams(req agent.Request) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{Model: req.Model.Name}

	if req.Model.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(req.Model.MaxTokens))
	}
	if req.Model.Temperature != nil {
		params.Temperature = openai.Float(*req.Model.Temperature)
	}

	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if len(req.System) > 0 {
		parts, err := textParts(req.System)
		if err != nil {
			return params, err
		}
		msgs = append(msgs, openai.SystemMessage(parts))
	}

	for i, m := range req.Messages {
		converted, err := toMessage(m)
		if err != nil {
			return params, fmt.Errorf("message %d: %w", i, err)
		}
		msgs = append(msgs, converted)
	}
	params.Messages = msgs

	tools, err := toTools(req.Tools)
	if err != nil {
		return params, err
	}
	params.Tools = tools

	return params, nil
}

// textParts converts blocks that must be text — the system prompt and tool
// results. A blob in either is a caller mistake worth naming rather than
// dropping.
func textParts(blocks []agent.Content) ([]openai.ChatCompletionContentPartTextParam, error) {
	parts := make([]openai.ChatCompletionContentPartTextParam, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != agent.ContentText {
			return nil, fmt.Errorf("a %s block cannot be sent here; only text can", b.Type)
		}
		part := openai.ChatCompletionContentPartTextParam{Text: b.Text}
		if b.Cacheable {
			markCacheable(&part)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// markCacheable attaches Anthropic's cache_control to a content part.
//
// There is no field for it in the OpenAI schema; a gateway forwarding to
// Anthropic reads it off the part as an extra key. Providers that do not
// understand it ignore it, so this is safe to send everywhere.
func markCacheable(part *openai.ChatCompletionContentPartTextParam) {
	part.SetExtraFields(map[string]any{
		"cache_control": map[string]string{"type": "ephemeral"},
	})
}

func toMessage(m agent.Message) (openai.ChatCompletionMessageParamUnion, error) {
	switch m.Role {
	case agent.RoleSystem:
		parts, err := textParts(m.Content)
		if err != nil {
			return openai.ChatCompletionMessageParamUnion{}, err
		}
		return openai.SystemMessage(parts), nil

	case agent.RoleUser:
		parts, err := userParts(m.Content)
		if err != nil {
			return openai.ChatCompletionMessageParamUnion{}, err
		}
		return openai.UserMessage(parts), nil

	case agent.RoleTool:
		parts, err := textParts(m.Content)
		if err != nil {
			return openai.ChatCompletionMessageParamUnion{}, err
		}
		return openai.ToolMessage(parts, m.ToolCallID), nil

	case agent.RoleAssistant:
		return toAssistant(m)

	default:
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("unknown role %q", m.Role)
	}
}

// toAssistant rebuilds a turn the model produced, tool calls included. Replaying
// them matters: a tool result whose call is missing from the history is rejected
// by the provider rather than ignored.
func toAssistant(m agent.Message) (openai.ChatCompletionMessageParamUnion, error) {
	msg := openai.ChatCompletionAssistantMessageParam{}

	if text := m.Text(); text != "" {
		msg.Content.OfString = openai.String(text)
	}

	for _, call := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: call.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			},
		})
	}

	return openai.ChatCompletionMessageParamUnion{OfAssistant: &msg}, nil
}

func userParts(blocks []agent.Content) ([]openai.ChatCompletionContentPartUnionParam, error) {
	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(blocks))

	for _, b := range blocks {
		switch b.Type {
		case agent.ContentText:
			text := openai.ChatCompletionContentPartTextParam{Text: b.Text}
			if b.Cacheable {
				markCacheable(&text)
			}
			parts = append(parts, openai.ChatCompletionContentPartUnionParam{OfText: &text})

		case agent.ContentImage:
			if b.Blob == nil {
				return nil, fmt.Errorf("an image block carried no blob")
			}
			parts = append(parts, openai.ChatCompletionContentPartUnionParam{
				OfImageURL: &openai.ChatCompletionContentPartImageParam{
					ImageURL: openai.ChatCompletionContentPartImageImageURLParam{URL: dataURI(b.Blob)},
				},
			})

		case agent.ContentFile:
			if b.Blob == nil {
				return nil, fmt.Errorf("a file block carried no blob")
			}
			parts = append(parts, openai.ChatCompletionContentPartUnionParam{
				OfFile: &openai.ChatCompletionContentPartFileParam{
					File: openai.ChatCompletionContentPartFileFileParam{
						FileData: openai.String(dataURI(b.Blob)),
						Filename: openai.String(b.Blob.Name),
					},
				},
			})

		default:
			return nil, fmt.Errorf("unknown content type %q", b.Type)
		}
	}

	return parts, nil
}

// dataURI is how bytes reach a provider that only accepts addresses. Base64
// inflates by 4/3, so a 5 MB photo travels as roughly 6.7 MB — the size cap
// belongs where the attachment is admitted, not here.
func dataURI(b *agent.Blob) string {
	return "data:" + b.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b.Data)
}

func toTools(tools []agent.Tool) ([]openai.ChatCompletionToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		def := shared.FunctionDefinitionParam{Name: t.Name}
		if t.Description != "" {
			def.Description = openai.String(t.Description)
		}

		// Decoded rather than passed through: the field is a map on the wire,
		// and a schema that is not an object would otherwise be rejected by the
		// provider with an error naming neither the tool nor the reason.
		if len(t.Schema) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(t.Schema, &schema); err != nil {
				return nil, fmt.Errorf("tool %q has an unparsable schema: %w", t.Name, err)
			}
			def.Parameters = schema
		}

		out = append(out, openai.ChatCompletionFunctionTool(def))
	}
	return out, nil
}

func fromCompletion(c openai.ChatCompletion) (agent.Response, error) {
	if len(c.Choices) == 0 {
		return agent.Response{}, fmt.Errorf("the response carried no choices")
	}
	choice := c.Choices[0]

	msg := agent.Message{Role: agent.RoleAssistant}
	if choice.Message.Content != "" {
		msg.Content = []agent.Content{{Type: agent.ContentText, Text: choice.Message.Content}}
	}

	for _, call := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}

	return agent.Response{
		Message: msg,
		Finish:  finishReason(choice.FinishReason),
		Usage: agent.Usage{
			InputTokens:       int(c.Usage.PromptTokens),
			OutputTokens:      int(c.Usage.CompletionTokens),
			CachedInputTokens: int(c.Usage.PromptTokensDetails.CachedTokens),
		},
	}, nil
}

// finishReason maps the wire's vocabulary onto ours.
//
// An unrecognised value becomes FinishStop rather than an error: gateways
// invent reasons, and refusing a completed turn over a label we have not seen
// throws away an answer the model actually produced.
func finishReason(reason string) agent.FinishReason {
	switch reason {
	case "tool_calls", "function_call":
		return agent.FinishToolCalls
	case "length", "max_tokens":
		return agent.FinishLength
	case "content_filter":
		return agent.FinishFilter
	default:
		return agent.FinishStop
	}
}

// The compiler is what keeps this honest: the loop only ever sees the
// interface, so a signature drifting here is a build failure rather than a
// runtime surprise.
var _ agent.ModelClient = (*Client)(nil)
