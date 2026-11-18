package approval

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestASummaryKeepsTheCallsOrderAndShapes(t *testing.T) {
	t.Parallel()

	got := Summary([]byte(`{"to":"a@example.com","subject":"Overdue invoice 1042","cc":null,
		"count":3,"urgent":true,"labels":["a","b"],"meta":{"k":1}}`))
	want := []Field{
		{"to", "a@example.com"},
		{"subject", "Overdue invoice 1042"},
		{"cc", "null"},
		{"count", "3"},
		{"urgent", "true"},
		{"labels", "[2 items]"},
		{"…", "+1 more"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Summary\n got %v\nwant %v", got, want)
	}
}

// A long value is where an injected instruction hides, and nobody approves a
// letter by reading its first line: it is shown as its length.
func TestALongValueIsShownAsItsLength(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("é", 10<<20/2)
	got := Summary([]byte(`{"body":"` + body + `","short":"` + strings.Repeat("x", 150) + `"}`))
	if got[0].Value != fmt.Sprintf("%d characters", utf8.RuneCountInString(body)) {
		t.Errorf("a 10 MB body shown as %q", got[0].Value)
	}
	if n := utf8.RuneCountInString(got[1].Value); n != summaryChars || !strings.HasSuffix(got[1].Value, "…") {
		t.Errorf("a 150-character value shown as %d characters, %q", n, got[1].Value)
	}
}

// What a person reads is cleaned of everything that could make it read as
// something it is not: a newline that starts a fake line, a bidirectional
// override that reverses what follows, a zero-width character, a NUL.
func TestHostileTextIsCleanedInNamesAndValues(t *testing.T) {
	t.Parallel()

	got := Summary([]byte(`{"to\nApproved: yes":"ev\u202eil.com\u200b moc.dab\u0000x","note":"a\r\n\tb"}`))
	for _, f := range got {
		for _, r := range f.Name + f.Value {
			if r < 0x20 || r == 0x7f || r == 0x202e || r == 0x200b {
				t.Errorf("%q: %q still holds %U", f.Name, f.Value, r)
			}
		}
	}
	if got[0].Name != "to Approved: yes" || got[1].Value != "a b" {
		t.Errorf("cleaned to %v", got)
	}
}

func TestACutNeverSplitsACharacter(t *testing.T) {
	t.Parallel()

	got := Summary([]byte(`{"k":"` + strings.Repeat("日本", 60) + `"}`))
	if !utf8.ValidString(got[0].Value) || utf8.RuneCountInString(got[0].Value) != summaryChars {
		t.Errorf("cut to %q", got[0].Value)
	}
	long := strings.Repeat("ключ", 30)
	if got := Summary([]byte(`{"` + long + `":1}`)); utf8.RuneCountInString(got[0].Name) != summaryChars {
		t.Errorf("a long name cut to %q", got[0].Name)
	}
}

// Arguments that are not one object are shown as what they are, by size,
// and nothing of them is repeated back.
func TestArgumentsThatAreNotAnObjectAreOneLine(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{``, `[]`, `"hi"`, `{"a":1`, `{"a":1}{"b":2}`, `{"a":}`, `null`} {
		got := Summary([]byte(raw))
		if len(got) != 1 || got[0].Name != "arguments" || !strings.HasSuffix(got[0].Value, "not an object") {
			t.Errorf("Summary(%q) = %v", raw, got)
		}
	}
	if got := Summary([]byte(`{}`)); len(got) != 0 {
		t.Errorf("an empty object = %v, want no fields", got)
	}
}

func TestANestedValueIsNamedNotShown(t *testing.T) {
	t.Parallel()

	got := Summary([]byte(`{"o":{"secret":"hunter2","b":2},"one":{"x":1},"l":[{"secret":"hunter2"}],"e":[]}`))
	want := []Field{{"o", "{2 fields}"}, {"one", "{1 field}"}, {"l", "[1 item]"}, {"e", "[0 items]"}}
	if !slices.Equal(got, want) {
		t.Errorf("Summary = %v, want %v", got, want)
	}
}
