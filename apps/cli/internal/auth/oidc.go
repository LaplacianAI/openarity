package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderBody = 1 << 20

type Provider struct {
	http     *http.Client
	clientID string
	issuer   string
	device   string
	token    string

	now func() time.Time
}

type Token struct {
	Access  string
	Refresh string
	Expiry  time.Time
}

type discovery struct {
	Issuer                      string `json:"issuer"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func NewProvider(ctx context.Context, client *http.Client, issuer, clientID string) (*Provider, error) {
	address := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable issuer: %w", issuer, err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the identity provider at %s: %w", address, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the identity provider answered %d %s at %s",
			res.StatusCode, http.StatusText(res.StatusCode), address)
	}

	var found discovery
	if err := json.NewDecoder(io.LimitReader(res.Body, maxProviderBody)).Decode(&found); err != nil {
		return nil, fmt.Errorf("%s did not return an OIDC discovery document: %w", address, err)
	}

	if strings.TrimSuffix(found.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return nil, fmt.Errorf(
			"the identity provider at %s calls itself %q, but the brain expects %q",
			address, found.Issuer, issuer)
	}
	if found.TokenEndpoint == "" {
		return nil, fmt.Errorf("the identity provider at %s does not support token endpoint", address)
	}

	return &Provider{
		http:     client,
		clientID: clientID,
		issuer:   found.Issuer,
		device:   found.DeviceAuthorizationEndpoint,
		token:    found.TokenEndpoint,
		now:      time.Now,
	}, nil
}

func (e oauthError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return e.Code
}

func (p *Provider) post(ctx context.Context, endpoint string, form url.Values, out any) (*oauthError, error) {
	form.Set("client_id", p.clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable endpoint: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the identity provider at %s: %w", endpoint, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxProviderBody))
	if err != nil {
		return nil, fmt.Errorf("read the reply from %s: %w", endpoint, err)
	}

	var named oauthError
	if err := json.Unmarshal(body, &named); err != nil {
		return nil, fmt.Errorf("%s did not return JSON: %w", endpoint, err)
	}
	if named.Code != "" {
		return &named, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the identity provider answered %d %s at %s",
			res.StatusCode, http.StatusText(res.StatusCode), endpoint)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("%s did not return JSON: %w", endpoint, err)
	}

	return nil, nil
}

func (p *Provider) exchange(ctx context.Context, form url.Values) (*Token, *oauthError, error) {
	var body tokenResponse

	oerr, err := p.post(ctx, p.token, form, &body)
	if err != nil || oerr != nil {
		return nil, oerr, err
	}
	if body.AccessToken == "" {
		return nil, nil, fmt.Errorf("%s returned no access token", p.token)
	}

	return &Token{
		Access:  body.AccessToken,
		Refresh: body.RefreshToken,
		Expiry:  p.now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}, nil, nil
}
