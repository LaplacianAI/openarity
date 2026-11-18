package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

func sandboxKeyPath(team, id uuid.UUID) string {
	return secrets.Path(team, secrets.KindSandbox, id)
}

// A sandbox key is tombstoned as a search key is: replaced, turned off, or
// gone with its team. The trigger names both kinds in one statement.
func TestTheSandboxKeyIsTombstonedLikeTheSearchKey(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "sandboxed")
	a, b, search := uuid.New(), uuid.New(), uuid.New()

	first := keyed(team.ID, "brave", search)
	first.SandboxProvider, first.SandboxSecretID = "e2b", &a
	mustUpsertSettings(t, s, first)

	replaced := keyed(team.ID, "brave", search)
	replaced.SandboxProvider, replaced.SandboxSecretID = "e2b", &b
	mustUpsertSettings(t, s, replaced)
	if got, want := secretPaths(t, s), []string{sandboxKeyPath(team.ID, a)}; !samePaths(got, want) {
		t.Fatalf("after replacing it, tombstones = %v, want %v", got, want)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}
	// The team's other secrets, its attachments key among them, go too.
	got := secretPaths(t, s)
	for _, want := range []string{sandboxKeyPath(team.ID, b), searchKeyPath(team.ID, search)} {
		if !slices.Contains(got, want) {
			t.Errorf("after deleting the team, no tombstone at %s: %v", want, got)
		}
	}
}

// The provider, its URL and its key agree, or the row is refused.
func TestASandboxThatCannotWorkIsRefused(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	url, id := "https://sbx.example", uuid.New()
	for name, tc := range map[string]struct {
		provider   string
		url        *string
		secret     *uuid.UUID
		template   *string
		constraint string
	}{
		"an unknown provider":    {"modal", nil, &id, nil, "team_settings_sandbox_provider_known"},
		"e2b with a url":         {"e2b", &url, &id, nil, "team_settings_sandbox_url_allowed"},
		"sandboxd with no url":   {"sandboxd", nil, &id, nil, "team_settings_sandbox_url_required"},
		"a provider with no key": {"daytona", nil, nil, nil, "team_settings_sandbox_secret_iff_provider"},
		"none with a key":        {"none", nil, &id, nil, "team_settings_sandbox_secret_iff_provider"},
		"a template for daytona": {"daytona", nil, &id, ptrTo("openarity-sandbox"), "team_settings_sandbox_template_e2b_only"},
		"an empty e2b template":  {"e2b", nil, &id, ptrTo(""), "team_settings_sandbox_template_e2b_only"},
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "refused-"+name)
			p := settings(team.ID)
			p.SandboxProvider, p.SandboxUrl, p.SandboxSecretID, p.SandboxTemplate = tc.provider, tc.url, tc.secret, tc.template
			_, err := s.UpsertTeamSettings(t.Context(), p)

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
				t.Errorf("UpsertTeamSettings = %v, want %s", err, tc.constraint)
			}
		})
	}
}

// vars and egress belong to a command server, which runs in a room.
func TestAURLServerHoldsNoVarsOrEgress(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "url-room")
	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO mcp_servers (team_id, name, url, vars) VALUES ($1, 'web', 'https://mcp.example', '{"A":"b"}')`, team.ID)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "mcp_servers_room_fields_command_only" {
		t.Errorf("insert = %v, want mcp_servers_room_fields_command_only", err)
	}
}

const beforeSandboxSettings = 20261118090000

// Rolling back drops the columns and tombstones a saved sandbox key first, so
// no key is left that nothing names; the team's settings and its search key
// survive, and migrating again works.
func TestSandboxSettingsRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	team := mustCreate(t, s, "sandbox-down")
	key, search := uuid.New(), uuid.New()
	row := keyed(team.ID, "brave", search)
	row.SandboxProvider, row.SandboxSecretID = "e2b", &key
	mustUpsertSettings(t, s, row)

	downTo(t, s, beforeSandboxSettings)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'team_settings' AND column_name LIKE 'sandbox_%')
		   OR (table_name = 'mcp_servers' AND column_name IN ('vars', 'egress'))`); n != 0 {
		t.Errorf("rolled back, %d sandbox columns are still there", n)
	}
	if !slices.Contains(secretPaths(t, s), sandboxKeyPath(team.ID, key)) {
		t.Error("rolled back, the saved sandbox key has no tombstone")
	}
	if slices.Contains(secretPaths(t, s), searchKeyPath(team.ID, search)) {
		t.Error("rolling back tombstoned the search key, which is still in use")
	}
	if n := count(t, s, `SELECT count(*) FROM team_settings WHERE team_id = $1`, team.ID); n != 1 {
		t.Error("rolling back lost the team's settings")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after rolling back: %v", err)
	}
}

const beforeSandboxTemplate = 20261118120000

// A team's image becomes an E2B template: kept for an E2B team, dropped for
// any other, since the brain chooses every other provider's image. Rolling
// back brings the column back under its old name.
func TestTheSandboxImageBecomesAnE2BTemplate(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	downTo(t, s, beforeSandboxTemplate)
	e2b, daytona := mustCreate(t, s, "e2b-team"), mustCreate(t, s, "daytona-team")
	key := uuid.New()
	for _, tc := range []struct {
		team     uuid.UUID
		provider string
	}{{e2b.ID, "e2b"}, {daytona.ID, "daytona"}} {
		if _, err := s.pool.Exec(t.Context(), `INSERT INTO team_settings
			(team_id, search_provider, learning, compaction_messages, sandbox_provider, sandbox_secret_id, sandbox_image)
			VALUES ($1, 'none', false, 100, $2, $3, 'their-image')`, tc.team, tc.provider, key); err != nil {
			t.Fatalf("seeding %s: %v", tc.provider, err)
		}
	}

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM team_settings WHERE team_id = $1 AND sandbox_template = 'their-image'`, e2b.ID); n != 1 {
		t.Error("the E2B team's template was lost")
	}
	if n := count(t, s, `SELECT count(*) FROM team_settings WHERE team_id = $1 AND sandbox_template IS NULL`, daytona.ID); n != 1 {
		t.Error("the Daytona team kept an image the brain now chooses")
	}

	downTo(t, s, beforeSandboxTemplate)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'team_settings' AND column_name = 'sandbox_image'`); n != 1 {
		t.Fatal("rolled back, sandbox_image is not there")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after rolling back: %v", err)
	}
}
