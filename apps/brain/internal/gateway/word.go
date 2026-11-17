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

// WordsOverlap is whether a message could call both words: one holds the
// other as a word of its own, as "@Bot" and "@bot" do, or "@team" and
// "@team" inside "hi @team". "@Dhars" and "@Dharshini" do not: a letter
// follows. Empty overlaps nothing.
func WordsOverlap(a, b string) bool {
	return a != "" && b != "" && (HasWord(a, b) || HasWord(b, a))
}

// SharedAnswers is whether a channel on a number other channels also link
// may answer: it has a word, every other has one, and no two could be called
// by one message. A message reaches each channel on the number, so one with
// no word would answer what was meant for another.
func SharedAnswers(word string, others []string) bool {
	if word == "" {
		return false
	}
	for _, o := range others {
		if o == "" || WordsOverlap(word, o) {
			return false
		}
	}
	return true
}
