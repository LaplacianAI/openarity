package authconfig

import (
	"log/slog"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

type handler struct {
	logger   *slog.Logger
	response authConfigResponse
}

func describe(cfg *config.Config) authConfigResponse {
	response := authConfigResponse{
		Environment:      cfg.Environment,
		DevTokenAccepted: cfg.DevToken != "" && cfg.Environment == config.EnvironmentDevelopment,
	}

	if cfg.OIDCEnabled {
		response.OIDC = &oidcConfig{
			Issuer:   cfg.OIDCIssuer,
			ClientID: cfg.OIDCAudience,
		}
	}

	return response
}

func New(logger *slog.Logger, cfg *config.Config) *api.Router {
	h := &handler{
		logger:   logger,
		response: describe(cfg),
	}

	r := api.NewPublicRouter("/auth")
	r.Get("/config", h.config)
	return r
}

func (h *handler) config(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, h.logger, http.StatusOK, h.response)
}
