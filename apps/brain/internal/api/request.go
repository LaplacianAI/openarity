package api

import (
	"encoding/json"
	"io"
	"net/http"
)

const maxBodyBytes = 1 << 20

func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request body must only contain a single JSON object", http.StatusBadRequest)
		return false
	}

	return true
}
