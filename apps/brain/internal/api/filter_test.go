package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestOneOfKeepsEveryAllowedValueInOrder(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"absent":                        {"", []string{}},
		"one":                           {"?status=failed", []string{"failed"}},
		"repeated":                      {"?status=failed&status=queued", []string{"failed", "queued"}},
		"duplicate":                     {"?status=failed&status=failed", []string{"failed", "failed"}},
		"another parameter is not read": {"?state=nope", []string{}},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x"+tc.query, nil)

		got, ok := OneOf(w, r, "status", RunStatuses)
		if !ok || len(got) != len(tc.want) || (len(tc.want) > 0 && !slices.Equal(got, tc.want)) {
			t.Errorf("%s: got %v, %v; want %v", name, got, ok, tc.want)
		}
		if w.Code != http.StatusOK {
			t.Errorf("%s: wrote status %d", name, w.Code)
		}
	}
}

// One value outside the set refuses the whole parameter, names it and what
// it may be, and returns nothing to filter by.
func TestOneOfRefusesAValueOutsideTheSet(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"?status=done", "?status=failed&status=done", "?status=", "?status=Failed"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x"+query, nil)

		got, ok := OneOf(w, r, "status", SessionStatuses)
		if ok || got != nil {
			t.Errorf("%s: got %v, %v; want a refusal", query, got, ok)
		}
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "status must be one of open, closed") {
			t.Errorf("%s: status %d %q", query, w.Code, w.Body)
		}
	}
}
