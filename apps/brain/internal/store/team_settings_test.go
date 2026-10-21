package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func searchKeyPath(team, id uuid.UUID) string {
	return secrets.Path(team, secrets.KindSearch, id)
}

// settings is a valid row with search off; each test changes what it is about.
func settings(team uuid.UUID) db.UpsertTeamSettingsParams {
	return db.UpsertTeamSettingsParams{
		TeamID:             team,
		SearchProvider:     "none",
		CompactionMessages: 100,
	}
}

func keyed(team uuid.UUID, provider string, id uuid.UUID) db.UpsertTeamSettingsParams {
	p := settings(team)
	p.SearchProvider = provider
	p.SearchSecretID = &id
	return p
}

func mustUpsertSettings(t *testing.T, q interface {
	UpsertTeamSettings(ctx context.Context, arg db.UpsertTeamSettingsParams) (db.TeamSetting, error)
}, arg db.UpsertTeamSettingsParams,
) db.TeamSetting {
	t.Helper()

	row, err := q.UpsertTeamSettings(t.Context(), arg)
	if err != nil {
		t.Fatalf("UpsertTeamSettings: %v", err)
	}
	return row
}

func secretPaths(t *testing.T, s *Store) []string {
	t.Helper()

	var paths []string
	for _, row := range secretTombstones(t, s) {
		paths = append(paths, row.Path)
	}
	sort.Strings(paths)
	return paths
}

func samePaths(got, want []string) bool {
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// No row until the first save, so creating a team writes nothing here and
// teams that existed before this table need no backfill. The API reads the
// absence as the defaults.
func TestATeamHasNoSettingsUntilTheyAreSaved(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "unsaved")

	if _, err := s.GetTeamSettings(t.Context(), team.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetTeamSettings on a team that never saved: %v, want pgx.ErrNoRows", err)
	}
}

func TestTheColumnDefaultsAreTheSpecsDefaults(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "defaults")

	if _, err := s.pool.Exec(t.Context(),
		`INSERT INTO team_settings (team_id) VALUES ($1)`, team.ID); err != nil {
		t.Fatalf("inserting a bare row: %v", err)
	}
	got, err := s.GetTeamSettings(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("GetTeamSettings: %v", err)
	}

	if got.SearchProvider != "none" || got.Learning || got.CompactionMessages != 100 ||
		got.ModelName != nil || got.SearchSecretID != nil {
		t.Errorf("defaults = provider %q, learning %v, compaction %d, model %v, secret %v; "+
			"want none, false, 100, nil, nil",
			got.SearchProvider, got.Learning, got.CompactionMessages, got.ModelName, got.SearchSecretID)
	}
}

