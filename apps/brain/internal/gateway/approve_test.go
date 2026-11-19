package gateway

import (
	"crypto/sha256"
	"testing"
)

func TestOnlyAWholeYesOrNoIsATypedAnswer(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		text, trigger string
		want          Decision
		code          string
		ok            bool
	}{
		{text: "yes", want: Approve, ok: true},
		{text: "Y", want: Approve, ok: true},
		{text: "no.", want: Decline, ok: true},
		{text: "n", want: Decline, ok: true},
		{text: "yes 7k2f", want: Approve, code: "7K2F", ok: true},
		{text: "  No   9ABC! ", want: Decline, code: "9ABC", ok: true},
		{text: "@openarity yes 7K2F", want: Approve, code: "7K2F", ok: true},
		{text: "<@U0BOT> yes", want: Approve, ok: true},
		{text: "oa, yes 7K2F", trigger: "oa", want: Approve, code: "7K2F", ok: true},
		{text: "sure, yes 7K2F"},
		{text: "yes please"},
		{text: "yes 7K2"},
		{text: "yes 7K2F now"},
		{text: "yeah"},
		{text: "oa", trigger: "oa"},
		{text: ""},
	} {
		d, code, ok := typed(c.text, c.trigger)
		if ok != c.ok || d != c.want || code != c.code {
			t.Errorf("typed(%q, %q) = %q %q %v, want %q %q %v", c.text, c.trigger, d, code, ok, c.want, c.code, c.ok)
		}
	}
}

func TestAPollVoteIsMatchedByTheHashOfItsOption(t *testing.T) {
	t.Parallel()

	approve := sha256.Sum256([]byte("Approve 7K2F"))
	decline := sha256.Sum256([]byte("Decline 7K2F"))
	other := sha256.Sum256([]byte("Approve 9ABC"))

	if d, ok := pollChoice("7K2F", [][]byte{approve[:]}); !ok || d != Approve {
		t.Errorf("a vote to approve = %q %v", d, ok)
	}
	if d, ok := pollChoice("7K2F", [][]byte{decline[:]}); !ok || d != Decline {
		t.Errorf("a vote to decline = %q %v", d, ok)
	}
	if _, ok := pollChoice("7K2F", [][]byte{other[:]}); ok {
		t.Error("a vote on another request's poll matched this one")
	}
	if _, ok := pollChoice("7K2F", nil); ok {
		t.Error("an empty vote matched")
	}
}
