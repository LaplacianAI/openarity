package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

type fakeAdmins struct {
	owned bool
	err   error

	calls int
}

func (f *fakeAdmins) AnySuperAdmin(context.Context) (bool, error) {
	f.calls++
	return f.owned, f.err
}

// JSON for the same reason issuer_test.go uses it: the assertions read the
// fields an aggregator would rather than matching a sentence that may be
// reworded.
func warnUnowned(t *testing.T, cfg *config.Config, s superAdminChecker) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	warnIfInstallIsUnowned(t.Context(), cfg, logger, s)

	if buf.Len() == 0 {
		return nil
	}

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("the warning is not one JSON object: %v (%s)", err, buf.String())
	}
	return got
}

func TestFirstUserBootstrapNeedsBothHalves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cfg   *config.Config
		grant bool
	}{
		{
			name:  "asked for, and nobody named",
			cfg:   &config.Config{BootstrapFirstUser: true},
			grant: true,
		},
		{
			// The half that matters most. A deployment naming its admins has
			// answered the question, and a flag inherited from an image must
			// not quietly hand the install to the next visitor.
			name:  "asked for, but SUPER_ADMINS names somebody",
			cfg:   &config.Config{BootstrapFirstUser: true, SuperAdmins: []string{"akadmin"}},
			grant: false,
		},
		{
			name:  "not asked for",
			cfg:   &config.Config{},
			grant: false,
		},
		{
			name:  "not asked for, and somebody named",
			cfg:   &config.Config{SuperAdmins: []string{"akadmin"}},
			grant: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := firstUserBootstrap(tt.cfg); got != tt.grant {
				t.Errorf("firstUserBootstrap = %t, want %t", got, tt.grant)
			}
		})
	}
}

func TestAnUnownedInstallIsReportedAtStartup(t *testing.T) {
	t.Parallel()

	admins := &fakeAdmins{owned: false}
	got := warnUnowned(t, &config.Config{
		BootstrapFirstUser: true,
		APIBind:            "127.0.0.1:21120",
	}, admins)

	if got == nil {
		t.Fatal("an unowned install with the bootstrap on said nothing")
	}
	if got["api_bind"] != "127.0.0.1:21120" {
		t.Errorf("api_bind = %v, want the address somebody would be claiming it on", got["api_bind"])
	}
	if admins.calls != 1 {
		t.Errorf("asked the database %d times, want 1", admins.calls)
	}
}

func TestAnOwnedInstallSaysNothing(t *testing.T) {
	t.Parallel()

	if got := warnUnowned(t, &config.Config{BootstrapFirstUser: true}, &fakeAdmins{owned: true}); got != nil {
		t.Errorf("warned about an install that already has an admin: %v", got)
	}
}

// The database is not consulted at all when the policy is off, which is what
// keeps this free for every deployment that never asked for it.
func TestTheDatabaseIsNotAskedWhenTheBootstrapIsOff(t *testing.T) {
	t.Parallel()

	for _, cfg := range []*config.Config{
		{},
		{SuperAdmins: []string{"akadmin"}},
		{BootstrapFirstUser: true, SuperAdmins: []string{"akadmin"}},
	} {
		admins := &fakeAdmins{owned: false}
		if got := warnUnowned(t, cfg, admins); got != nil {
			t.Errorf("warned with the bootstrap off: %v", got)
		}
		if admins.calls != 0 {
			t.Errorf("asked the database %d times with the bootstrap off, want 0", admins.calls)
		}
	}
}

// A database that cannot answer must not stop the server starting: the warning
// is advice, and refusing to boot over it would turn a hint into an outage.
func TestAFailedCheckWarnsRatherThanHiding(t *testing.T) {
	t.Parallel()

	got := warnUnowned(t, &config.Config{BootstrapFirstUser: true},
		&fakeAdmins{err: errors.New("connection refused")})

	if got == nil {
		t.Fatal("a failed check said nothing at all")
	}
	if got["error"] != "connection refused" {
		t.Errorf("error = %v, want the reason the check failed", got["error"])
	}
}
