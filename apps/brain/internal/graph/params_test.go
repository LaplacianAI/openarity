package graph

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

func mustHeader(t *testing.T, params map[string]any) string {
	t.Helper()

	got, err := header(params)
	if err != nil {
		t.Fatalf("header(%v): %v", params, err)
	}
	return got
}

// No parameters is no header at all: the query is sent as written.
func TestNoParametersIsNoHeader(t *testing.T) {
	t.Parallel()

	for _, params := range []map[string]any{nil, {}} {
		if got := mustHeader(t, params); got != "" {
			t.Errorf("header(%v) = %q, want nothing", params, got)
		}
	}
}

// One set of values is always one string, whatever order the map yields: the
// tests can be exact, and FalkorDB's query cache can hit.
func TestParametersAreWrittenInNameOrder(t *testing.T) {
	t.Parallel()

	params := map[string]any{"zeta": 1, "alpha": "x", "mid": true, "Beta": nil, "_under": 2}
	want := `CYPHER Beta=null _under=2 alpha="x" mid=true zeta=1 `
	for range 50 {
		if got := mustHeader(t, params); got != want {
			t.Fatalf("header = %q, want %q", got, want)
		}
	}
}

func TestEachTypeHasItsLiteral(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		value any
		want  string
	}{
		"nil":               {nil, `null`},
		"true":              {true, `true`},
		"false":             {false, `false`},
		"an int":            {42, `42`},
		"a negative int":    {-7, `-7`},
		"an int32":          {int32(-2147483648), `-2147483648`},
		"the largest int64": {int64(math.MaxInt64), `9223372036854775807`},
		"the least int64":   {int64(math.MinInt64), `-9223372036854775808`},
		// The shortest text that reads back as the same float32, not the
		// float64 nearest to it: 0.1 and not 0.10000000149011612.
		"a float32":             {float32(0.1), `0.1`},
		"a float64":             {0.1, `0.1`},
		"a whole float":         {float32(3), `3`},
		"a small float32":       {float32(1e-7), `1e-07`},
		"the largest float32":   {float32(math.MaxFloat32), `3.4028235e+38`},
		"negative zero":         {float32(math.Copysign(0, -1)), `-0`},
		"a vector":              {[]float32{0.5, -1, 1e-7}, `[0.5,-1,1e-07]`},
		"an empty vector":       {[]float32{}, `[]`},
		"a nil vector":          {[]float32(nil), `[]`},
		"strings":               {[]string{"a", `b"c`}, `["a","b\"c"]`},
		"no strings":            {[]string{}, `[]`},
		"a mixed list":          {[]any{1, "a", nil, []any{true}}, `[1,"a",null,[true]]`},
		"a map, keys in order":  {map[string]any{"name": "pdf", "kind": "custom", "bare": false}, `{bare:false,kind:"custom",name:"pdf"}`},
		"an empty map":          {map[string]any{}, `{}`},
		"a nil map":             {map[string]any(nil), `{}`},
		"a nested map":          {map[string]any{"props": map[string]any{"allow": []string{"x"}}}, `{props:{allow:["x"]}}`},
		"a list of maps":        {[]map[string]any{{"to": "a"}, {"to": "b", "allow": []string{"x", "y"}}}, `[{to:"a"},{allow:["x","y"],to:"b"}]`},
		"an empty list of maps": {[]map[string]any{}, `[]`},
		"a null inside a map":   {map[string]any{"description": nil}, `{description:null}`},
		"an empty string":       {"", `""`},
		"a string":              {"pdf", `"pdf"`},
	} {
		got := mustHeader(t, map[string]any{"v": tc.value})
		if want := "CYPHER v=" + tc.want + " "; got != want {
			t.Errorf("%s: header = %q, want %q", name, got, want)
		}
	}
}

