package approval

import "testing"

func TestWhoMayAnswer(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name                                  string
		own, requireOther, mayDecide, maySelf bool
		want                                  bool
	}{
		{name: "their own, when they may approve their own", own: true, maySelf: true, want: true},
		{name: "their own, when they may not", own: true},
		{name: "anyone's, for someone who decides the team's", mayDecide: true, want: true},
		{name: "someone else's, approving only their own", maySelf: true},
		{name: "their own, where a rule wants someone else", own: true, requireOther: true, mayDecide: true, maySelf: true},
		{name: "someone else's under that rule, for a decider", requireOther: true, mayDecide: true, want: true},
	} {
		if got := MayAnswer(c.own, c.requireOther, c.mayDecide, c.maySelf); got != c.want {
			t.Errorf("%s: MayAnswer = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTheChatAsksOnlyWhoMayApproveTheirOwn(t *testing.T) {
	t.Parallel()

	if !AsksInChat(false, true) {
		t.Error("someone who may approve their own is not asked in the chat")
	}
	if AsksInChat(true, true) {
		t.Error("a request a rule wants someone else to answer is asked in the chat")
	}
	if AsksInChat(false, false) {
		t.Error("someone who may not approve their own is asked in the chat")
	}
}
