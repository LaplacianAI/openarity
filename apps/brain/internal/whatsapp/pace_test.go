package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Unix(1_763_190_000, 0)

func TestOneChatWaitsTheGapAnotherDoesNot(t *testing.T) {
	p, ch := newPacer(), uuid.New()
	if w, _ := p.take(ch, "a", t0); w != 0 {
		t.Fatalf("the first send waits %v", w)
	}
	if w, _ := p.take(ch, "a", t0.Add(500*time.Millisecond)); w != chatGap-500*time.Millisecond {
		t.Errorf("the second to one chat waits %v; want the rest of %v", w, chatGap)
	}
	if w, _ := p.take(ch, "b", t0.Add(500*time.Millisecond)); w != 0 {
		t.Errorf("another chat waits %v; want none", w)
	}
}

func TestANumberIsCappedAMinuteAndADay(t *testing.T) {
	p, ch := newPacer(), uuid.New()
	for i := range perMinute {
		if _, err := p.take(ch, string(rune('a'+i)), t0); err != nil {
			t.Fatalf("send %d refused: %v", i, err)
		}
	}
	if _, err := p.take(ch, "z", t0); !errors.Is(err, errCapped) || !strings.Contains(err.Error(), "a minute") {
		t.Fatalf("send %d = %v; want the minute's cap", perMinute+1, err)
	}
	if _, err := p.take(uuid.New(), "z", t0); err != nil {
		t.Errorf("another number was capped: %v", err)
	}

	day := newPacer()
	at := t0
	for i := range perDay {
		at = at.Add(3 * time.Second)
		if _, err := day.take(ch, string(rune(i)), at); err != nil {
			t.Fatalf("send %d of the day refused: %v", i, err)
		}
	}
	if _, err := day.take(ch, "x", at.Add(time.Hour)); !errors.Is(err, errCapped) || !strings.Contains(err.Error(), "a day") {
		t.Fatalf("send %d = %v; want the day's cap", perDay+1, err)
	}
	if _, err := day.take(ch, "x", at.Add(25*time.Hour)); err != nil {
		t.Errorf("a day later the number is still capped: %v", err)
	}
}

func TestTypingTakesLongerForMoreAndNeverOverSixSeconds(t *testing.T) {
	short, long := typing("ok", 0), typing(strings.Repeat("x", 1000), 0)
	if short >= long || long != typingMax {
		t.Errorf("typing %v for two characters, %v for a thousand; want less, then the %v cap", short, long, typingMax)
	}
	if got := typing("ok", time.Hour); got != short+typingJitter {
		t.Errorf("jitter is bounded: got %v", got)
	}
}
