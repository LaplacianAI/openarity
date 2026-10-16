package skills

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	codeUniqueViolation   = "23505"
	codeRestrictViolation = "23001"
)

type Store interface {
	GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error)
	ListSkillsByTeam(ctx context.Context, arg db.ListSkillsByTeamParams) ([]db.ListSkillsByTeamRow, error)
	ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error)
	GetSkillFile(ctx context.Context, arg db.GetSkillFileParams) (db.SkillFile, error)
	DeleteSkill(ctx context.Context, id uuid.UUID) error
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
	InTx(ctx context.Context, fn func(Queries) error) error
}

type ObjectStore interface {
	Objects
	Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error)
}

type handler struct {
	logger   *slog.Logger
	store    Store
	objects  ObjectStore
	importer Importer
	writer   writer
}

func New(logger *slog.Logger, s Store, o ObjectStore, imp Importer) *api.Router {
	h := &handler{logger: logger, store: s, objects: o, importer: imp, writer: writer{store: s, objects: o}}

	r := api.NewRouter("/teams")
	r.Get("/{id}/skills", h.list)
	r.Post("/{id}/skills", h.create)
	r.Get("/{id}/skills/{skillID}", h.get)
	r.Put("/{id}/skills/{skillID}", h.replace)
	r.Delete("/{id}/skills/{skillID}", h.delete)
	r.Get("/{id}/skills/{skillID}/files/{path...}", h.file)
	r.Post("/{id}/skills/import", h.importSkill)
	r.Post("/{id}/skills/{skillID}/sync", h.sync)

	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	dir, ok := readUpload(w, r)
	if !ok {
		return
	}

	row, err := h.writer.create(r.Context(), teamID, dir, uploaded)
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "a skill with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to create skill", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, view(row, written(dir.Files)))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	files, err := h.store.ListSkillFiles(r.Context(), row.ID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list skill files", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, view(row, stored(files)))
}

func (h *handler) replace(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	dir, ok := readUpload(w, r)
	if !ok {
		return
	}

	current, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	row, err := h.writer.replace(r.Context(), current.ID, teamID, dir, uploaded)
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "a skill with that name already exists", http.StatusConflict)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to replace skill", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, view(row, written(dir.Files)))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	err := h.store.DeleteSkill(r.Context(), row.ID)
	if hasCode(err, codeRestrictViolation) {
		http.Error(w, "the skill is granted to an agent", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to delete skill", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params, ok := page(w, r, teamID, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListSkillsByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list skills", err)
		return
	}

	out, err := api.MapPage(rows, limit,
		func(row db.ListSkillsByTeamRow) any { return skillCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		viewSummary,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page skills", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) file(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	p := r.PathValue("path")
	if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	f, err := h.store.GetSkillFile(r.Context(), db.GetSkillFileParams{SkillID: row.ID, Path: p})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read skill file", err)
		return
	}

	body, err := h.objects.Get(r.Context(), teamID, f.ObjectKey)
	switch {
	case errors.Is(err, objects.ErrNotFound):
		api.Fail(w, h.logger, u, "skill file object is missing", err)
		return
	case err != nil:
		api.Fail(w, h.logger, u, "failed to read skill file object", err)
		return
	}

	writeFile(w, f, body)
}

func (h *handler) find(w http.ResponseWriter, r *http.Request, u *auth.User, teamID uuid.UUID) (db.Skill, bool) {
	id, err := uuid.Parse(r.PathValue("skillID"))
	if err != nil {
		http.Error(w, "skill id must be a uuid", http.StatusBadRequest)
		return db.Skill{}, false
	}

	row, err := h.store.GetSkill(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.TeamID != teamID) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Skill{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read skill", err)
		return db.Skill{}, false
	}

	return row, true
}

func writeFile(w http.ResponseWriter, f db.SkillFile, body []byte) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(f.Path)})
	if disposition == "" {
		disposition = "attachment"
	}

	w.Header().Set("Content-Type", f.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, no-store")

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(body) //nolint:gosec // G705: mitigated by the headers above
}

func page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListSkillsByTeamParams, bool) {
	params := db.ListSkillsByTeamParams{TeamID: teamID, PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c skillCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func view(row db.Skill, files []file) detail {
	return detail{
		summary: summary{
			ID: row.ID, TeamID: row.TeamID, Name: row.Name, Description: row.Description,
			Source: row.Source, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
		License:       row.License,
		Compatibility: row.Compatibility,
		Metadata:      json.RawMessage(row.Metadata),
		AllowedTools:  row.AllowedTools,
		SourceRef:     row.SourceRef,
		SourceSHA:     row.SourceSha,
		Body:          row.Body,
		Files:         files,
	}
}

func viewSummary(row db.ListSkillsByTeamRow) summary {
	return summary{
		ID: row.ID, TeamID: row.TeamID, Name: row.Name, Description: row.Description,
		Source: row.Source, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func written(files []skill.File) []file {
	out := make([]file, len(files))
	for i, f := range files {
		out[i] = file{Path: f.Path, Size: int64(len(f.Data)), SHA256: hex.EncodeToString(f.SHA256[:]), MediaType: f.MediaType}
	}
	return out
}

func stored(rows []db.SkillFile) []file {
	out := make([]file, len(rows))
	for i, f := range rows {
		out[i] = file{Path: f.Path, Size: f.Size, SHA256: hex.EncodeToString(f.Sha256), MediaType: f.MediaType}
	}
	return out
}
