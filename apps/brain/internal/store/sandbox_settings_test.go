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
		image      *string
		constraint string
	}{
		"an unknown provider":    {"modal", nil, &id, nil, "team_settings_sandbox_provider_known"},
		"e2b with a url":         {"e2b", &url, &id, nil, "team_settings_sandbox_url_allowed"},
		"sandboxd with no url":   {"sandboxd", nil, &id, nil, "team_settings_sandbox_url_required"},
		"a provider with no key": {"daytona", nil, nil, nil, "team_settings_sandbox_secret_iff_provider"},
		"none with a key":        {"none", nil, &id, nil, "team_settings_sandbox_secret_iff_provider"},
		"none with an image":     {"none", nil, nil, ptrTo("img"), "team_settings_sandbox_image_allowed"},
		"an empty image":         {"e2b", nil, &id, ptrTo(""), "team_settings_sandbox_image_allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "refused-"+name)
			p := settings(team.ID)
			p.SandboxProvider, p.SandboxUrl, p.SandboxSecretID, p.SandboxImage = tc.provider, tc.url, tc.secret, tc.image
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