func TestUpsertingTwiceKeepsOneRow(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "twice")

	first := mustUpsertSettings(t, s, settings(team.ID))

	again := settings(team.ID)
	again.Learning = true
	second := mustUpsertSettings(t, s, again)

	if !second.Learning {
		t.Error("the second save did not change learning")
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at %v did not move past %v", second.UpdatedAt, first.UpdatedAt)
	}

	var n int
	if err := s.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM team_settings WHERE team_id = $1`, team.ID).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows for one team, want 1", n)
	}
}

// The API validates all of this first. The database refuses it as well, so a
// row written by anything else — a migration, psql, a future handler — cannot
// hold a state the brain would then have to interpret.
func TestTeamSettingsRefuseWhatTheAPIWould(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "refused")

	name := "gpt-5"
	url := "http://searxng:8080"
	var (
		zero  int32 = 0
		some  int32 = 1024
		hot         = 2.5
		chill       = -0.1
	)

	for label, tc := range map[string]struct {
		edit       func(p *db.UpsertTeamSettingsParams)
		constraint string
	}{
		"a model name with no max_tokens": {
			func(p *db.UpsertTeamSettingsParams) { p.ModelName = &name },
			"team_settings_model_complete",
		},
		"max_tokens with no model name": {
			func(p *db.UpsertTeamSettingsParams) { p.ModelMaxTokens = &some },
			"team_settings_model_complete",
		},
		"max_tokens of zero": {
			func(p *db.UpsertTeamSettingsParams) { p.ModelName, p.ModelMaxTokens = &name, &zero },
			"team_settings_model_max_tokens_positive",
		},
		"a temperature above 2": {
			func(p *db.UpsertTeamSettingsParams) {
				p.ModelName, p.ModelMaxTokens, p.ModelTemperature = &name, &some, &hot
			},
			"team_settings_model_temperature_range",
		},
		"a temperature below 0": {
			func(p *db.UpsertTeamSettingsParams) {
				p.ModelName, p.ModelMaxTokens, p.ModelTemperature = &name, &some, &chill
			},
			"team_settings_model_temperature_range",
		},
		"a provider nobody wrote": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchProvider = "bing" },
			"team_settings_search_provider_known",
		},
		"searxng with no url": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchProvider = "searxng" },
			"team_settings_search_url_iff_self_hosted",
		},
		"self-hosted firecrawl with no url": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchProvider = "firecrawl-oss" },
			"team_settings_search_url_iff_self_hosted",
		},
		"a url with search off": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchUrl = &url },
			"team_settings_search_url_iff_self_hosted",
		},
		"firecrawl cloud with a url": {
			func(p *db.UpsertTeamSettingsParams) {
				id := uuid.New()
				p.SearchProvider, p.SearchUrl, p.SearchSecretID = "firecrawl-cloud", &url, &id
			},
			"team_settings_search_url_iff_self_hosted",
		},
		"brave with no key": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchProvider = "brave" },
			"team_settings_search_secret_required",
		},
		"firecrawl cloud with no key": {
			func(p *db.UpsertTeamSettingsParams) { p.SearchProvider = "firecrawl-cloud" },
			"team_settings_search_secret_required",
		},
		"a key with search off": {
			func(p *db.UpsertTeamSettingsParams) { id := uuid.New(); p.SearchSecretID = &id },
			"team_settings_search_secret_allowed",
		},
		"searxng with a key": {
			func(p *db.UpsertTeamSettingsParams) {
				id := uuid.New()
				p.SearchProvider, p.SearchUrl, p.SearchSecretID = "searxng", &url, &id
			},
			"team_settings_search_secret_allowed",
		},
		"compaction below 10": {
			func(p *db.UpsertTeamSettingsParams) { p.CompactionMessages = 9 },
			"team_settings_compaction_range",
		},
		"compaction above 10000": {
			func(p *db.UpsertTeamSettingsParams) { p.CompactionMessages = 10001 },
			"team_settings_compaction_range",
		},
	} {
		t.Run(label, func(t *testing.T) {
			p := settings(team.ID)
			tc.edit(&p)

			_, err := s.UpsertTeamSettings(t.Context(), p)

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("UpsertTeamSettings = %v, want a check violation (23514)", err)
			}
			if pgErr.ConstraintName != tc.constraint {
				t.Errorf("refused by %q, want %q", pgErr.ConstraintName, tc.constraint)
			}
		})
	}
}

// Self-hosted Firecrawl is the one provider whose key is optional: an
// instance may run with authentication off.
func TestSelfHostedFirecrawlTakesAKeyOrNone(t *testing.T) {
	s := queryStore(t)
	url := "https://firecrawl.internal.example"

	for name, id := range map[string]*uuid.UUID{"with a key": ptrTo(uuid.New()), "without": nil} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "oss-"+name)
			p := settings(team.ID)
			p.SearchProvider, p.SearchUrl, p.SearchSecretID = "firecrawl-oss", &url, id
			mustUpsertSettings(t, s, p)
		})
	}
}

func ptrTo[T any](v T) *T { return &v }

// Every key is written to a path of its own, and a trigger records the one it
// replaces. One fixed path would let the tombstone of a cleared key destroy a
// key saved after it but before the sweep ran.
func TestReplacingTheSearchKeyTombstonesTheOldOne(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "replaced")
	a, b := uuid.New(), uuid.New()

	mustUpsertSettings(t, s, keyed(team.ID, "brave", a))
	mustUpsertSettings(t, s, keyed(team.ID, "tavily", b))

	if got, want := secretPaths(t, s), []string{searchKeyPath(team.ID, a)}; !samePaths(got, want) {
		t.Errorf("tombstones = %v, want %v", got, want)
	}
}

// The commonest save: something else changed and the key did not. Nothing may
// be destroyed, or every learning switch flipped in the dashboard would take
// search down with it.
func TestKeepingTheSearchKeyTombstonesNothing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "kept")
	a := uuid.New()

	mustUpsertSettings(t, s, keyed(team.ID, "brave", a))
	again := keyed(team.ID, "brave", a)
	again.Learning = true
	mustUpsertSettings(t, s, again)

	if got := secretPaths(t, s); len(got) != 0 {
		t.Errorf("tombstones = %v, want none", got)
	}
}

func TestTurningSearchOffTombstonesTheKey(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "off")
	a := uuid.New()

	mustUpsertSettings(t, s, keyed(team.ID, "brave", a))
	mustUpsertSettings(t, s, settings(team.ID))

	if got, want := secretPaths(t, s), []string{searchKeyPath(team.ID, a)}; !samePaths(got, want) {
		t.Errorf("tombstones = %v, want %v", got, want)
	}
}

// A cascade runs no Go, so only a trigger on team_settings itself sees the
// row go when its team is deleted.
func TestDeletingATeamTombstonesItsSearchKey(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "gone")
	a := uuid.New()
	mustUpsertSettings(t, s, keyed(team.ID, "tavily", a))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}

	rows := secretTombstones(t, s)
	for _, row := range rows {
		if row.Path == searchKeyPath(team.ID, a) {
			if row.TeamID != team.ID {
				t.Errorf("recorded team %s, want %s", row.TeamID, team.ID)
			}
			return
		}
	}
	t.Errorf("no tombstone at %q: %v", searchKeyPath(team.ID, a), rows)
}

func TestDeletingSettingsWithNoKeyTombstonesNothing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "keyless")
	mustUpsertSettings(t, s, settings(team.ID))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}
	// The team's attachment key is tombstoned by the teams trigger; only a
	// search path would be this table's.
	for _, path := range secretPaths(t, s) {
		if strings.Contains(path, "/search/") {
			t.Errorf("tombstoned %q: a null id is not a path", path)
		}
	}
}

// Two saves at once: each wrote its key to its own path before saving, so
// whichever loses the row must have its path recorded, along with the key the
// winner replaced. The current key is never recorded.
func TestTwoSavesRacingTombstoneEveryKeyButTheOneKept(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "racing")
	x, a, b := uuid.New(), uuid.New(), uuid.New()
	mustUpsertSettings(t, s, keyed(team.ID, "brave", x))

	first, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = first.Rollback(t.Context()) }()
	mustUpsertSettings(t, db.New(first), keyed(team.ID, "brave", a))

	done := make(chan error, 1)
	go func() {
		_, err := s.UpsertTeamSettings(t.Context(), keyed(team.ID, "brave", b))
		done <- err
	}()

	// The second save has to be waiting on the first's row lock before the
	// first commits, or this is two saves in a row rather than a race.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := s.pool.QueryRow(t.Context(),
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND query LIKE '%team_settings%')`).Scan(&waiting); err != nil {
			t.Fatalf("watching for the lock wait: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second save never waited on the first")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := first.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the second save: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second save never finished")
	}

	got, err := s.GetTeamSettings(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("GetTeamSettings: %v", err)
	}
	if got.SearchSecretID == nil || *got.SearchSecretID != b {
		t.Fatalf("kept %v, want the last save's key %s", got.SearchSecretID, b)
	}
	want := []string{searchKeyPath(team.ID, x), searchKeyPath(team.ID, a)}
	if paths := secretPaths(t, s); !samePaths(paths, want) {
		t.Errorf("tombstones = %v, want %v", paths, want)
	}
}

// The handler's path when a save fails after its key reached the secret
// store: record that key so the sweep removes it, rather than leaving a
// credential nothing names.
func TestRecordingADeletedSecretIsIdempotent(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "orphan")
	path := searchKeyPath(team.ID, uuid.New())

	for range 2 {
		if err := s.RecordDeletedSecret(t.Context(), db.RecordDeletedSecretParams{
			Path: path, TeamID: team.ID,
		}); err != nil {
			t.Fatalf("RecordDeletedSecret: %v", err)
		}
	}
	if got := secretPaths(t, s); !samePaths(got, []string{path}) {
		t.Errorf("tombstones = %v, want one at %q", got, path)
	}
}
