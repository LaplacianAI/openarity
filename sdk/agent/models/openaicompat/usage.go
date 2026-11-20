package openaicompat

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/openai/openai-go/v3"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// writeFields are the names gateways give the tokens a call wrote to the
// provider's cache, nearest first: in prompt_tokens_details, then beside it.
var writeFields = []string{"cache_creation_tokens", "cache_creation_input_tokens", "cache_write_tokens"}

// writesFlag is the field a gateway sets to say whether prompt_tokens holds
// the writes.
const writesFlag = "cache_creation_in_prompt"

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
	details := u.PromptTokensDetails.JSON.ExtraFields
	top := u.JSON.ExtraFields

	writes := firstCount(details, top)
	if writes == 0 {
		return out
	}
	flag := extra(details, writesFlag)
	if flag == "" {
		flag = extra(top, writesFlag)
	}
	if writesOutsidePrompt(flag, out.InputTokens, out.CachedInputTokens, writes) {
		out.InputTokens += writes
	} else {
		// Inside, writes are part of what prompt_tokens counted fresh; a
		// gateway saying more than that does not make them more.
		writes = min(writes, max(0, out.InputTokens-out.CachedInputTokens))
	}
	out.CacheWriteTokens = writes
	return out
}

// writesOutsidePrompt is whether prompt_tokens leaves the writes out. A
// gateway that says so is believed. One that does not is judged by
// arithmetic: writes counted inside would leave prompt_tokens at least cached
// plus writes, so a smaller one cannot hold them. A gateway that leaves them
// out with more fresh input than writes is read as holding them, which counts
// short, never twice.
func writesOutsidePrompt(flag string, prompt, cached, writes int) bool {
	if inside, ok := boolean(flag); ok {
		return !inside
	}
	return prompt < cached+writes
}

// boolean reads a flag sent as true or false, or as either in quotes.
func boolean(raw string) (bool, bool) {
	switch strings.Trim(raw, `"`) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
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

// maxCount is the largest count taken: past it a float64 no longer holds
// every whole number, and no call writes that much.
const maxCount = 1 << 53

// count is a whole number, written as 40188, 40188.0 or 4.0188e4, or zero
// for a string, a fraction or a number too large to be a count. A negative
// comes back as it is, and firstCount, taking only one above zero, skips it.
func count(raw string) int {
	var v any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return 0
	}
	num, _ := v.(json.Number) // anything else, a quoted number among them, fails Float64
	f, err := num.Float64()
	if err != nil || f > maxCount || f != math.Trunc(f) {
		return 0
	}
	return int(f)
}
