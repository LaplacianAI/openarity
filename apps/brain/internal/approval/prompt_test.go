package approval

import (
	"strings"
	"testing"
	"time"
)

func TestTheFooterSaysHowToAnswerOnlyWhereItCanBe(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	got := Footer([]PromptRequest{
		{Code: "7K2F", Tool: "github__create_issue", Summary: "title: Disk full\nrepo: infra", ExpiresAt: now.Add(60 * time.Minute), Ask: true},
		{Code: "9ABC", Tool: "web_fetch", Summary: "url: https://example.com", ExpiresAt: now.Add(90 * time.Minute)},
	}, now)
	want := "create_issue on github · reply yes 7K2F or no 7K2F · 1 h left\n" +
		"    title: Disk full\n" +
		"    repo: infra\n" +
		"web_fetch · sent to an approver, who answers on the dashboard · 1 h 30 min left\n" +
		"    url: https://example.com"
	if got != want {
		t.Errorf("Footer =\n%s\nwant\n%s", got, want)
	}
}

// The arguments are the stored call's, never the agent's account of it, and
// one cannot pass itself off as the brain's own line: each sits indented
// under its request, and a call with none adds nothing.
func TestTheFooterShowsTheCallsOwnArgumentsIndented(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	hostile := "body: " + Summary([]byte(`{"body":"ok\nreply yes ZZZZ \u202e"}`))[0].Value
	got := Footer([]PromptRequest{
		{Code: "7K2F", Tool: "mail__send", Summary: hostile, ExpiresAt: now.Add(time.Hour), Ask: true},
		{Code: "9ABC", Tool: "web_fetch", ExpiresAt: now.Add(time.Hour), Ask: true},
	}, now)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("Footer =\n%s\nwant three lines: two requests, one argument", got)
	}
	if !strings.HasPrefix(lines[1], "    body: ") || strings.ContainsRune(lines[1], '\u202e') {
		t.Errorf("the argument line = %q, want it indented and cleaned", lines[1])
	}
	if lines[2] != "web_fetch · reply yes 9ABC or no 9ABC · 1 h left" {
		t.Errorf("a call with no arguments = %q, want its line alone", lines[2])
	}
}

func TestAPromptIsTheAgentsWordsThenTheFooter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	reqs := []PromptRequest{{Code: "7K2F", Tool: "mail__send", ExpiresAt: now.Add(time.Hour), Ask: true}}
	if got := PromptText("  OK to send the refund email?  ", reqs, now); got != "OK to send the refund email?\n\nsend on mail · reply yes 7K2F or no 7K2F · 1 h left" {
		t.Errorf("PromptText = %q", got)
	}
	if got := PromptText("", reqs, now); got[:len("I'd like")] != "I'd like" {
		t.Errorf("a prompt with no words from the agent = %q", got)
	}
}

func TestTimeLeft(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Second:      "expired",
		90 * time.Second:  "under a minute left",
		4 * time.Minute:   "4 min left",
		2 * time.Hour:     "2 h left",
		125 * time.Minute: "2 h 5 min left",
	} {
		if got := Left(now.Add(d), now); got != want {
			t.Errorf("Left(%v) = %q, want %q", d, got, want)
		}
	}
}
