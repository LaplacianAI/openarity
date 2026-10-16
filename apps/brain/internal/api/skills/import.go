package skills

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

const (
	importBudget = 30 * time.Second
	writeBudget  = 30 * time.Second
)

type Importer interface {
	Fetch(ctx context.Context, source string) (hub.Import, error)
}

func (h *handler) importSkill(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req importRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	imp, ok := h.fetch(w, r, req.Source)
	if !ok {
		return
	}
	dir, ok := assemble(w, imp)
	if !ok {
		return
	}

	row, err := h.writer.create(r.Context(), teamID, dir, origin(imp))
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "a skill with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to import skill", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, view(row, written(dir.Files)))
}

func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	current, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}
	if current.SourceRef == nil {
		http.Error(w, "an uploaded skill has no source to sync from", http.StatusConflict)
		return
	}

	imp, ok := h.fetch(w, r, *current.SourceRef)
	if !ok {
		return
	}

	if current.SourceSha != nil && imp.SHA == *current.SourceSha {
		files, err := h.store.ListSkillFiles(r.Context(), current.ID)
		if err != nil {
			api.Fail(w, h.logger, u, "failed to list skill files", err)
			return
		}
		api.WriteJSON(w, h.logger, http.StatusOK, view(current, stored(files)))
		return
	}

	dir, ok := assemble(w, imp)
	if !ok {
		return
	}

	row, err := h.writer.replace(r.Context(), current.ID, teamID, dir, origin(imp))
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "a skill with that name already exists", http.StatusConflict)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to sync skill", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, view(row, written(dir.Files)))
}

func (h *handler) fetch(w http.ResponseWriter, r *http.Request, source string) (hub.Import, bool) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(importBudget + writeBudget))

	ctx, cancel := context.WithTimeout(r.Context(), importBudget)
	defer cancel()

	imp, err := h.importer.Fetch(ctx, source)
	if err == nil {
		return imp, true
	}

	status, msg := importStatus(err), err.Error()
	if status == http.StatusGatewayTimeout {
		msg = fmt.Sprintf("the source did not answer within %s", importBudget)
	}
	if status >= http.StatusInternalServerError {
		h.logger.Warn("skill import failed", "status", status, "error", err)
	}
	http.Error(w, msg, status)
	return hub.Import{}, false
}

func importStatus(err error) int {
	switch {
	case errors.Is(err, hub.ErrInvalid), errors.Is(err, hub.ErrRefused):
		return http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, hub.ErrUnavailable):
		return http.StatusBadGateway
	default:
		return http.StatusUnprocessableEntity
	}
}

func assemble(w http.ResponseWriter, imp hub.Import) (skill.Directory, bool) {
	dir, err := skill.Assemble(imp.Entries)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return skill.Directory{}, false
	}
	return dir, true
}

func origin(imp hub.Import) Origin {
	return Origin{Source: imp.Kind, Ref: &imp.Ref, SHA: &imp.SHA}
}
