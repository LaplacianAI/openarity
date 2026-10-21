package teamsettings

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	KeyAPI            = "api_key"
	defaultCompaction = 100
)

type Store interface {
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	UpsertTeamSettings(ctx context.Context, arg db.UpsertTeamSettingsParams) (db.TeamSetting, error)
	RecordDeletedSecret(ctx context.Context, arg db.RecordDeletedSecretParams) error
}

type Secrets interface {
	Put(ctx context.Context, path, key, value string) error
}

type handler struct {
	logger  *slog.Logger
	store   Store
	secrets Secrets
	allow   func(netip.Addr) bool
}

func New(logger *slog.Logger, s Store, sec Secrets, allow func(netip.Addr) bool) *api.Router {
	h := &handler{logger: logger, store: s, secrets: sec, allow: allow}

	r := api.NewRouter("/teams")
	r.Get("/{id}/settings", h.get)
	r.Put("/{id}/settings", h.put)
	return r
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, found, err := h.stored(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team settings", err)
		return
	}
	if !found {
		api.WriteJSON(w, h.logger, http.StatusOK, defaults())
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, view(row))
}

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req settingsRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	row, found, err := h.stored(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team settings", err)
		return
	}
	var stored *db.TeamSetting
	if found {
		stored = &row
	}

	if err := validate(req, stored, h.allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	secretID, written, ok := h.searchKey(w, r, teamID, req.Search, stored)
	if !ok {
		return
	}

	saved, err := h.store.UpsertTeamSettings(r.Context(), params(teamID, req, secretID))
	if err != nil {
		if written != "" {
			h.forget(r.Context(), teamID, written)
		}
		api.Fail(w, h.logger, u, "failed to save team settings", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, view(saved))
}

func (h *handler) stored(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, bool, error) {
	row, err := h.store.GetTeamSettings(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.TeamSetting{}, false, nil
	}
	if err != nil {
		return db.TeamSetting{}, false, err
	}
	return row, true, nil
}

func (h *handler) searchKey(
	w http.ResponseWriter, r *http.Request, teamID uuid.UUID, s searchRequest, stored *db.TeamSetting,
) (*uuid.UUID, string, bool) {
	switch {
	case s.APIKey != nil && *s.APIKey != "":
		id := uuid.New()
		path := secrets.Path(teamID, secrets.KindSearch, id)
		if err := h.secrets.Put(r.Context(), path, KeyAPI, *s.APIKey); err != nil {
			h.logger.Warn("search key not stored", "team", teamID, "error", err)
			http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
			return nil, "", false
		}
		return &id, path, true
	case s.APIKey == nil && keepsKey(stored, s.Provider):
		return stored.SearchSecretID, "", true
	default:
		return nil, "", true
	}
}

func (h *handler) forget(ctx context.Context, teamID uuid.UUID, path string) {
	err := h.store.RecordDeletedSecret(ctx, db.RecordDeletedSecretParams{Path: path, TeamID: teamID})
	if err != nil {
		h.logger.Error("a search key outlived its failed save", "team", teamID, "error", err)
	}
}

func params(teamID uuid.UUID, req settingsRequest, secretID *uuid.UUID) db.UpsertTeamSettingsParams {
	p := db.UpsertTeamSettingsParams{
		TeamID:             teamID,
		SearchProvider:     req.Search.Provider,
		SearchUrl:          req.Search.URL,
		SearchSecretID:     secretID,
		Learning:           req.Learning,
		CompactionMessages: req.CompactionMessages,
	}
	if m := req.Model; m != nil {
		p.ModelName, p.ModelMaxTokens, p.ModelTemperature = &m.Name, &m.MaxTokens, m.Temperature
	}
	return p
}

func view(row db.TeamSetting) settings {
	s := settings{
		Search: search{
			Provider: row.SearchProvider,
			URL:      row.SearchUrl,
			KeySet:   row.SearchSecretID != nil,
		},
		Learning:           row.Learning,
		CompactionMessages: row.CompactionMessages,
		UpdatedAt:          &row.UpdatedAt,
	}
	if row.ModelName != nil && row.ModelMaxTokens != nil {
		s.Model = &modelRef{Name: *row.ModelName, MaxTokens: *row.ModelMaxTokens, Temperature: row.ModelTemperature}
	}
	return s
}

func defaults() settings {
	return settings{
		Search:             search{Provider: providerNone},
		CompactionMessages: defaultCompaction,
	}
}
