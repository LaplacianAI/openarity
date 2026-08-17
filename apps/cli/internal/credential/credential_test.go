package credential

import (
	"testing"
	"time"
)

// The whole point of storing a moment rather than a lifetime: a credential
// read tomorrow has to know the token is dead. Every case below is one a real
// login produces within an hour of itself.
func TestIsExpiredJudgesTheAccessToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"long dead", now.Add(-time.Hour), true},
		{"just dead", now.Add(-time.Second), true},
		{"dead exactly now", now, true},
		{"alive for an hour", now.Add(time.Hour), false},

		// The minute of slack, from both sides. A token with fifty seconds
		// left is treated as gone: spending it means the request dies in
		// flight and comes back as a 401 nothing can tell apart from a
		// revoked credential. The concrete times are deliberate — changing
		// skew should have to be a decision, not a silent widening.
		{"inside the skew", now.Add(50 * time.Second), true},
		{"outside the skew", now.Add(70 * time.Second), false},
	} {
		if got := (Credential{Expiry: tc.expiry}).IsExpired(now); got != tc.want {
			t.Errorf("%s: IsExpired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A token set by hand or arriving in OPENARITY_TOKEN has no login behind it
// and nothing to refresh with. Calling it expired would send the CLI looking
// for a refresh token that was never issued.
func TestACredentialWithNoRecordedExpiryIsNeverExpired(t *testing.T) {
	t.Parallel()

	c := Credential{Token: "set-by-hand"}

	if c.IsExpired(time.Now()) {
		t.Error("a credential with no expiry was judged expired")
	}
	if c.IsExpired(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a zero expiry became expired given a distant now")
	}
}

// Get answers with the zero Credential for a context nobody has logged into,
// so this is what separates "never logged in" from "logged in". Reading the
// token directly cannot: a half-written credential is empty in exactly the
// same way.
func TestIsZeroSeesAnythingWorthKeeping(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cred Credential
		want bool
	}{
		{"never logged in", Credential{}, true},
		{"a hand-set token", Credential{Token: "t"}, false},
		{"a full login", Credential{Token: "t", Refresh: "r", Expiry: time.Now()}, false},

		// A refresh token with no access token is what an expired session
		// looks like after the access token is cleared. There is still a
		// login here, and discarding it would make someone log in again for
		// nothing.
		{"only a refresh token", Credential{Refresh: "r"}, false},
	} {
		if got := tc.cred.IsZero(); got != tc.want {
			t.Errorf("%s: IsZero = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An expiry that has passed is not the same question as whether anything can
// be done about it, and the two are answered by different fields. Collapsing
// them is how a hand-set token ends up being refreshed with an empty string.
func TestExpiryAndRenewalAreSeparateQuestions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	dead := now.Add(-time.Hour)

	for _, tc := range []struct {
		name    string
		cred    Credential
		expired bool
		renew   bool
	}{
		{
			"a live login",
			Credential{Token: "t", Refresh: "r", Expiry: now.Add(time.Hour)},
			false, true,
		},
		{
			// The case the refresh exists for: dead, but recoverable without
			// a browser.
			"an expired login",
			Credential{Token: "t", Refresh: "r", Expiry: dead},
			true, true,
		},
		{
			// Dead with nothing behind it. This one has to reach `oa login`
			// rather than the token endpoint.
			"an expired hand-set token",
			Credential{Token: "t", Expiry: dead},
			true, false,
		},
		{
			"a hand-set token with no expiry",
			Credential{Token: "t"},
			false, false,
		},
	} {
		if got := tc.cred.IsExpired(now); got != tc.expired {
			t.Errorf("%s: IsExpired = %v, want %v", tc.name, got, tc.expired)
		}
		if got := tc.cred.CanRefresh(); got != tc.renew {
			t.Errorf("%s: CanRefresh = %v, want %v", tc.name, got, tc.renew)
		}
	}
}

// CanRefresh reads one field, and the mistake it guards against is inferring
// renewability from expiry instead.
func TestCanRefreshReadsTheRefreshTokenAndNothingElse(t *testing.T) {
	t.Parallel()

	if (Credential{Token: "t", Expiry: time.Now().Add(-time.Hour)}).CanRefresh() {
		t.Error("a credential with no refresh token offered to renew itself")
	}
	if !(Credential{Refresh: "r"}).CanRefresh() {
		t.Error("a refresh token was present and CanRefresh said no")
	}
}
