package auth

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRefreshExchangesTheTokenForANewOne(t *testing.T) {
	t.Parallel()

	polls := newScript(reply{
		http.StatusOK,
		`{"access_token":"a-new-access","refresh_token":"a-new-refresh","expires_in":3600}`,
	})
	p := deviceProvider(t, nil, polls.handler())

	fixed := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return fixed }

	got, err := p.Refresh(t.Context(), "the-old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Access != "a-new-access" {
		t.Errorf("Access = %q", got.Access)
	}
	if want := fixed.Add(time.Hour); !got.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want)
	}

	form := polls.form(0)
	for field, want := range map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": "the-old-refresh",
		"client_id":     "a-client",
	} {
		if got := form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// Providers that rotate — authentik with rotation on, Okta, Auth0 — kill the
// old refresh token as they issue the new one. Keeping the old one would mean
// the next renewal is rejected.
func TestARotatedRefreshTokenReplacesTheOldOne(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, nil, jsonHandler(http.StatusOK,
		`{"access_token":"a","refresh_token":"the-rotated-one","expires_in":60}`))

	got, err := p.Refresh(t.Context(), "the-old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Refresh != "the-rotated-one" {
		t.Errorf("Refresh = %q, want the new one", got.Refresh)
	}
}

// The other half, and the one that silently breaks. A provider without
// rotation sends no refresh_token at all and expects the old one to be reused
// — storing what came back would give a login that renews exactly once and
// then sends someone to `oa login` an hour later.
func TestARefreshTokenIsKeptWhenTheProviderSendsNoneBack(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, nil, jsonHandler(http.StatusOK,
		`{"access_token":"a-new-access","expires_in":3600}`))

	got, err := p.Refresh(t.Context(), "the-only-refresh-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Refresh != "the-only-refresh-token" {
		t.Errorf("Refresh = %q, want the original kept", got.Refresh)
	}
}

// invalid_grant is the provider saying this login is over: revoked, expired,
// or already spent. The caller's answer is to discard the credential, so the
// sentinel is what authorises that.
func TestARevokedRefreshTokenIsFatal(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, nil, jsonHandler(http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"token is not valid"}`))

	_, err := p.Refresh(t.Context(), "a-dead-refresh-token")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err = %v, want ErrRefreshRejected", err)
	}
	if !strings.Contains(err.Error(), "token is not valid") {
		t.Errorf("the error drops the provider's description: %v", err)
	}
}

// The one that would be quietly wrong for months. A provider having a bad
// minute is not a dead login, and reporting it as one logs everybody out of a
// session that is perfectly alive — an authentik restart would do it.
func TestAProviderOutageIsNotADeadLogin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"named as temporary", http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`},
		{"a bare 500", http.StatusInternalServerError, `{}`},
		{"a server error with prose", http.StatusBadGateway, `{"error":"server_error","error_description":"upstream"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := deviceProvider(t, nil, jsonHandler(tc.status, tc.body))

			_, err := p.Refresh(t.Context(), "a-perfectly-good-refresh-token")
			if err == nil {
				t.Fatal("a failed renewal reported success")
			}
			if errors.Is(err, ErrRefreshRejected) {
				t.Errorf("a transient failure was reported as a dead login: %v", err)
			}
		})
	}
}

// A credential set by hand, or arriving in OPENARITY_TOKEN, has no refresh
// token behind it. Asking the provider to renew nothing wastes a round trip
// and produces an error naming a field rather than the situation.
func TestRenewingWithoutARefreshTokenNeverReachesTheProvider(t *testing.T) {
	t.Parallel()

	polls := newScript(reply{http.StatusOK, `{"access_token":"a","expires_in":60}`})
	p := deviceProvider(t, nil, polls.handler())

	for _, empty := range []string{"", "   ", "\n"} {
		_, err := p.Refresh(t.Context(), empty)
		if !errors.Is(err, ErrRefreshRejected) {
			t.Errorf("%q: err = %v, want ErrRefreshRejected", empty, err)
		}
	}
	if polls.calls() != 0 {
		t.Errorf("the provider was asked %d times to renew nothing", polls.calls())
	}
}

// The access token is what every request carries, so a renewal that returns
// none is a failure however cheerful the status line.
func TestARenewalWithNoAccessTokenIsAnError(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, nil, jsonHandler(http.StatusOK, `{"refresh_token":"r","expires_in":3600}`))

	if _, err := p.Refresh(t.Context(), "a-refresh-token"); err == nil {
		t.Fatal("a renewal with no access token was accepted")
	}
}
