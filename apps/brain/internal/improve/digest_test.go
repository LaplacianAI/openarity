package improve

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// An answer becomes rows only for agents the tree holds, from the closed
// sets, with a target only when the agent held it, and each agent once.
func TestDigestRows(t *testing.T) {
	bot, other := uuid.New(), uuid.New()
	subjects := map[string]subject{
		"support-bot": {id: bot, held: held{skills: []string{"refunds"}, tools: []string{"orders__lookup"}}},
		"billing":     {id: other},
	}
	o := db.RunOutcome{RunID: uuid.New(), TeamID: uuid.New()}
	sig := func(part, target, cause string) signature {
		return signature{Part: part, Target: target, Cause: cause, Behaviour: "closed the ticket early"}
	}
	answer := digestAnswer{Agents: []agentDigest{
		{Agent: "support-bot", Summary: "asked to refund; closed first", Went: "wrong", Signature: sig("skill", "refunds", "skipped_step")},
		{Agent: "support-bot", Summary: "again", Went: "wrong", Signature: sig("instructions", "", "other")}, // twice
		{Agent: "billing", Summary: "fine", Went: "well", Signature: sig("tool", "made_up", "other")},        // target not held
		{Agent: "hub", Summary: "x", Went: "wrong", Signature: sig("pattern", "", "other")},                  // not a subject
		{Agent: "billing", Summary: "x", Went: "meh", Signature: sig("pattern", "", "other")},                // went not in set
	}}
	rows := answer.rows(o, subjects, true, "m", agent.Usage{InputTokens: 100, OutputTokens: 20})
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want support-bot and billing once each", rows)
	}
	if rows[0].AgentID != bot || rows[0].Target != "refunds" || rows[0].Cause != "skipped_step" || !rows[0].Outsider {
		t.Errorf("support-bot row = %+v", rows[0])
	}
	if rows[1].AgentID != other || rows[1].Target != "" {
		t.Errorf("billing row = %+v, want its made-up target blanked", rows[1])
	}
	if rows[0].InputTokens != 100 || rows[1].InputTokens != 0 {
		t.Errorf("the call's tokens must be counted once: %d, %d", rows[0].InputTokens, rows[1].InputTokens)
	}

	bad := digestAnswer{Agents: []agentDigest{{Agent: "support-bot", Summary: "x", Went: "wrong", Signature: sig("everything", "", "other")}}}
	if len(bad.rows(o, subjects, false, "m", agent.Usage{})) != 0 {
		t.Error("a part outside the set became a row")
	}
}

func TestTargetOf(t *testing.T) {
	h := held{skills: []string{"refunds"}, tools: []string{"orders__lookup", "web_search"}}
	for _, tc := range []struct {
		part, target, want string
	}{
		{"skill", "refunds", "refunds"},
		{"skill", "orders", ""},
		{"tool", "web_search", "web_search"},
		{"server", "orders", "orders"},
		{"instructions", "refunds", ""},
	} {
		if got := targetOf(signature{Part: tc.part, Target: tc.target}, h); got != tc.want {
			t.Errorf("targetOf(%s, %s) = %q, want %q", tc.part, tc.target, got, tc.want)
		}
	}
}

func TestJudgeModelOf(t *testing.T) {
	name, team := "small", "big"
	tokens := int32(1000)
	if m, ok := judgeModelOf(db.GetImprovementSettingsRow{JudgeModelName: &name, JudgeModelMaxTokens: &tokens, ModelName: &team, ModelMaxTokens: &tokens}); !ok || m.Name != "small" {
		t.Errorf("the judge's model must win: %+v", m)
	}
	if m, ok := judgeModelOf(db.GetImprovementSettingsRow{ModelName: &team, ModelMaxTokens: &tokens}); !ok || m.Name != "big" {
		t.Errorf("without a judge model, the team's: %+v", m)
	}
	if _, ok := judgeModelOf(db.GetImprovementSettingsRow{}); ok {
		t.Error("no model at all must say so")
	}
}

func TestAnAgentIsKnownWithOrWithoutTheMark(t *testing.T) {
	for in, want := range map[string]string{
		"support-bot":      "support-bot",
		"| support-bot":    "support-bot",
		"  |support-bot  ": "support-bot",
		"|":                "",
	} {
		if got := agentKey(in); got != want {
			t.Errorf("agentKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnlyACancelLeavesNothingToGiveUpOn(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"worked":    {nil, true},
		"cancelled": {fmt.Errorf("asking m: %w", context.Canceled), true},
		"timed out": {fmt.Errorf("asking m: %w", context.DeadlineExceeded), false},
		"a 503":     {errors.New("503"), false},
	} {
		if got := stopped(ctx, tc.err); got != tc.want {
			t.Errorf("%s: stopped = %v", name, got)
		}
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if !stopped(gone, errors.New("503")) {
		t.Error("a failure after shutdown was given up on")
	}
}
