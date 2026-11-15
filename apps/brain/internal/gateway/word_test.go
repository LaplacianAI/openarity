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
