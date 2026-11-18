package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// What a person is shown of a call: its first fields, each cut short.
	summaryFields = 6
	summaryChars  = 80
	// A string longer than this is shown as its length: a body, a file, a
	// prompt. Nobody approves a letter by reading 80 characters of it, and a
	// long value is where an injected instruction hides.
	longString = 200
)

// A Field is one argument as a person reads it.
type Field struct {
	Name  string
	Value string
}

// Summary is what a person is shown of a call's arguments, built from the
// stored call and never from the model's own description of it: an injected
// page can write that. The arguments are hostile text, so every name and
// value is cleaned of control and format characters (a bidirectional
// override, a zero-width joiner) and cut short. Fields keep the order the
// call gave them.
func Summary(args []byte) []Field {
	fields, more, err := topLevel(args)
	if err != nil {
		return []Field{{Name: "arguments", Value: fmt.Sprintf("%s, not an object", sizeOf(len(args)))}}
	}
	if more > 0 {
		fields = append(fields, Field{Name: "…", Value: fmt.Sprintf("+%d more", more)})
	}
	return fields
}

// topLevel reads the first fields of a JSON object, in order, with the
// count of those past them.
func topLevel(args []byte) ([]Field, int, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, 0, errors.New("not an object")
	}

	var (
		fields []Field
		more   int
	)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		name, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		if len(fields) == summaryFields {
			more++
			continue
		}
		fields = append(fields, Field{Name: clean(name), Value: valueOf(raw)})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, 0, errors.New("more after the object")
	}
	return fields, more, nil
}

func valueOf(raw json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "?"
	}
	switch v := v.(type) {
	case string:
		if n := utf8.RuneCountInString(v); n > longString {
			return fmt.Sprintf("%d characters", n)
		}
		return clean(v)
	case map[string]any:
		return fmt.Sprintf("{%s}", plural(len(v), "field"))
	case []any:
		return fmt.Sprintf("[%s]", plural(len(v), "item"))
	case nil:
		return "null"
	default:
		return clean(fmt.Sprint(v))
	}
}

// clean makes hostile text safe to show: every control and format character
// becomes a space, runs of space become one, and it is cut to summaryChars on
// a rune boundary.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > summaryChars {
		s = string([]rune(s)[:summaryChars-1]) + "…"
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func sizeOf(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
