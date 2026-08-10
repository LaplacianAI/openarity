package main

import (
	"context"
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func newVerifier(ctx context.Context, cfg *config.Config) (auth.Verifier, error) {
	var chain auth.Chain

	if cfg.OIDCEnabled {
		v, err := auth.NewOIDCVerifier(ctx, cfg.OIDCIssuer, cfg.OIDCAudience)
		if err != nil {
			return nil, err
		}
		chain = append(chain, v)
	}

	if cfg.DevToken != "" {
		v, err := auth.NewDevVerifier(cfg.DevToken)
		if err != nil {
			return nil, err
		}
		chain = append(chain, v)
	}

	if len(chain) == 0 {
		return nil, errors.New("no authentication configured: set OPENARITY_OIDC_ENABLED or OPENARITY_DEV_TOKEN")
	}

	return chain, nil
}
