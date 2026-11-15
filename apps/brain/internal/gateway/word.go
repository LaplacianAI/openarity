package gateway

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TriggerWordMax is the longest word a channel may be called by, in characters.
const TriggerWordMax = 32

// ValidTriggerWord is whether w can call a channel: one word, no spaces or
// control characters, with a letter or digit in it, and short enough for a
// person to type.
func ValidTriggerWord(w string) bool {
	n, named := 0, false
	for _, r := range w {
		switch {
		case r == utf8.RuneError, unicode.IsSpace(r), unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return false
		case unicode.IsLetter(r), unicode.IsDigit(r):
			named = true
		}
		n++
	}
	return n >= 1 && n <= TriggerWordMax && named
}

// HasWord is whether text holds word, in any case, as a word of its own:
// "@fduv, what now?" holds "@Fduv", "@Fduvx" and "mail@Fduv" do not.
func HasWord(text, word string) bool {
	if word == "" {
		return false
	}
	for k := 0; k+len(word) <= len(text); k++ {
		if !utf8.RuneStart(text[k]) || !strings.EqualFold(text[k:k+len(word)], word) {
			continue
		}
		before, _ := utf8.DecodeLastRuneInString(text[:k])
		after, _ := utf8.DecodeRuneInString(text[k+len(word):])
		if !wordy(before) && !wordy(after) {
			return true
		}
	}
	return false
}

func wordy(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
