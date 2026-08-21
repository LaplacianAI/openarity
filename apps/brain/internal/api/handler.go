package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

func Fail(w http.ResponseWriter, logger *slog.Logger, u *auth.User, msg string, err error) {
	logger.Error(msg, "subject", u.Subject, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func RequireTeam(w http.ResponseWriter, r *http.Request, logger *slog.Logger) (uuid.UUID, bool) {
	id, ok := TeamFrom(r.Context())
	if !ok {
		logger.Error("route has no team — check its scope in rbac.json", "path", r.URL.Path)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, false
	}
	return id, true
}

func Name(raw string, max int) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > max {
		return "", false
	}
	return name, true
}
