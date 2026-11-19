package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

func browserKeyPath(team, id uuid.UUID) string {
	return secrets.Path(team, secrets.KindBrowser, id)
}

// A browser key is tombstoned as the search and sandbox keys are: replaced,
// or gone with its team. The trigger names all three in one statement.
func TestTheBrowserKeyIsTombstonedLikeTheOthers(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "browsing")
	a, b, sandbox := uuid.New(), uuid.New(), uuid.New()

	first := keyed(team.ID, "none", uuid.Nil)
	first.SearchSecretID = nil
	first.SandboxProvider, first.SandboxSecretID = "e2b", &sandbox
	first.BrowserProvider, first.BrowserSecretID = "kernel", &a
	mustUpsertSettings(t, s, first)

	replaced := first
	replaced.BrowserSecretID = &b
	mustUpsertSettings(t, s, replaced)
	if got, want := secretPaths(t, s), []string{browserKeyPath(team.ID, a)}; !samePaths(got, want) {
		t.Fatalf("after replacing it, tombstones = %v, want %v", got, want)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}
	got := secretPaths(t, s)
	for _, want := range []string{browserKeyPath(team.ID, b), sandboxKeyPath(team.ID, sandbox)} {
		if !slices.Contains(got, want) {
			t.Errorf("after deleting the team, no tombstone at %s: %v", want, got)
		}
	}
}

// The provider, its URL and its key agree, or the row is refused.
func TestABrowserThatCannotWorkIsRefused(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	url, id := "ws://browser.example:3000", uuid.New()
	for name, tc := range map[string]struct {
		provider   string
		url        *string
		secret     *uuid.UUID
		constraint string
	}{
		"an unknown provider":         {"chrome", nil, &id, "team_settings_browser_provider_known"},
		"a hosted one at a url":       {"kernel", &url, &id, "team_settings_browser_url_allowed"},
		"cdp nowhere":                 {"cdp", nil, nil, "team_settings_browser_url_required"},
		"none with a key":             {"none", nil, &id, "team_settings_browser_secret_allowed"},
		"a hosted one with no key":    {"browserbase", nil, nil, "team_settings_browser_secret_required"},
		"browserless cloud, no key":   {"browserless", nil, nil, "team_settings_browser_secret_required"},
		"brightdata with no key":      {"brightdata", nil, nil, "team_settings_browser_secret_required"},
		"lightpanda cloud, with none": {"lightpanda", nil, nil, "team_settings_browser_secret_required"},
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "refused-browser-"+name)
			p := settings(team.ID)
			p.BrowserProvider, p.BrowserUrl, p.BrowserSecretID = tc.provider, tc.url, tc.secret
			_, err := s.UpsertTeamSettings(t.Context(), p)

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
				t.Errorf("UpsertTeamSettings = %v, want %s", err, tc.constraint)
			}
		})
	}
}

// What a browser may be is saved: a self-hosted one with no key, cdp with or
// without, a hosted one with its key.
func TestABrowserThatCanWorkIsSaved(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	url, id := "ws://browser.example:3000", uuid.New()
	for name, tc := range map[string]struct {
		provider string
		url      *string
		secret   *uuid.UUID
	}{
		"none":                        {"none", nil, nil},
		"self-hosted with no key":     {"steel", &url, nil},
		"self-hosted with a key":      {"browserless", &url, &id},
		"cdp with no key":             {"cdp", &url, nil},
		"a hosted one with its key":   {"anchor", nil, &id},
		"lightpanda cloud with a key": {"lightpanda", nil, &id},
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "saved-browser-"+name)
			p := settings(team.ID)
			p.BrowserProvider, p.BrowserUrl, p.BrowserSecretID = tc.provider, tc.url, tc.secret
			if _, err := s.UpsertTeamSettings(t.Context(), p); err != nil {
				t.Errorf("UpsertTeamSettings = %v", err)
			}
		})
	}
}

const beforeBrowser = 20261124090000

// Rolling back drops the columns and tombstones a saved browser key first, so
// no key is left that nothing names; the team's settings and its other keys
// survive, and migrating again works.
func TestBrowserSettingsRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	team := mustCreate(t, s, "browser-down")
	key, search := uuid.New(), uuid.New()
	row := keyed(team.ID, "brave", search)
	row.BrowserProvider, row.BrowserSecretID = "browserbase", &key
	mustUpsertSettings(t, s, row)

	downTo(t, s, beforeBrowser)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'team_settings' AND column_name LIKE 'browser_%')
		   OR (table_name = 'agents' AND column_name = 'browser')`); n != 0 {
		t.Errorf("rolled back, %d browser columns are still there", n)
	}
	if !slices.Contains(rawSecretPaths(t, s), browserKeyPath(team.ID, key)) {
		t.Error("rolled back, the saved browser key has no tombstone")
	}
	if slices.Contains(rawSecretPaths(t, s), searchKeyPath(team.ID, search)) {
		t.Error("rolling back tombstoned the search key, which is still in use")
	}
	if n := count(t, s, `SELECT count(*) FROM team_settings WHERE team_id = $1`, team.ID); n != 1 {
		t.Error("rolling back lost the team's settings")
	}
	// The triggers are as they were: a replaced search key is still tombstoned.
	other := uuid.New()
	if _, err := s.pool.Exec(t.Context(), `UPDATE team_settings SET search_secret_id = $2 WHERE team_id = $1`, team.ID, other); err != nil {
		t.Fatalf("replacing the search key after rolling back: %v", err)
	}
	if !slices.Contains(rawSecretPaths(t, s), searchKeyPath(team.ID, search)) {
		t.Error("rolled back, a replaced search key is no longer tombstoned")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after rolling back: %v", err)
	}
}
