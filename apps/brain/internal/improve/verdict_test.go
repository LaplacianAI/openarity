package improve

import (
	"testing"

	"github.com/google/uuid"
)

// A member's word outranks the judge; what people wrote next never decides
// on its own, since anyone can write in a conversation.
func TestEffective(t *testing.T) {
	for _, tc := range []struct {
		verdict, implicit string
		up, down          int32
		want              Effect
	}{
		{verdictDone, implicitNone, 0, 1, Bad},
		{verdictWrong, implicitNone, 1, 0, Good},
		{verdictDone, implicitNone, 3, 1, Bad},
		{verdictDone, implicitNone, 0, 0, Good},
		{verdictPartly, implicitNone, 0, 0, Bad},
		{verdictNotDone, implicitNone, 0, 0, Bad},
		{verdictWrong, implicitNone, 0, 0, Bad},
		{verdictCantTell, implicitNone, 0, 0, Neither},
		// Channel text changes nothing by itself.
		{verdictDone, implicitCorrection, 0, 0, Good},
		{verdictDone, implicitRepeat, 0, 0, Good},
		{verdictWrong, implicitThanks, 0, 0, Bad},
		{verdictCantTell, implicitThanks, 0, 0, Neither},
	} {
		if got := Effective(tc.verdict, tc.implicit, tc.up, tc.down); got != tc.want {
			t.Errorf("Effective(%s, %s, %d up, %d down) = %s, want %s", tc.verdict, tc.implicit, tc.up, tc.down, got, tc.want)
		}
	}
}

// The sample is the same whenever it is asked, and holds its share.
func TestSampled(t *testing.T) {
	id := uuid.New()
	first := Sampled(id, 50)
	for range 5 {
		if Sampled(id, 50) != first {
			t.Fatal("the same run sampled two ways")
		}
	}
	in := 0
	const n = 10000
	for range n {
		if Sampled(uuid.New(), 10) {
			in++
		}
	}
	if share := float64(in) / n; share < 0.09 || share > 0.11 {
		t.Errorf("a 10%% share sampled %.3f", share)
	}
	for range 200 {
		id := uuid.New()
		if Sampled(id, 0) || !Sampled(id, 100) {
			t.Fatal("0 must sample nothing and 100 everything")
		}
	}
}

func TestDigestFor(t *testing.T) {
	for _, tc := range []struct {
		e       Effect
		sampled bool
		want    string
	}{
		{Bad, false, digestPending},
		{Good, true, digestPending},
		{Good, false, digestSkipped},
		{Neither, true, digestSkipped},
	} {
		if got := digestFor(tc.e, tc.sampled); got != tc.want {
			t.Errorf("digestFor(%s, %v) = %s, want %s", tc.e, tc.sampled, got, tc.want)
		}
	}
}

func TestOutcomeAnswerCheck(t *testing.T) {
	ok := outcomeAnswer{Verdict: verdictDone, Implicit: implicitNone, Asked: []askedPart{{Part: "x", Delivered: "yes"}}}
	if err := ok.check(); err != nil {
		t.Errorf("a valid answer: %v", err)
	}
	for name, a := range map[string]outcomeAnswer{
		"verdict":   {Verdict: "great", Implicit: implicitNone},
		"implicit":  {Verdict: verdictDone, Implicit: "sarcasm"},
		"delivered": {Verdict: verdictDone, Implicit: implicitNone, Asked: []askedPart{{Part: "x", Delivered: "maybe"}}},
	} {
		if err := a.check(); err == nil {
			t.Errorf("%s outside its set was allowed", name)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("  héllo world  ", 5); got != "héll…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip = %q", got)
	}
}

func TestADigestInputRoundTrips(t *testing.T) {
	id := uuid.New()
	got, round, err := parseDigestInput(digestInput(id, 7))
	if err != nil || got != id || round != 7 {
		t.Errorf("parsed %s %d %v", got, round, err)
	}
	for _, bad := range []string{"", id.String(), "nope:1", id.String() + ":x"} {
		if _, _, err := parseDigestInput(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAFenceIsStripped(t *testing.T) {
	for in, want := range map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}```":       `{"a":1}`,
		"\t{\"a\":1}\n":           `{"a":1}`,
	} {
		if got := stripFence(in); got != want {
			t.Errorf("stripFence(%q) = %q", in, got)
		}
	}
}
