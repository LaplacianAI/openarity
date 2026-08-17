package main

import (
	"context"
	"log/slog"
	"slices"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

type issuerLister interface {
	ListUserIssuers(ctx context.Context) ([]string, error)
}

func warnIfIssuerIsNew(ctx context.Context, cfg *config.Config, logger *slog.Logger, s issuerLister) {
	if !cfg.OIDCEnabled {
		return
	}

	known, err := s.ListUserIssuers(ctx)
	if err != nil {
		logger.Warn("could not check whether the OIDC issuer is known", "error", err)
		return
	}

	if !issuerIsNew(cfg.OIDCIssuer, known) {
		return
	}

	logger.Warn("the configured OIDC issuer matches no existing user; logins will "+
		"create new principals and team memberships will not follow them",
		"configured_issuer", cfg.OIDCIssuer,
		"known_issuers", known,
	)
}

func issuerIsNew(configured string, known []string) bool {
	return len(known) > 0 && !slices.Contains(known, configured)
}
