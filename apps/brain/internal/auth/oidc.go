package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type oidcVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func (o oidcVerifier) Verify(ctx context.Context, token string) (*Principal, error) {
	idToken, err := o.verifier.Verify(ctx, token)
	if err != nil {
		return nil, errors.Join(ErrUnauthenticated, err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	_ = idToken.Claims(&claims)

	return &Principal{
		Kind:    KindUser,
		Issuer:  idToken.Issuer,
		Subject: idToken.Subject,
		Email:   claims.Email,
	}, nil
}

func NewOIDCVerifier(ctx context.Context, issuer, audience string) (Verifier, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	ctx = oidc.ClientContext(ctx, client)

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery for %s: %w", issuer, err)
	}

	v := provider.Verifier(&oidc.Config{
		ClientID:             audience,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	})

	return oidcVerifier{
		verifier: v,
	}, nil
}
