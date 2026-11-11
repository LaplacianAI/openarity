package mcpservers

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	maxSecretKeys  = 20
	maxSecretValue = 16 << 10
	// Every key at its largest, and the JSON around them.
	maxSecretBody = maxSecretKeys*(maxSecretValue+256) + 1024
)

// secretSegment is one name or key of a reference, name#key, as RefPattern
// takes them: a name here is one segment, the path a server's form writes.
var secretSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// putSecret writes the team's MCP secret <name>, every key given, as one
// secret: a write to a path replaces each key it held, so keys written apart
// would keep only the last. The values go to the secret store and nowhere
// else, not even back to the caller.
func (h *handler) putSecret(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	if h.secrets == nil {
		http.Error(w, "this brain does not write secrets; write it to the secret store yourself", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("name")
	if !secretSegment.MatchString(name) {
		http.Error(w, "a secret's name is letters, digits, _ and -, at most 128", http.StatusBadRequest)
		return
	}

	var req secretRequest
	if !api.DecodeJSONWithin(w, r, &req, maxSecretBody) {
		return
	}
	if msg := checkValues(req.Values); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	path := secrets.TeamPath(teamID, secrets.KindMCP) + "/" + name
	if err := h.secrets.PutAll(r.Context(), path, req.Values); err != nil {
		h.logger.Error("failed to store an MCP secret", "subject", u.Subject, "team", teamID, "name", name, "error", err)
		http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func checkValues(values map[string]string) string {
	switch {
	case len(values) == 0:
		return "values is required: the keys to write, each with its value"
	case len(values) > maxSecretKeys:
		return fmt.Sprintf("at most %d keys in one secret", maxSecretKeys)
	}
	for k, v := range values {
		switch {
		case !secretSegment.MatchString(k):
			return "a key is letters, digits, _ and -, at most 128"
		case v == "":
			return "a value is empty; leave the key out to keep nothing there"
		case len(v) > maxSecretValue:
			return fmt.Sprintf("a value is over %d KiB", maxSecretValue>>10)
		}
	}
	return ""
}
