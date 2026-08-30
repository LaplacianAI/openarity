package channels

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxNameBytes        = 200
	codeUniqueViolation = "23505"

	secretBytes  = 32
	secretPrefix = "oawh_"
)

type Store interface {
	CreateChannel(ctx context.Context, arg db.CreateChannelParams) (db.Channel, error)
	GetChannel(ctx context.Context, id uuid.UUID) (db.Channel, error)
	ListChannelsByTeam(ctx context.Context, arg db.ListChannelsByTeamParams) ([]db.Channel, error)
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	ListPendingSenders(ctx context.Context, arg db.ListPendingSendersParams) ([]db.PendingSender, error)
	ListChannelSenders(ctx context.Context, arg db.ListChannelSendersParams) ([]db.ChannelSender, error)
	FindTeamMember(ctx context.Context, arg db.FindTeamMemberParams) (string, error)
	ApproveSender(ctx context.Context, arg db.ApproveSenderParams) error
	RemoveSender(ctx context.Context, arg db.RemoveSenderParams) error
}

type Secrets interface {
	Put(ctx context.Context, path, key, value string) error
	Delete(ctx context.Context, path string) error
}

type Providers interface {
	Get(name string) (gateway.Provider, bool)
}

type handler struct {
	logger    *slog.Logger
	store     Store
	secrets   Secrets
	providers Providers
}

func New(logger *slog.Logger, s Store, sec Secrets, p Providers) *api.Router {
	h := &handler{logger: logger, store: s, secrets: sec, providers: p}

	r := api.NewRouter("/teams")
	r.Get("/{id}/channels", h.list)
	r.Post("/{id}/channels", h.create)
	r.Delete("/{id}/channels/{channelID}", h.delete)

	senders := "/{id}/channels/{channelID}/senders"
	r.Get(senders+"/pending", h.listPending)
	r.Get(senders, h.listSenders)
	r.Post(senders, h.approve)
	r.Delete(senders, h.removeSender)

	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req createRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	name, ok := api.Name(req.Name, maxNameBytes)
	if !ok {
		http.Error(w, "name must be between 1 and 200 characters", http.StatusBadRequest)
		return
	}

	if _, known := h.providers.Get(req.Provider); !known {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	secret, generated, ok := h.signingSecret(w, req)
	if !ok {
		return
	}

	row, err := h.store.CreateChannel(r.Context(), db.CreateChannelParams{
		TeamID: teamID, Provider: req.Provider, Name: name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
			http.Error(w, "a channel with that name already exists", http.StatusConflict)
			return
		}
		api.Fail(w, h.logger, u, "failed to create channel", err)
		return
	}

	if err := h.secrets.Put(r.Context(),
		secrets.Path(teamID, secrets.KindChannel, row.ID),
		gateway.KeySigning, secret); err != nil {

		if err := h.store.DeleteChannel(r.Context(), row.ID); err != nil {
			h.logger.Error("channel row left behind with no secret",
				"channel_id", row.ID, "error", err)
		}
		h.logger.Error("failed to store the channel secret",
			"subject", u.Subject, "channel_id", row.ID, "error", err)
		http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
		return
	}

	out := created{channel: view(row)}
	if generated {
		out.SigningSecret = secret
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, out)
}

func (h *handler) signingSecret(w http.ResponseWriter, req createRequest) (string, bool, bool) {
	if req.SigningSecret == nil {
		secret, err := newSecret()
		if err != nil {
			h.logger.Error("failed to generate a signing secret", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return "", false, false
		}
		return secret, true, true
	}
	supplied := strings.TrimSpace(*req.SigningSecret)
	if supplied == "" {
		http.Error(w, "signing_secret must not be empty; omit it to have one generated", http.StatusBadRequest)
		return "", false, false
	}
	return supplied, false, true
}

func newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
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

	params, ok := h.page(w, r, teamID, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListChannelsByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list channels", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Channel) any { return channelCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		view,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page channels", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListChannelsByTeamParams, bool) {
	params := db.ListChannelsByTeamParams{TeamID: teamID, PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c channelCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	channelID, err := uuid.Parse(r.PathValue("channelID"))
	if err != nil {
		http.Error(w, "channel id must be a uuid", http.StatusBadRequest)
		return
	}

	row, err := h.store.GetChannel(r.Context(), channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read channel", err)
		return
	}

	if row.TeamID != teamID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := h.store.DeleteChannel(r.Context(), channelID); err != nil {
		api.Fail(w, h.logger, u, "failed to delete channel", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func view(row db.Channel) channel {
	return channel{ID: row.ID, TeamID: row.TeamID, Provider: row.Provider, Name: row.Name}
}
