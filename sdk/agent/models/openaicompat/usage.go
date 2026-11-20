package openaicompat

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// writeFields are the names gateways give the tokens a call wrote to the
// provider's cache, nearest first: in prompt_tokens_details, then beside it.
var writeFields = []string{"cache_creation_tokens", "cache_creation_input_tokens", "cache_write_tokens"}

// usageOf is a call's usage, its cache writes counted as input when the
// gateway reported them outside prompt_tokens, as OmniRoute does: Anthropic
// pads a short prompt's write to its cache minimum, and OmniRoute keeps that
// out of prompt_tokens so a two-token prompt is not billed as a thousand.
// Left out, a call that missed the cache reads as a handful of tokens.
func usageOf(u openai.CompletionUsage) agent.Usage {
	out := agent.Usage{
		InputTokens:       int(u.PromptTokens),
		OutputTokens:      int(u.CompletionTokens),
		CachedInputTokens: int(u.PromptTokensDetails.CachedTokens),
	}
	details, top := u.PromptTokensDetails.JSON.ExtraFields, u.JSON.ExtraFields
	writes := firstCount(details, top)
	if writes == 0 {
		return out
	}
	out.CacheWriteTokens = writes
	if outside(extra(details, "cache_creation_in_prompt"), out, writes) {
		out.InputTokens += writes
	}
	return out
}

// outside is whether the writes are missing from prompt_tokens. A gateway
// that says so is believed. One that does not is judged by arithmetic:
// writes counted inside would leave prompt_tokens at least cached plus
// writes, so a smaller one cannot hold them. A gateway that leaves them out
// with more fresh input than writes is read as holding them, which counts
// short, never twice.
func outside(flag string, u agent.Usage, writes int) bool {
	switch flag {
	case "false":
		return true
	case "true":
		return false
	}
	return u.InputTokens < u.CachedInputTokens+writes
}

// firstCount is the first write count above zero under any of writeFields,
// in details before the usage itself.
func firstCount[F interface{ Raw() string }](details, top map[string]F) int {
	for _, fields := range []map[string]F{details, top} {
		for _, name := range writeFields {
			if n := count(extra(fields, name)); n > 0 {
				return n
			}
		}
	}
	return 0
}

// count is a whole number of tokens, or zero for anything that is not one.
func count(raw string) int {
	var n int
	if raw == "" || json.Unmarshal([]byte(raw), &n) != nil {
		return 0
	}
	return n
}
