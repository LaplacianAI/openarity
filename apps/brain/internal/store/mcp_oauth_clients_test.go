package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const callback = "https://dash.example/oauth/mcp/callback"

func (w *oauthWorld) register(t *testing.T, client string, expected *string, secret *uuid.UUID) (db.ReplaceOAuthClientRow, error) {
	t.Helper()
	return w.s.ReplaceOAuthClient(t.Context(), db.ReplaceOAuthClientParams{
		ServerID: w.server.ID, TeamID: w.team.ID, Issuer: "https://accounts.example", RedirectUri: callback,
		ClientID: client, SecretID: secret, ExpectedClientID: expected,
	})
}

func (w *oauthWorld) mustRegister(t *testing.T, client string, expected *string, secret *uuid.UUID) {
	t.Helper()
	if _, err := w.register(t, client, expected, secret); err != nil {
		t.Fatalf("registering %s: %v", client, err)
	}
}

func (w *oauthWorld) registered(t *testing.T) (string, bool) {
	t.Helper()
	c, err := w.s.GetOAuthClient(t.Context(), w.server.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return c.ClientID, true
}

func (w *oauthWorld) connected(t *testing.T) bool {
	t.Helper()
	return count(t, w.s, `SELECT count(*) FROM mcp_oauth_connections WHERE server_id = $1`, w.server.ID) == 1
}

func strp(s string) *string { return &s }

func TestAFirstRegistrationIsKept(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)

	got, err := w.register(t, "reg-1", nil, nil)
	if err != nil || got.ClientID != "reg-1" || got.Issuer != "https://accounts.example" || got.RedirectUri != callback {
		t.Fatalf("registered %+v, %v", got, err)
	}
	if id, ok := w.registered(t); !ok || id != "reg-1" {
		t.Errorf("kept %q, want reg-1", id)
	}
}

// Two sign-ins that both read no registration and both registered: the first
// is kept, and the second is told so by getting no row.
func TestASecondRegistrationOverOneItDidNotReadIsRefused(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	w.mustRegister(t, "reg-first", nil, nil)

	cases := map[string]*string{"expecting none": nil, "expecting another": strp("reg-stale")}
	for name, expected := range cases {
		if _, err := w.register(t, "reg-second", expected, nil); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s: err = %v, want pgx.ErrNoRows", name, err)
		}
	}
	if id, _ := w.registered(t); id != "reg-first" {
		t.Errorf("kept %q, want reg-first", id)
	}
}

// A registration replaced takes the sign-in made with it: the tokens are
// bound to the client that obtained them.
func TestReplacingARegistrationEndsTheSignInMadeWithIt(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	w.mustRegister(t, "reg-1", nil, nil)
	w.connect(t)

	got, err := w.register(t, "reg-2", strp("reg-1"), nil)
	if err != nil || got.ClientID != "reg-2" {
		t.Fatalf("replacing: %+v, %v", got, err)
	}
	if w.connected(t) {
		t.Error("the sign-in made with reg-1 outlived it")
	}
	want := secrets.Path(w.team.ID, secrets.KindMCPOAuth, w.server.ID)
	if n := count(t, w.s, `SELECT count(*) FROM deleted_secrets WHERE path = $1`, want); n != 1 {
		t.Errorf("the ended sign-in's tokens were not left to the sweep at %s", want)
	}
}

func TestARefusedReplacementLeavesTheSignInAlone(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	w.mustRegister(t, "reg-1", nil, nil)
	w.connect(t)

	if _, err := w.register(t, "reg-2", nil, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
	if !w.connected(t) {
		t.Error("a replacement that lost ended the sign-in anyway")
	}
}

func TestForgettingARegistrationEndsItsSignInAndOnlyTheOneNamed(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	w.mustRegister(t, "reg-1", nil, nil)
	w.connect(t)

	n, err := w.s.ForgetOAuthClient(t.Context(), db.ForgetOAuthClientParams{ServerID: w.server.ID, ClientID: "reg-other"})
	if err != nil || n != 0 {
		t.Fatalf("forgetting another client: %d, %v; want 0", n, err)
	}
	if _, ok := w.registered(t); !ok || !w.connected(t) {
		t.Fatal("forgetting another client changed this one")
	}

	n, err = w.s.ForgetOAuthClient(t.Context(), db.ForgetOAuthClientParams{ServerID: w.server.ID, ClientID: "reg-1"})
	if err != nil || n != 1 {
		t.Fatalf("forgetting reg-1: %d, %v; want 1", n, err)
	}
	if _, ok := w.registered(t); ok || w.connected(t) {
		t.Error("reg-1, or the sign-in made with it, is still there")
	}
}

// The trigger spells the path as secrets.Path does: a tombstone at any other
// path is never destroyed, and the secret outlives its registration.
func TestARegistrationsSecretIsLeftToTheSweepWhenItGoes(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, w *oauthWorld){
		"replaced": func(t *testing.T, w *oauthWorld) { w.mustRegister(t, "reg-2", strp("reg-1"), nil) },
		"forgotten": func(t *testing.T, w *oauthWorld) {
			if _, err := w.s.ForgetOAuthClient(t.Context(), db.ForgetOAuthClientParams{ServerID: w.server.ID, ClientID: "reg-1"}); err != nil {
				t.Fatal(err)
			}
		},
		"its server deleted": func(t *testing.T, w *oauthWorld) {
			if err := w.s.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
				t.Fatal(err)
			}
		},
		"its team deleted": func(t *testing.T, w *oauthWorld) {
			if _, err := w.s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, w.team.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, gone := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newOAuthWorld(t)
			secret := uuid.New()
			w.mustRegister(t, "reg-1", nil, &secret)
			gone(t, w)
			want := secrets.Path(w.team.ID, secrets.KindMCPOAuthClient, secret)
			if n := count(t, w.s, `SELECT count(*) FROM deleted_secrets WHERE path = $1`, want); n != 1 {
				t.Errorf("no tombstone at %s", want)
			}
		})
	}
}

// A replacement keeping the same secret, or a registration with none, leaves
// nothing to destroy: a tombstone for the secret in use would delete it.
func TestOnlyASecretNoLongerNamedIsTombstoned(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	secret := uuid.New()
	w.mustRegister(t, "reg-1", nil, &secret)
	w.mustRegister(t, "reg-1b", strp("reg-1"), &secret)

	if n := count(t, w.s, `SELECT count(*) FROM deleted_secrets WHERE path LIKE '%/mcp-oauth-client/%' AND team_id = $1`, w.team.ID); n != 0 {
		t.Errorf("tombstoned %d client secrets; the one in use is still named", n)
	}
}

func TestOnlyTheServersAskedForHaveTheirRegistrationsListed(t *testing.T) {
	t.Parallel()
	w := newOAuthWorld(t)
	w.mustRegister(t, "reg-1", nil, nil)

	got, err := w.s.ListOAuthClientsOf(t.Context(), []uuid.UUID{uuid.New()})
	if err != nil || len(got) != 0 {
		t.Errorf("another server's: %v, %v", got, err)
	}
	got, err = w.s.ListOAuthClientsOf(t.Context(), []uuid.UUID{w.server.ID})
	if err != nil || len(got) != 1 || got[0].ClientID != "reg-1" {
		t.Errorf("this server's: %v, %v", got, err)
	}
}

const beforeMCPOAuthClients = 20261121090000

func TestMCPOAuthClientsRollsBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	downTo(t, s, beforeMCPOAuthClients)
	if n := count(t, s, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'mcp_oauth_clients'`); n != 0 {
		t.Error("rolled back, the table is still there")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