// Only the backslash and the double quote are escaped. Every other character
// goes as itself, because FalkorDB's parser reads a handful of escapes and
// takes the rest as literal text: a \u00a0 it is sent is stored as six
// characters, not as a non-breaking space. Measured against FalkorDB 4.20.6.
func TestOnlyTheBackslashAndTheQuoteAreEscaped(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		`plain`:            `"plain"`,
		`it's`:             `"it's"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		`trailing\`:        `"trailing\\"`,
		`\"`:               `"\\\""`,
		`\n as two chars`:  `"\\n as two chars"`,
		"a real\nnewline":  "\"a real\nnewline\"",
		"tab\tand\rreturn": "\"tab\tand\rreturn\"",
		"nbsp\u00a0here":   "\"nbsp\u00a0here\"",
		"sep\u2028here":    "\"sep\u2028here\"",
		"bell\x07del\x7f":  "\"bell\x07del\x7f\"",
		"漢字 🙂 é":           "\"漢字 🙂 é\"",
		`" RETURN 1 //`:    `"\" RETURN 1 //"`,
		"`tick` $dollar":   "\"`tick` $dollar\"",
	} {
		got := mustHeader(t, map[string]any{"v": in})
		if want := "CYPHER v=" + want + " "; got != want {
			t.Errorf("%q: header = %q, want %q", in, got, want)
		}
	}
}

// unquote reads a literal back the way the escaping rule implies: a backslash
// makes the next byte literal, and an unescaped quote ends the string.
func unquote(t *testing.T, lit string) (value, rest string) {
	t.Helper()

	if lit == "" || lit[0] != '"' {
		t.Fatalf("literal %q does not open with a quote", lit)
	}
	var b strings.Builder
	for i := 1; i < len(lit); i++ {
		switch lit[i] {
		case '\\':
			i++
			if i == len(lit) {
				t.Fatalf("literal %q ends in a lone backslash", lit)
			}
			b.WriteByte(lit[i])
		case '"':
			return b.String(), lit[i+1:]
		default:
			b.WriteByte(lit[i])
		}
	}
	t.Fatalf("literal %q never closes", lit)
	return "", ""
}

// Whatever the string, the literal closes exactly where the string ends:
// nothing after it but the separating space. That is the property that keeps
// a description from becoming part of the query.
func TestNoStringCanEndItsLiteralEarly(t *testing.T) {
	t.Parallel()

	alphabet := []rune{'"', '\\', '\'', '`', ' ', '\n', '$', '{', '}', '/', '=', 'a', 'é', '漢', '\u00a0', '\x01'}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		runes := make([]rune, rng.IntN(12))
		for i := range runes {
			runes[i] = alphabet[rng.IntN(len(alphabet))]
		}
		in := string(runes)

		got := mustHeader(t, map[string]any{"v": in})
		value, rest := unquote(t, strings.TrimPrefix(got, "CYPHER v="))
		if value != in || rest != " " {
			t.Fatalf("%q was written as %q: reads back as %q with %q left over", in, got, value, rest)
		}
	}
}

func TestStringsThatCannotBeStoredAreRefused(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"a NUL":            "a\x00b",
		"only a NUL":       "\x00",
		"invalid UTF-8":    "a\xffb",
		"a truncated rune": "\xe6\xbc",
		"a lone surrogate": "\xed\xa0\x80",
	} {
		for where, value := range map[string]any{
			"a string":        in,
			"in a list":       []string{"ok", in},
			"in a map":        map[string]any{"description": in},
			"in a mixed list": []any{1, in},
		} {
			got, err := header(map[string]any{"v": value})
			if err == nil || got != "" {
				t.Errorf("%s, %s: header = %q, %v; want it refused", name, where, got, err)
			}
		}
	}
}

