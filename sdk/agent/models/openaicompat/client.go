// Package openaicompat talks to anything speaking the OpenAI chat completions
// API: a LiteLLM proxy, an OmniRoute gateway, a provider directly.
//
// It is the only package in this module with a third-party dependency, and that
// is the point — agent.ModelClient is what keeps openai-go's types out of the
// pattern, the Spec and the brain.
package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

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
// rather than the pattern parsing SSE itself.
type stream struct {
	raw *ssestream.Stream[openai.ChatCompletionChunk]
	acc openai.ChatCompletionAccumulator

	// Tracked from the chunks alongside the accumulator rather than trusting
	// it alone. The accumulator discards what it holds when a chunk's id
	// changes mid-stream, and a gateway that renumbers — some do, between the
	// content and the trailing usage chunk — would otherwise hand back an
	// empty answer, no tokens, and no sign anything went wrong.
	text   strings.Builder
	usage  agent.Usage
	finish string

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
		s.observe(chunk)

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

	// An error here is not always fatal. The accumulator having nothing usable
	// matters only if the chunks had nothing either; when they carried an
	// answer, that answer is what the caller is owed, and repair rebuilds it
	// from the zero response fromCompletion returned.
	final, err := fromCompletion(s.acc.ChatCompletion)
	if err != nil && s.text.Len() == 0 && s.finish == "" {
		s.err = err
		return false
	}
	s.repair(&final)

	s.cur = agent.StreamEvent{Final: &final}
	s.done = true
	return true
}

// observe records what each chunk carried, so nothing depends on the
// accumulator still holding it at the end.
func (s *stream) observe(chunk openai.ChatCompletionChunk) {
	if u := usageOf(chunk.Usage); u != (agent.Usage{}) {
		s.usage = u
	}
	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	s.text.WriteString(choice.Delta.Content)
	if choice.FinishReason != "" {
		s.finish = choice.FinishReason
	}
}

// repair replaces whatever the accumulator lost. What was tracked here is
// preferred rather than used as a fallback: the accumulator can come back with
// part of an answer as easily as none of it, and half a reply is worse than an
// empty one because nothing about it looks wrong.
//
// The accumulator is still what assembles tool calls, whose arguments arrive
// as fragments that mean nothing until they are whole.
func (s *stream) repair(final *agent.Response) {
	final.Message.Role = agent.RoleAssistant
	if s.text.Len() > 0 {
		final.Message.Content = []agent.Content{{Type: agent.ContentText, Text: s.text.String()}}
	}
	if s.usage != (agent.Usage{}) {
		final.Usage = s.usage
	}
	// Defence rather than a fixed bug: the accumulator was measured to keep the
	// finish reason across an id change even though it loses the text and the
	// usage. Kept because the chunks are authoritative and it costs nothing,
	// and because losing it is the worst of the three — no reason at all
	// converts to FinishStop, so a turn truncated at MaxTokens would read as a
	// turn that finished, and the loop would dispatch its half-written call.
	if s.finish != "" {
		final.Finish = finishReason(s.finish)
	}
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
		Usage:   usageOf(c.Usage),
	}, nil
}

func usageOf(u openai.CompletionUsage) agent.Usage {
	return agent.Usage{
		InputTokens:       int(u.PromptTokens),
		OutputTokens:      int(u.CompletionTokens),
		CachedInputTokens: int(u.PromptTokensDetails.CachedTokens),
	}
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

// the compiler is what keeps this honest: the pattern only ever sees the
// interface, so a signature drifting here is a build failure rather than a
// runtime surprise.
var _ agent.ModelClient = (*Client)(nil)
