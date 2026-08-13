package authconfig

import "github.com/LaplacianAI/openarity/apps/brain/internal/config"

type oidcConfig struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
}

type authConfigResponse struct {
	Environment      config.Environment `json:"environment"`
	DevTokenAccepted bool               `json:"dev_token_accepted"`
	OIDC             *oidcConfig        `json:"oidc,omitempty"`
}
