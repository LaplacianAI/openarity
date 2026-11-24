package replay

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
)

// judgeTrace is the try's steps as the judge reads a run's, each call saying
// how it was served.
func judgeTrace(res runtime.Replayed) []string {
	lines := []string{fmt.Sprintf("the agent started with tools [%s]", strings.Join(res.Tools, ", "))}
	for _, c := range res.Calls {
		how := ""
		switch c.How {
		case runtime.ServedRecorded:
			how = " [as in the past run]"
		case runtime.ServedLive:
			how = " [run in the replay]"
		case runtime.ServedNotMade:
			lines = append(lines, fmt.Sprintf("  the agent calls %s(%s) [not made in the replay]", c.Name, clip(c.Arguments, 300)))
			continue
		}
		lines = append(lines, fmt.Sprintf("  the agent calls %s(%s)%s", c.Name, clip(c.Arguments, 300), how))
		if c.Error != "" {
			lines = append(lines, fmt.Sprintf("  %s failed: %s", c.Name, clip(c.Error, 300)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s returned: %s", c.Name, clip(c.Output, 300)))
		}
	}
	return lines
}

func ending(res runtime.Replayed) string {
	return fmt.Sprintf("status succeeded; %d steps; tokens in %d, out %d", res.Steps, res.Usage.InputTokens, res.Usage.OutputTokens)
}

// traceOf is the try's calls as its row keeps them, a JSON list: whole
// when they fit in traceBytes, else the first of them that do, so a long try
// keeps how it began.
func traceOf(calls []runtime.Served) []byte {
	if len(calls) == 0 {
		// Never JSON null: a reader counts the calls.
		return []byte("[]")
	}
	kept := calls
	for {
		b, err := json.Marshal(kept)
		if err == nil && len(b) <= traceBytes {
			return b
		}
		if len(kept) == 0 {
			return []byte("[]")
		}
		kept = kept[:len(kept)*3/4]
	}
}

// clip cuts s to at most n bytes, at the start of a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
