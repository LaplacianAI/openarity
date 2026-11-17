package gateway

import (
	"strings"
	"testing"
)

func TestHasWordFindsTheWordAsAWordOfItsOwn(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		"@Fduv what now?":       true,
		"hey @fduv?":            true,
		"(@FDUV) summarise":     true,
		"ok @Fduv":              true,
		"@Fduv":                 true,
		"@Fduvx what now?":      false,
		"mail@Fduv":             false,
		"x@Fduv1":               false,
		"Fduv without the at":   false,
		"":                      false,
		"two @Fduv and @Fduvs":  true,
		"é@Fduv is a word part": false,
	} {
		if got := HasWord(text, "@Fduv"); got != want {
			t.Errorf("HasWord(%q) = %v; want %v", text, got, want)
		}
	}
	if HasWord("anything", "") {
		t.Error("an empty word matched")
	}
}

func TestATriggerWordIsOneWordWithALetterOrDigit(t *testing.T) {
	t.Parallel()

	for w, want := range map[string]bool{
		"@Fduv":                 true,
		"bot7":                  true,
		"@":                     false,
		"@@@":                   false,
		"":                      false,
		"two words":             false,
		"tab\there":             false,
		"zero\u200bwidth":       false,
		strings.Repeat("a", 32): true,
		strings.Repeat("a", 33): false,
		strings.Repeat("é", 32): true,
		"bad\xffutf8":           false,
	} {
		if got := ValidTriggerWord(w); got != want {
			t.Errorf("ValidTriggerWord(%q) = %v; want %v", w, got, want)
		}
	}
}

func TestWordsOverlapWhenOneMessageCouldCallBoth(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"@Dhars", "@dhars", true},
		{"@Dhars", "@Dharshini", false},
		{"@Fduv", "@Dhars", false},
		{"bot", "@bot", true}, // "@bot hi" holds bot as a word: @ is not a letter
		{"", "@Dhars", false},
	}
	for _, c := range cases {
		if got := WordsOverlap(c.a, c.b); got != c.want {
			t.Errorf("WordsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAChannelOnASharedNumberAnswersOnlyWhenEveryWordIsItsOwn(t *testing.T) {
	cases := []struct {
		name   string
		word   string
		others []string
		want   bool
	}{
		{"its word and another", "@Dhars", []string{"@Fduv"}, true},
		{"no word of its own", "", []string{"@Fduv"}, false},
		{"another with no word", "@Dhars", []string{""}, false},
		{"the same word", "@Dhars", []string{"@DHARS"}, false},
		{"alone", "@Dhars", nil, true},
	}
	for _, c := range cases {
		if got := SharedAnswers(c.word, c.others); got != c.want {
			t.Errorf("%s: SharedAnswers = %v, want %v", c.name, got, c.want)
		}
	}
}
