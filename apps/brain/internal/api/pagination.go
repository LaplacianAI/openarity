package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

// The limit is int32 because that is what a Postgres LIMIT parameter is. Doing
// the conversion here, once, keeps every call site free of a cast that no
// linter can prove is safe.
const (
	DefaultLimit int32 = 50
	MaxLimit     int32 = 100
)

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor,omitempty"`
}

func EncodeCursor(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func MapPage[S, T any](rows []S, limit int32, cursorOf func(S) any, to func(S) T) (Page[T], error) {
	var next *string

	// The len check is not redundant: a limit of zero trims every row, and the
	// cursor would then be read from rows[-1].
	if len(rows) > int(limit) {
		rows = rows[:limit]
		if len(rows) > 0 {
			cursor, err := EncodeCursor(cursorOf(rows[len(rows)-1]))
			if err != nil {
				return Page[T]{}, err
			}
			next = &cursor
		}
	}

	items := make([]T, len(rows))
	for i, row := range rows {
		items[i] = to(row)
	}

	return Page[T]{Items: items, NextCursor: next}, nil
}

func DecodeCursor(w http.ResponseWriter, raw string, v any) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return false
	}

	if err := json.Unmarshal(decoded, v); err != nil {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return false
	}

	return true
}

// A value above the maximum is clamped rather than rejected, as AIP-158
// requires; an unparseable or non-positive one is a client bug and says so.
// ParseInt with a bit size of 32 rejects anything outside int32 up front, so
// the conversion below is over an already bounded value.
func Limit(w http.ResponseWriter, r *http.Request) (int32, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return DefaultLimit, true
	}

	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 1 {
		http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
		return 0, false
	}
	if n > int64(MaxLimit) {
		return MaxLimit, true
	}

	return int32(n), true
}
