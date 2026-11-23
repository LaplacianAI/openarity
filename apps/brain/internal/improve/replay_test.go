package improve

import (
	"strings"
	"testing"
)

// A replay is judged as its run would be, with the replay's steps and
// answer in place of the run's, and the paragraph that judges a call not
// made as made.
func TestAReplayIsJudgedAsItsRun(t *testing.T) {
	w := newWorld(t)
	s := w.session(t)
	w.say(t, s, "please refund order 42")
	run := w.finished(t, s, "1 hour")
	w.model.replies = []reply{{text: goodAnswer}}

	j, err := w.checker.JudgeReplay(t.Context(), run, Replay{
		Trace:  []string{"  the agent calls orders__refund({\"id\":42}) [not made in the replay]"},
		Answer: "Refunded order 42.",
		Ending: "status succeeded; 2 steps",
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.Verdict != "done" || j.Usage.InputTokens == 0 {
		t.Errorf("judged %+v", j)
	}
	req := w.model.asked[0]
	var system string
	for _, c := range req.System {
		system += c.Text
	}
	user := req.Messages[0].Content[0].Text
	if !strings.Contains(system, "This run is a replay") {
		t.Error("the judge was not told the run is a replay")
	}
	for _, want := range []string{"refund order 42", "orders__refund", "not made in the replay", "Refunded order 42.", "please refund order 42"} {
		if !strings.Contains(user, want) {
			t.Errorf("the judge did not read %q", want)
		}
	}
	if strings.Contains(user, "order 42: paid") || strings.Contains(user, "Done, refunded.") {
		t.Error("the judge read the past run's own steps or answer")
	}
}

// An answer the judge cannot give a usable verdict in is cant_tell, with
// why, not a failure to try again.
func TestAnUnusableJudgementOfAReplayIsCantTell(t *testing.T) {
	w := newWorld(t)
	run := w.finished(t, w.session(t), "1 hour")
	w.model.replies = []reply{{text: `{"verdict":"maybe"}`}}
	j, err := w.checker.JudgeReplay(t.Context(), run, Replay{Answer: "x"})
	if err != nil || j.Verdict != verdictCantTell || j.Why == "" {
		t.Errorf("judged %+v, %v", j, err)
	}
}
