package improve

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The judge's verdicts, and what the people's next messages said.
const (
	verdictDone     = "done"
	verdictPartly   = "partly"
	verdictNotDone  = "not_done"
	verdictWrong    = "wrong"
	verdictCantTell = "cant_tell"

	implicitNone       = "none"
	implicitCorrection = "correction"
	implicitRepeat     = "repeat"
	implicitRetry      = "retry"
	implicitThanks     = "thanks"

	digestPending = "pending"
	digestSkipped = "skipped"
	digestDone    = "done"
)

var (
	verdicts  = []string{verdictDone, verdictPartly, verdictNotDone, verdictWrong, verdictCantTell}
	implicits = []string{implicitNone, implicitCorrection, implicitRepeat, implicitRetry, implicitThanks}
	delivered = []string{"yes", "partly", "no"}
)

// Effect is what a run's outcome comes to once a person's word, what their
// next message said and the judge are weighed.
type Effect string

const (
	Good    Effect = "good"
	Bad     Effect = "bad"
	Neither Effect = "neither"
)

// Effective weighs a run's outcome: a team member's own word, given signed
// in, outranks the judge. What people wrote next in the conversation is
// evidence the judge has already read, never a verdict of its own: channel
// text is anyone's, and must not steer what an agent learns. implicit is
// kept in the signature for that reason, and deliberately unused here.
func Effective(verdict, _ string, up, down int32) Effect {
	switch {
	case down > 0:
		return Bad
	case up > 0:
		return Good
	}
	switch verdict {
	case verdictDone:
		return Good
	case verdictPartly, verdictNotDone, verdictWrong:
		return Bad
	}
	return Neither
}

// Sampled is whether a run judged good is among the share digested anyway.
// The run id's hash decides, so the answer is the same whenever it is asked.
func Sampled(runID uuid.UUID, share int16) bool {
	sum := sha256.Sum256(runID[:])
	return int16(binary.BigEndian.Uint64(sum[:8])%100) < share
}

// digestFor is whether a judged run is digested: every bad one, every one
// whose tree fought a tool (the same tool failing again and again, though
// the run may have ended well), and a good one in the sample, so the
// improver has successes to contrast with.
func digestFor(e Effect, sampled, toolTrouble bool) string {
	if e == Bad || toolTrouble || (e == Good && sampled) {
		return digestPending
	}
	return digestSkipped
}

// clip cuts s to at most n characters, on a rune boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func oneOf(v string, set []string) bool { return slices.Contains(set, v) }
