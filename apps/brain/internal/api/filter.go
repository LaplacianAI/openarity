package api

import (
	"net/http"
	"slices"
	"strings"
)

var RunStatuses = []string{"queued", "running", "succeeded", "failed", "cancelled"}

var SessionStatuses = []string{"open", "closed"}

func OneOf(w http.ResponseWriter, r *http.Request, param string, allowed []string) ([]string, bool) {
	values := r.URL.Query()[param]
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			http.Error(w, param+" must be one of "+strings.Join(allowed, ", "), http.StatusBadRequest)
			return nil, false
		}
	}
	return values, true
}
