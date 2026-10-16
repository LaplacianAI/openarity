package graph

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func header(params map[string]any) (string, error) {
	if len(params) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("CYPHER ")
	for _, name := range slices.Sorted(maps.Keys(params)) {
		if !identifier.MatchString(name) {
			return "", fmt.Errorf("graph: parameter name %q is not an identifier", name)
		}
		b.WriteString(name)
		b.WriteByte('=')
		if err := literal(&b, params[name]); err != nil {
			return "", fmt.Errorf("graph: parameter %s: %w", name, err)
		}
		b.WriteByte(' ')
	}
	return b.String(), nil
}

func literal(b *strings.Builder, v any) error {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		return quoted(b, v)
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case int:
		b.WriteString(strconv.Itoa(v))
	case int32:
		b.WriteString(strconv.FormatInt(int64(v), 10))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float32:
		return number(b, float64(v), 32)
	case float64:
		return number(b, v, 64)
	case []float32:
		return list(b, v, func(f float32) error { return number(b, float64(f), 32) })
	case []string:
		return list(b, v, func(s string) error { return quoted(b, s) })
	case []any:
		return list(b, v, func(item any) error { return literal(b, item) })
	case []map[string]any:
		return list(b, v, func(m map[string]any) error { return object(b, m) })
	case map[string]any:
		return object(b, v)
	default:
		return fmt.Errorf("a %T cannot be written as a Cypher value", v)
	}
	return nil
}

func quoted(b *strings.Builder, s string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return errors.New("a string that is not valid UTF-8, or holds a NUL, cannot be stored")
	}

	b.WriteByte('"')
	for i := range len(s) {
		if s[i] == '\\' || s[i] == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return nil
}

func number(b *strings.Builder, f float64, bits int) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errors.New("NaN and infinity cannot be stored")
	}
	b.WriteString(strconv.FormatFloat(f, 'g', -1, bits))
	return nil
}

func list[T any](b *strings.Builder, items []T, each func(T) error) error {
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := each(item); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	return nil
}

func object(b *strings.Builder, m map[string]any) error {
	b.WriteByte('{')
	for i, key := range slices.Sorted(maps.Keys(m)) {
		if !identifier.MatchString(key) {
			return fmt.Errorf("the key %q is not an identifier", key)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(key)
		b.WriteByte(':')
		if err := literal(b, m[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	b.WriteByte('}')
	return nil
}
