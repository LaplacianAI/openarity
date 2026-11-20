package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// withUsage is a plain answer whose usage is exactly u.
func withUsage(u map[string]any) string {
	out, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "model": "probe",
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop",
		}},
		"usage": u,
	})
	return string(out)
}

func usage(prompt int, details, top map[string]any) map[string]any {
	u := map[string]any{"prompt_tokens": prompt, "completion_tokens": 7, "prompt_tokens_details": details}
	for k, v := range top {
		u[k] = v
	}
	return u
}

// A cache write is input the model read. A gateway that reports it outside
// prompt_tokens has it added back; one that counts it inside is left alone,
// so a write is never counted twice. The numbers in the first two are
// staging's: OmniRoute's own log for the same calls says in=149 with
// cache_create=40188, and in=21301 with cache_read=21246 and cache_create=8299.
func TestCacheWritesCountAsInputOnce(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		usage map[string]any
		want  agent.Usage
	}{
		"OmniRoute, a cache miss": {
			usage(149, map[string]any{"cached_tokens": 0, "cache_creation_tokens": 40188, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 40337, OutputTokens: 7, CacheWriteTokens: 40188},
		},
		"OmniRoute, a hit that also wrote": {
			usage(21301, map[string]any{"cached_tokens": 21246, "cache_creation_tokens": 8299, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 29600, OutputTokens: 7, CachedInputTokens: 21246, CacheWriteTokens: 8299},
		},
		"a gateway that says the writes are inside": {
			usage(40150, map[string]any{"cache_creation_tokens": 40000, "cache_creation_in_prompt": true}, nil),
			agent.Usage{InputTokens: 40150, OutputTokens: 7, CacheWriteTokens: 40000},
		},
		// The flag is believed over the arithmetic, both ways.
		"outside by the flag, with more fresh input than writes": {
			usage(50000, map[string]any{"cache_creation_tokens": 1000, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 51000, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		// Inside, writes are part of prompt_tokens, so never more than its
		// fresh part.
		"inside by the flag, though prompt_tokens is smaller": {
			usage(150, map[string]any{"cache_creation_tokens": 1000, "cache_creation_in_prompt": true}, nil),
			agent.Usage{InputTokens: 150, OutputTokens: 7, CacheWriteTokens: 150},
		},
		"inside by the flag, held to the fresh part after cached": {
			usage(1000, map[string]any{"cached_tokens": 900, "cache_creation_tokens": 500, "cache_creation_in_prompt": true}, nil),
			agent.Usage{InputTokens: 1000, OutputTokens: 7, CachedInputTokens: 900, CacheWriteTokens: 100},
		},
		"the flag in quotes": {
			usage(50000, map[string]any{"cache_creation_tokens": 1000, "cache_creation_in_prompt": "false"}, nil),
			agent.Usage{InputTokens: 51000, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"the flag beside the details": {
			usage(50000, map[string]any{"cache_creation_tokens": 1000}, map[string]any{"cache_creation_in_prompt": false}),
			agent.Usage{InputTokens: 51000, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"a null flag is no flag": {
			usage(150, map[string]any{"cache_creation_tokens": 1000, "cache_creation_in_prompt": nil}, nil),
			agent.Usage{InputTokens: 1150, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		// Without a flag, writes outside with more fresh input than writes are
		// read as inside: short, never twice.
		"no flag, outside but read as inside": {
			usage(50000, map[string]any{"cache_creation_tokens": 1000}, nil),
			agent.Usage{InputTokens: 50000, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"no flag, prompt exactly cached plus writes": {
			usage(40000, map[string]any{}, map[string]any{"cache_creation_input_tokens": 40000}),
			agent.Usage{InputTokens: 40000, OutputTokens: 7, CacheWriteTokens: 40000},
		},
		"no flag, prompt one short of cached plus writes": {
			usage(39999, map[string]any{}, map[string]any{"cache_creation_input_tokens": 40000}),
			agent.Usage{InputTokens: 79999, OutputTokens: 7, CacheWriteTokens: 40000},
		},
		"no flag, all of prompt cached and a write beside it": {
			usage(21246, map[string]any{"cached_tokens": 21246}, map[string]any{"cache_creation_input_tokens": 100}),
			agent.Usage{InputTokens: 21346, OutputTokens: 7, CachedInputTokens: 21246, CacheWriteTokens: 100},
		},
		"no flag, writes inside by arithmetic": {
			usage(40150, map[string]any{}, map[string]any{"cache_creation_input_tokens": 40000}),
			agent.Usage{InputTokens: 40150, OutputTokens: 7, CacheWriteTokens: 40000},
		},
		"no flag, writes outside by arithmetic": {
			usage(150, map[string]any{"cached_tokens": 0}, map[string]any{"cache_creation_input_tokens": 40000}),
			agent.Usage{InputTokens: 40150, OutputTokens: 7, CacheWriteTokens: 40000},
		},
		"the count in details under its long name": {
			usage(150, map[string]any{"cache_creation_input_tokens": 1000, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 1150, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"the count as cache_write_tokens": {
			usage(150, map[string]any{}, map[string]any{"cache_write_tokens": 1000}),
			agent.Usage{InputTokens: 1150, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"details win over the usage itself": {
			usage(150, map[string]any{"cache_creation_tokens": 1000, "cache_creation_in_prompt": false},
				map[string]any{"cache_creation_input_tokens": 9999}),
			agent.Usage{InputTokens: 1150, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"a zero first name gives way to the next": {
			usage(150, map[string]any{"cache_creation_tokens": 0, "cache_creation_in_prompt": false},
				map[string]any{"cache_creation_input_tokens": 1000}),
			agent.Usage{InputTokens: 1150, OutputTokens: 7, CacheWriteTokens: 1000},
		},
		"no writes": {
			usage(100, map[string]any{"cached_tokens": 80}, nil),
			agent.Usage{InputTokens: 100, OutputTokens: 7, CachedInputTokens: 80},
		},
		"a count that is not a number": {
			usage(150, map[string]any{"cache_creation_tokens": "40188", "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 150, OutputTokens: 7},
		},
		"a negative count": {
			usage(150, map[string]any{"cache_creation_tokens": -5, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 150, OutputTokens: 7},
		},
		"a fractional count": {
			usage(150, map[string]any{"cache_creation_tokens": 10.5, "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 150, OutputTokens: 7},
		},
		"a whole count written with a point": {
			usage(150, map[string]any{"cache_creation_tokens": json.Number("40188.0"), "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 40338, OutputTokens: 7, CacheWriteTokens: 40188},
		},
		"a whole count written with an exponent": {
			usage(150, map[string]any{"cache_creation_tokens": json.Number("4.0188e4"), "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 40338, OutputTokens: 7, CacheWriteTokens: 40188},
		},
		"a count too large to be one": {
			usage(150, map[string]any{"cache_creation_tokens": json.Number("1e30"), "cache_creation_in_prompt": false}, nil),
			agent.Usage{InputTokens: 150, OutputTokens: 7},
		},
		"a bad first name gives way to the next": {
			usage(150, map[string]any{"cache_creation_tokens": -5, "cache_creation_in_prompt": false},
				map[string]any{"cache_write_tokens": 40000}),
			agent.Usage{InputTokens: 40150, OutputTokens: 7, CacheWriteTokens: 40000},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c, _ := serve(t, answer(t, withUsage(tc.usage)))
			got, err := c.Complete(t.Context(), probeRequest())
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got.Usage != tc.want {
				t.Errorf("Usage = %+v, want %+v", got.Usage, tc.want)
			}
		})
	}
}

// A stream's usage arrives on its last chunk and is read the same way.
func TestAStreamCountsItsCacheWritesAsInput(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		idChunk("a", map[string]any{"role": "assistant", "content": "ok"}, "stop", nil),
		idChunk("a", map[string]any{}, "", usage(149,
			map[string]any{"cached_tokens": 0, "cache_creation_tokens": 40188, "cache_creation_in_prompt": false}, nil)),
	))
	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	_, final := drain(t, s)
	if final == nil {
		t.Fatal("no final response")
	}
	want := agent.Usage{InputTokens: 40337, OutputTokens: 7, CacheWriteTokens: 40188}
	if final.Usage != want {
		t.Errorf("Usage = %+v, want %+v", final.Usage, want)
	}
}
