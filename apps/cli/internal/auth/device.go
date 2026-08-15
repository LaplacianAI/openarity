package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"
)

const deviceScope = "openid profile email offline_access"

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

var (
	defaultInterval = 5 * time.Second
	slowDownStep    = 5 * time.Second
	defaultLifetime = 15 * time.Minute
)

var ErrLoginRefused = errors.New("the login was refused")

type DeviceAuth struct {
	UserCode        string
	VerificationURI string
	Complete        string
	ExpiresIn       time.Duration

	deviceCode string
	interval   time.Duration
}

type deviceResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

func (p *Provider) StartDevice(ctx context.Context) (*DeviceAuth, error) {
	if p.device == "" {
		return nil, fmt.Errorf(
			"the identity provider at %s does not offer the device flow, "+
				"so `oa` cannot log in from a terminal", p.issuer)
	}

	var body deviceResponse
	oerr, err := p.post(ctx, p.device, url.Values{"scope": {deviceScope}}, &body)
	if err != nil {
		return nil, err
	}
	if oerr != nil {
		if oerr.Code == "invalid_scope" {
			return nil, fmt.Errorf(
				"the identity provider will not grant %q to this client — "+
					"check the scopes it allows: %w", deviceScope, oerr)
		}
		return nil, fmt.Errorf("the identity provider refused the login request: %w", oerr)
	}

	if body.DeviceCode == "" || body.UserCode == "" || body.VerificationURI == "" {
		return nil, fmt.Errorf("%s did not return a usable device authorization", p.device)
	}

	interval, lifetime := defaultInterval, defaultLifetime
	if body.Interval > 0 {
		interval = time.Duration(body.Interval) * time.Second
	}
	if body.ExpiresIn > 0 {
		lifetime = time.Duration(body.ExpiresIn) * time.Second
	}

	return &DeviceAuth{
		UserCode:        body.UserCode,
		VerificationURI: body.VerificationURI,
		Complete:        body.VerificationURIComplete,
		ExpiresIn:       lifetime,
		deviceCode:      body.DeviceCode,
		interval:        interval,
	}, nil
}

func (p *Provider) WaitForToken(ctx context.Context, device *DeviceAuth) (*Token, error) {
	form := url.Values{
		"grant_type":  {deviceGrant},
		"device_code": {device.deviceCode},
	}
	wait := device.interval

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("gave up waiting for the login to be approved: %w", ctx.Err())
		case <-time.After(wait):
		}

		token, oerr, err := p.exchange(ctx, form)
		if err != nil {
			return nil, err
		}
		if oerr == nil {
			return token, nil
		}

		switch oerr.Code {
		case "authorization_pending":
			// Nobody has approved it yet. Ask again on the same interval.
		case "slow_down":
			wait += slowDownStep
		case "access_denied":
			return nil, ErrLoginRefused
		case "expired_token":
			return nil, errors.New("the code expired before it was approved — run `oa login` again")
		default:
			return nil, fmt.Errorf("the identity provider ended the login: %w", oerr)
		}
	}
}
