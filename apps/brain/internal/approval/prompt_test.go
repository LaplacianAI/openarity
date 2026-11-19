package approval

import (
	"testing"
	"time"
)

func TestTheFooterSaysHowToAnswerOnlyWhereItCanBe(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	got := Footer([]PromptRequest{
		{Code: "7K2F", Tool: "github__create_issue", ExpiresAt: now.Add(60 * time.Minute), Ask: true},
		{Code: "9ABC", Tool: "web_fetch", ExpiresAt: now.Add(90 * time.Minute)},
	}, now)
	want := "create_issue on github · reply yes 7K2F or no 7K2F · 1 h left\n" +
		"web_fetch · sent to an approver · 1 h 30 min left"
	if got != want {
		t.Errorf("Footer =\n%s\nwant\n%s", got, want)
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
