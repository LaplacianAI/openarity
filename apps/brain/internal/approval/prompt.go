package approval

import (
	"fmt"
	"strings"
	"time"
)

// Prompt is what a conversation is sent when a run in it waits for its
// person: the requests the agent's words are about, for a platform to draw
// its buttons from. The runtime writes it with the prompt and the gateway
// reads it back, so it is stored as JSON.
type Prompt struct {
	Requests []PromptRequest `json:"requests"`
}

// PromptRequest is one request of a prompt. Ask is whether the chat asks
// its person to answer it there (AsksInChat): a request it does not is only
// said to have gone to an approver, and carries no code a chat could use.
type PromptRequest struct {
	ID        string    `json:"id"`
	Code      string    `json:"code,omitempty"`
	Tool      string    `json:"tool"`
	Summary   string    `json:"summary"`
	ExpiresAt time.Time `json:"expires_at"`
	Ask       bool      `json:"ask"`
}

// ToolName is a tool as a person reads it: "create_issue on github" for a
// server's, a built-in's own name alone.
func ToolName(tool string) string {
	server, name, ok := strings.Cut(tool, "__")
	if !ok || server == "" {
		return tool
	}
	return name + " on " + server
}

// Footer is the brain's lines for each request under what the agent said:
// what it is, how to answer it here or that an approver was asked, how long
// it waits, then the call's own arguments, indented. The words above it are
// the agent's and can misdescribe the call; these never are, so they cannot
// be talked into a different code. The summary is the stored call's, already
// cleaned of control and format characters and cut short (Summary).
func Footer(reqs []PromptRequest, now time.Time) string {
	lines := make([]string, 0, len(reqs))
	for _, r := range reqs {
		how := "sent to an approver, who answers on the dashboard"
		if r.Ask {
			how = fmt.Sprintf("reply yes %s or no %s", r.Code, r.Code)
		}
		lines = append(lines, fmt.Sprintf("%s · %s · %s", ToolName(r.Tool), how, Left(r.ExpiresAt, now)))
		for _, l := range strings.Split(r.Summary, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, "    "+l)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// Left is how long a request still waits, as a person reads it.
func Left(expires, now time.Time) string {
	d := expires.Sub(now)
	switch {
	case d <= 0:
		return "expired"
	case d < 2*time.Minute:
		return "under a minute left"
	case d < time.Hour:
		return fmt.Sprintf("%d min left", int((d+time.Minute-1)/time.Minute))
	}
	mins := int((d + time.Minute - 1) / time.Minute)
	if mins%60 == 0 {
		return fmt.Sprintf("%d h left", mins/60)
	}
	return fmt.Sprintf("%d h %d min left", mins/60, mins%60)
}

// PromptText is the whole prompt: what the agent said, or a plain sentence
// when it said nothing, then the footer.
func PromptText(said string, reqs []PromptRequest, now time.Time) string {
	said = strings.TrimSpace(said)
	if said == "" {
		said = "I'd like to make a call that needs approval first."
	}
	return said + "\n\n" + Footer(reqs, now)
}
