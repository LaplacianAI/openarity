package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrRefreshRejected = errors.New("the saved login is no longer valid")

func (p *Provider) Refresh(ctx context.Context, refresh string) (*Token, error) {
	refresh = strings.TrimSpace(refresh)
	if refresh == "" {
		return nil, fmt.Errorf("%w: there is no refresh token to renew with", ErrRefreshRejected)
	}

	token, oerr, err := p.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
	})
	if err != nil {
		return nil, err
	}
	if oerr != nil {
		if oerr.Code == "invalid_grant" {
			return nil, fmt.Errorf("%w: %w", ErrRefreshRejected, oerr)
		}
		return nil, fmt.Errorf("renew the login: %w", oerr)
	}

	if token.Refresh == "" {
		token.Refresh = refresh
	}
	return token, nil
}