func TestNumbersThatCannotBeStoredAreRefused(t *testing.T) {
	t.Parallel()

	nan32, inf32 := float32(math.NaN()), float32(math.Inf(1))
	for name, value := range map[string]any{
		"NaN":                  math.NaN(),
		"infinity":             math.Inf(1),
		"negative infinity":    math.Inf(-1),
		"a float32 NaN":        nan32,
		"a float32 infinity":   inf32,
		"NaN in a vector":      []float32{0.1, nan32, 0.2},
		"infinity in a vector": []float32{inf32},
		"NaN in a map":         map[string]any{"score": math.NaN()},
	} {
		got, err := header(map[string]any{"v": value})
		if err == nil || got != "" || !strings.Contains(err.Error(), "NaN and infinity") {
			t.Errorf("%s: header = %q, %v; want it refused", name, got, err)
		}
	}
}

// A value of a type that has no literal is an error naming the type and the
// parameter. It is never a panic: one unexpected value fails one write, not
// the worker.
func TestAnUnknownTypeIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	s := "x"
	for name, value := range map[string]any{
		"a uuid":          uuid.New(),
		"bytes":           []byte("x"),
		"ints":            []int{1, 2},
		"float64s":        []float64{0.1},
		"a struct":        struct{ A int }{1},
		"a pointer":       &s,
		"a uint":          uint(3),
		"a map of string": map[string]string{"a": "b"},
		"a func":          func() {},
	} {
		got, err := header(map[string]any{"embedding": value})
		if err == nil || got != "" {
			t.Errorf("%s: header = %q, %v; want an error", name, got, err)
			continue
		}
		if !strings.Contains(err.Error(), "embedding") || !strings.Contains(err.Error(), "cannot be written as a Cypher value") {
			t.Errorf("%s: err = %v, want the parameter and the reason named", name, err)
		}
	}

	_, err := header(map[string]any{"v": map[string]any{"props": map[string]any{"id": uuid.Nil}}})
	if err == nil || !strings.Contains(err.Error(), "v: props: id: a uuid.UUID") {
		t.Errorf("err = %v, want the path to the value", err)
	}
}

// Names are written into the query unquoted, so a name is an identifier or it
// is refused. This is what keeps a property name from becoming Cypher.
func TestNamesAndKeysMustBeIdentifiers(t *testing.T) {
	t.Parallel()

	bad := []string{"", "1a", "a-b", "a b", "a=b", "$a", "a.b", "a:b", "é", "a}", `a"`, "x} RETURN 1 //", "a\n"}
	for _, name := range bad {
		if got, err := header(map[string]any{name: 1}); err == nil || got != "" {
			t.Errorf("parameter name %q: header = %q, %v; want it refused", name, got, err)
		}
		if got, err := header(map[string]any{"v": map[string]any{name: 1}}); err == nil || got != "" {
			t.Errorf("map key %q: header = %q, %v; want it refused", name, got, err)
		}
		if got, err := header(map[string]any{"v": []map[string]any{{"ok": 1}, {name: 1}}}); err == nil || got != "" {
			t.Errorf("key %q in a list of maps: header = %q, %v; want it refused", name, got, err)
		}
	}

	for _, name := range []string{"a", "A", "_", "_a1", "content_sha256", "camelCase", "a1"} {
		if _, err := header(map[string]any{name: map[string]any{name: 1}}); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
}

// A refused value writes nothing: there is no half-built header to send.
func TestARefusalReturnsNoHeader(t *testing.T) {
	t.Parallel()

	got, err := header(map[string]any{"a": "fine", "b": "also fine", "z": math.NaN()})
	if err == nil || got != "" {
		t.Errorf("header = %q, %v; want nothing and an error", got, err)
	}
}

// The output is valid UTF-8 whenever it is produced at all.
func TestTheHeaderIsValidUTF8(t *testing.T) {
	t.Parallel()

	got := mustHeader(t, map[string]any{"v": "漢字\u00a0🙂", "m": map[string]any{"k": []string{"é"}}})
	if !utf8.ValidString(got) {
		t.Errorf("header %q is not valid UTF-8", got)
	}
}
