package whatsapp

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// What a person could plausibly send, which is what keeps a linked number
// from looking like a bot: two seconds between messages to one chat, 30 a
// minute and 600 a day from one number, and typing first at about 25
// characters a second, never more than six seconds.
const (
	chatGap      = 2 * time.Second
	perMinute    = 30
	perDay       = 600
	typingPerRun = 40 * time.Millisecond
	typingMax    = 6 * time.Second
	typingJitter = 800 * time.Millisecond
)

// errCapped is a part over a cap: retried by the outbox later, not failed.
var errCapped = errors.New("over the number's sending cap")

// pacer is when each number may next send. It is in memory: a role that
// restarts starts the day's count again, which errs towards sending more,
// never towards losing a reply.
type pacer struct {
	mu    sync.Mutex
	sends map[uuid.UUID][]time.Time
	last  map[string]time.Time
}

func newPacer() *pacer {
	return &pacer{sends: map[uuid.UUID][]time.Time{}, last: map[string]time.Time{}}
}

// take reserves the next send from channel to chat and says how long to wait
// for it, or refuses one over a cap.
func (p *pacer) take(channel uuid.UUID, chat string, now time.Time) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	sent := p.sends[channel]
	for len(sent) > 0 && now.Sub(sent[0]) >= 24*time.Hour {
		sent = sent[1:]
	}
	minute := 0
	for _, at := range sent {
		if now.Sub(at) < time.Minute {
			minute++
		}
	}
	switch {
	case len(sent) >= perDay:
		p.sends[channel] = sent
		return 0, fmt.Errorf("%w: %d a day", errCapped, perDay)
	case minute >= perMinute:
		p.sends[channel] = sent
		return 0, fmt.Errorf("%w: %d a minute", errCapped, perMinute)
	}

	key := channel.String() + "/" + chat
	at := now
	if next := p.last[key].Add(chatGap); next.After(at) {
		at = next
	}
	p.last[key] = at
	p.sends[channel] = append(sent, at)
	return at.Sub(now), nil
}

// typing is how long a person would take to type text, and a little more.
func typing(text string, jitter time.Duration) time.Duration {
	d := time.Duration(utf8.RuneCountInString(text)) * typingPerRun
	return min(d, typingMax) + min(max(jitter, 0), typingJitter)
}
