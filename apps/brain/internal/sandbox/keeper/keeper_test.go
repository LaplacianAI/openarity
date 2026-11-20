package keeper_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxtest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

const (
	tokenEnv = "ROOM_TOKEN"
	apiKey   = "the-teams-provider-key"
)

type secretsBoth struct {
	secrets.Store
	secrets.Writer
}

// world is a team with a sandbox, the owner of a room, and a provider whose
// rooms are real HTTP servers.
type world struct {
	t       *testing.T
	store   *store.Store
	secrets secretsBoth
	rooms   *sandboxtest.Rooms
	team    uuid.UUID
	keyID   uuid.UUID
	owner   keeper.Owner
	// connected is what the provider's client was opened with.
	mu        sync.Mutex
	connected []string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	dsn := storetest.Migrated(t, func(ctx context.Context, dsn string) error {
		s, err := store.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Migrate(ctx)
		return err
	})
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)

	team, err := s.CreateTeam(t.Context(), "rooms")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	st := static.New()
	writer, ok := st.(secrets.Writer)
	if !ok {
		t.Fatal("the static store cannot write")
	}
	w := &world{
		t: t, store: s, secrets: secretsBoth{st, writer},
		rooms: sandboxtest.New(t, tokenEnv), team: team.ID, keyID: uuid.New(),
	}
	w.useSandbox("sandboxd", "https://sbx.example")

	srv, err := s.CreateMCPServer(t.Context(), db.CreateMCPServerParams{
		TeamID: team.ID, Name: "mail", Command: []string{"npx", "mail-mcp"}, Env: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	w.owner = keeper.Owner{Kind: "mcp_server", ID: srv.ID}
	return w
}

// useSandbox saves the team's sandbox, with a key it has just made.
func (w *world) useSandbox(provider, url string) {
	w.t.Helper()
	w.keyID = uuid.New()
	if err := w.secrets.Put(w.t.Context(), secrets.Path(w.team, secrets.KindSandbox, w.keyID), keeper.KeyField, apiKey); err != nil {
		w.t.Fatal(err)
	}
	p := db.UpsertTeamSettingsParams{
		TeamID: w.team, SearchProvider: "none", CompactionMessages: 100, CompactionTokensMin: 50_000, CompactionTokensMax: 60_000,
		SandboxProvider: provider, BrowserProvider: "none", SandboxSecretID: &w.keyID,
	}
	if url != "" {
		p.SandboxUrl = &url
	}
	if _, err := w.store.UpsertTeamSettings(w.t.Context(), p); err != nil {
		w.t.Fatalf("UpsertTeamSettings: %v", err)
	}
}

func (w *world) keeper() *keeper.Keeper {
	connect := func(_ sandbox.Provider, _, key string) (sandbox.Rooms, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.connected = append(w.connected, key)
		return w.rooms, nil
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return keeper.New(w.store, w.secrets, connect, http.DefaultClient, keeper.Images{}, logger).
		Starting(2*time.Second, 10*time.Millisecond)
}

func want() keeper.Want {
	return keeper.Want{
		Argv:   []string{"sandbox", "shim", "--", "npx", "mail-mcp"},
		Env:    map[string]string{"IMAP_HOST": "imap.example"},
		Egress: []sandbox.HostPort{{Host: "imap.example", Port: 993}},
		Port:   sandbox.ShimPort, Path: "/mcp", HealthPath: "/healthz", TokenEnv: tokenEnv,
	}
}

func (w *world) ensure(k *keeper.Keeper, want keeper.Want) sandbox.Endpoint {
	w.t.Helper()
	ep, err := k.Ensure(w.t.Context(), w.owner, w.team, want)
	if err != nil {
		w.t.Fatalf("Ensure: %v", err)
	}
	return ep
}

// call is a request to the room's endpoint as a client would make it.
func call(t *testing.T, ep sandbox.Endpoint) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ep.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	for name, vs := range ep.Header {
		req.Header[name] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func rowOf(t *testing.T, w *world) db.SandboxRoom {
	t.Helper()
	row, err := w.store.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: w.owner.Kind, OwnerID: w.owner.ID})
	if err != nil {
		t.Fatalf("GetSandboxRoom: %v", err)
	}
	return row
}

func countRows(t *testing.T, w *world, query string, args ...any) int {
	t.Helper()
	var n int
	if err := w.store.Pool().QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The first call makes a room, runs the command in it, waits for it to
// answer and returns an endpoint that the room accepts: its address at /mcp,
// the provider's header, and the room's own token as a bearer.
func TestAColdStartMakesARoomAndReturnsAnEndpointItAccepts(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()

	ep := w.ensure(k, want())

	if !strings.HasSuffix(ep.URL, "/mcp") {
		t.Errorf("URL = %q, want it to end at /mcp", ep.URL)
	}
	if ep.Header.Get(sandboxtest.ProviderHeader) != "1" {
		t.Errorf("the provider's header is not on the endpoint: %v", ep.Header)
	}
	if got := call(t, ep); got != http.StatusOK {
		t.Errorf("the room answered %d to its own endpoint", got)
	}
	if got := w.rooms.Starts(); len(got) != 1 || strings.Join(got[0], " ") != "sandbox shim -- npx mail-mcp" {
		t.Errorf("starts = %v, want the shim command once", got)
	}

	row := rowOf(t, w)
	if row.Provider != "sandboxd" || row.SandboxSecretID != w.keyID || row.TeamID != w.team {
		t.Errorf("row = %+v, want the team's provider and the key the room was made with", row)
	}
	token, err := w.secrets.Get(t.Context(), secrets.RoomPath(w.team, row.ID), keeper.TokenKey)
	if err != nil || len(token) != 64 {
		t.Errorf("token = %q, %v, want 32 random bytes in hex at the room's path", token, err)
	}
	if ep.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("Authorization = %q, want the stored token", ep.Header.Get("Authorization"))
	}
}

// The provider's key goes to the provider and into nothing the command can
// read; the token is the room's, and a caller cannot replace it by naming it.
func TestTheProvidersKeyNeverReachesTheRoom(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()

	hostile := want()
	hostile.Env[tokenEnv] = "chosen-by-the-server"
	w.ensure(k, hostile)

	spec := w.rooms.Created()[0]
	for name, v := range spec.Env {
		if strings.Contains(v, apiKey) {
			t.Errorf("env %s holds the provider's key", name)
		}
	}
	if spec.Env[tokenEnv] == "chosen-by-the-server" || len(spec.Env[tokenEnv]) != 64 {
		t.Errorf("%s = %q, want the room's own token", tokenEnv, spec.Env[tokenEnv])
	}
	if spec.Env["IMAP_HOST"] != "imap.example" {
		t.Errorf("the server's own settings are missing: %v", spec.Env)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.connected) == 0 || w.connected[0] != apiKey {
		t.Errorf("the provider was opened with %v, want the team's key", w.connected)
	}
}

// A room that matches and answers is the room: no second one is made, and its
// use is recorded for the sweep of idle rooms.
func TestARoomThatMatchesAndAnswersIsReused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	first := w.ensure(k, want())
	before := rowOf(t, w)
	time.Sleep(20 * time.Millisecond) // later than the row's last use, by the clock's own tick

	second := w.ensure(k, want())

	if first.URL != second.URL {
		t.Errorf("URLs differ: %q then %q", first.URL, second.URL)
	}
	if n := len(w.rooms.Created()); n != 1 {
		t.Errorf("%d rooms were made, want 1", n)
	}
	if after := rowOf(t, w); !after.LastUsedAt.After(before.LastUsedAt) {
		t.Errorf("last_used_at = %v, want it after %v", after.LastUsedAt, before.LastUsedAt)
	}
}

// What the room was made from decides whether it is the room: a changed
// command, setting, egress or image gives the owner a new one, and the old is
// deleted, not left running.
func TestAChangedSpecReplacesTheRoom(t *testing.T) {
	t.Parallel()

	changes := map[string]func(*keeper.Want){
		"argv":   func(w *keeper.Want) { w.Argv = append(w.Argv, "--verbose") },
		"env":    func(w *keeper.Want) { w.Env = map[string]string{"IMAP_HOST": "other.example"} },
		"secret": func(w *keeper.Want) { w.Secrets = map[string]string{"EMAIL_PASS": "rotated"} },
		"egress": func(w *keeper.Want) { w.Egress = nil },
		"image":  func(w *keeper.Want) { w.Image = "registry.example/other:1" },
		"port":   func(w *keeper.Want) { w.Port = 9999 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			k := w.keeper()
			w.ensure(k, want())
			old := rowOf(t, w)

			changed := want()
			change(&changed)
			ep := w.ensure(k, changed)

			if call(t, ep) != http.StatusOK {
				t.Error("the new room does not accept its endpoint")
			}
			if w.rooms.Has(old.RoomID) {
				t.Errorf("the old room %s is still there", old.RoomID)
			}
			if now := rowOf(t, w); now.RoomID == old.RoomID || now.ID == old.ID || now.SpecHash == old.SpecHash {
				t.Errorf("row = %+v, want a new room, row and hash", now)
			}
			// Its token went with it: a fresh one, and the old tombstoned.
			if _, err := w.secrets.Get(t.Context(), secrets.RoomPath(w.team, rowOf(t, w).ID), keeper.TokenKey); err != nil {
				t.Errorf("the new room's token: %v", err)
			}
			if n := countRows(t, w, `SELECT count(*) FROM deleted_secrets WHERE path = $1`, secrets.RoomPath(w.team, old.ID)); n != 1 {
				t.Errorf("the old token has %d tombstones, want 1", n)
			}
		})
	}
}

// A secret reaches the room's environment beside the settings, and the token
// is still the room's own.
func TestASecretReachesTheRoom(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	ws := want()
	ws.Secrets = map[string]string{"EMAIL_PASS": "hunter2", tokenEnv: "forged"}
	w.ensure(k, ws)

	created := w.rooms.Created()
	if len(created) != 1 {
		t.Fatalf("created %d rooms, want 1", len(created))
	}
	env := created[0].Env
	if env["EMAIL_PASS"] != "hunter2" || env["IMAP_HOST"] != "imap.example" {
		t.Errorf("env = %v, want the secret beside the settings", env)
	}
	if env[tokenEnv] == "forged" {
		t.Error("a secret named like the token replaced it")
	}
}

// The stored hash holds the secrets keyed by each room's own token, so two
// rooms made from the same settings and password store different hashes, and
// the database cannot be used to test a guess at the password.
func TestTheStoredHashIsKeyedByTheRoom(t *testing.T) {
	t.Parallel()

	ws := want()
	ws.Secrets = map[string]string{"EMAIL_PASS": "hunter2"}
	a, b := newWorld(t), newWorld(t)
	a.ensure(a.keeper(), ws)
	b.ensure(b.keeper(), ws)

	if rowOf(t, a).SpecHash == rowOf(t, b).SpecHash {
		t.Error("two rooms with the same secret stored the same hash")
	}
}

// A room the provider lost is not an error: the next call makes another.
func TestARoomTheProviderLostIsRecreated(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	lost := rowOf(t, w)
	w.rooms.Lose(lost.RoomID)

	ep := w.ensure(k, want())

	if call(t, ep) != http.StatusOK {
		t.Error("the new room does not accept its endpoint")
	}
	if now := rowOf(t, w); now.RoomID == lost.RoomID {
		t.Error("the row still names the lost room")
	}
	if n := len(w.rooms.Created()); n != 2 {
		t.Errorf("%d rooms were made, want 2", n)
	}
}

// A room that is there and does not answer, because its command exited, is
// replaced and the broken one deleted.
func TestARoomThatStoppedAnsweringIsReplaced(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	broken := rowOf(t, w)
	w.rooms.Break(broken.RoomID)

	ep := w.ensure(k, want())

	if call(t, ep) != http.StatusOK {
		t.Error("the new room does not accept its endpoint")
	}
	if w.rooms.Has(broken.RoomID) {
		t.Error("the broken room was left running")
	}
}

// A room whose token the secret store lost cannot be called, and is replaced.
func TestARoomWhoseTokenIsGoneIsReplaced(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	old := rowOf(t, w)
	if err := w.secrets.Delete(t.Context(), secrets.RoomPath(w.team, old.ID)); err != nil {
		t.Fatal(err)
	}

	ep := w.ensure(k, want())

	if call(t, ep) != http.StatusOK {
		t.Error("the new room does not accept its endpoint")
	}
	if rowOf(t, w).ID == old.ID {
		t.Error("the row was kept")
	}
}

// Two brains, as two keepers on one database, ask at once for a server that
// has no room. The first to hold the lock makes it; the other waits for it and
// finds it. Creation is held long enough that both are inside Ensure together.
func TestTwoBrainsMakeOneRoom(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.rooms.CreateDelay = 300 * time.Millisecond
	a, b := w.keeper(), w.keeper()

	urls := make([]string, 2)
	var wg sync.WaitGroup
	for i, k := range []*keeper.Keeper{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ep, err := k.Ensure(t.Context(), w.owner, w.team, want())
			if err != nil {
				t.Errorf("Ensure: %v", err)
				return
			}
			urls[i] = ep.URL
		}()
	}
	wg.Wait()

	if n := len(w.rooms.Created()); n != 1 {
		t.Errorf("%d rooms were made, want 1", n)
	}
	if urls[0] == "" || urls[0] != urls[1] {
		t.Errorf("urls = %v, want the same room", urls)
	}
}

// Callers in one brain wait for each other in the process, not at the
// database, where each would hold a pooled connection while it waited.
type countingStore struct {
	*store.Store
	in, peak atomic.Int64
}

func (c *countingStore) WithRoomLock(ctx context.Context, kind string, owner uuid.UUID, fn func(context.Context) error) error {
	n := c.in.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer c.in.Add(-1)
	return c.Store.WithRoomLock(ctx, kind, owner, fn)
}

func TestCallersInOneBrainWaitInTheProcess(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.rooms.CreateDelay = 100 * time.Millisecond
	cs := &countingStore{Store: w.store}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := keeper.New(cs, w.secrets, func(sandbox.Provider, string, string) (sandbox.Rooms, error) { return w.rooms, nil },
		http.DefaultClient, keeper.Images{}, logger).Starting(2*time.Second, 10*time.Millisecond)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := k.Ensure(t.Context(), w.owner, w.team, want()); err != nil {
				t.Errorf("Ensure: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := len(w.rooms.Created()); n != 1 {
		t.Errorf("%d rooms were made, want 1", n)
	}
	if p := cs.peak.Load(); p != 1 {
		t.Errorf("%d callers held a database lock connection at once, want 1", p)
	}
}

// A caller that gives up while it waits for another's cold start returns its
// own context's error and leaves the room being made alone.
func TestACallerThatGivesUpWhileWaitingReturnsItsContextError(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.rooms.CreateDelay = 500 * time.Millisecond
	k := w.keeper()

	done := make(chan error, 1)
	go func() {
		_, err := k.Ensure(t.Context(), w.owner, w.team, want())
		done <- err
	}()
	// Wait until the first caller is inside Create.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		if w.rooms.CreateDelay > 0 && countRows(t, w, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`) > 0 {
			break
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := k.Ensure(ctx, w.owner, w.team, want())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the caller's own deadline", err)
	}
	if err := <-done; err != nil {
		t.Errorf("the first caller: %v", err)
	}
	if n := len(w.rooms.Created()); n != 1 {
		t.Errorf("%d rooms were made, want 1", n)
	}
}

// A room that never answers is deleted when the start times out, with its
// token, and nothing is saved: a failed start leaves nothing behind.
func TestARoomThatNeverAnswersIsDeletedAndTheCallFails(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.rooms.NeverHealthy = true
	k := w.keeper().Starting(200*time.Millisecond, 10*time.Millisecond)

	_, err := k.Ensure(t.Context(), w.owner, w.team, want())

	if !errors.Is(err, sandbox.ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if w.rooms.Live() != 0 {
		t.Errorf("%d rooms are still there, want the one that failed deleted", w.rooms.Live())
	}
	if n := countRows(t, w, `SELECT count(*) FROM sandbox_rooms`); n != 0 {
		t.Errorf("%d rows were saved, want none", n)
	}
	// The token it wrote is tombstoned for the sweep.
	if n := countRows(t, w, `SELECT count(*) FROM deleted_secrets WHERE path LIKE $1`, "teams/"+w.team.String()+"/sandbox/rooms/%"); n != 1 {
		t.Errorf("%d tombstoned tokens, want 1", n)
	}
}

// A start the provider refuses, or a command it cannot start, leaves nothing.
func TestAFailedStartLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	t.Run("create refused", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.rooms.CreateErr = sandbox.ErrRefused
		_, err := w.keeper().Ensure(t.Context(), w.owner, w.team, want())
		if !errors.Is(err, sandbox.ErrRefused) {
			t.Errorf("err = %v, want ErrRefused", err)
		}
		if n := countRows(t, w, `SELECT count(*) FROM deleted_secrets`); n != 1 {
			t.Errorf("%d tombstoned tokens, want the one it wrote", n)
		}
	})
	t.Run("start fails", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.rooms.StartErr = errors.New("no such command")
		if _, err := w.keeper().Ensure(t.Context(), w.owner, w.team, want()); err == nil {
			t.Fatal("no error")
		}
		if w.rooms.Live() != 0 {
			t.Errorf("%d rooms left running", w.rooms.Live())
		}
	})
}

// A team with no sandbox is told so, and nothing is made.
func TestATeamWithNoSandboxIsToldToChooseOne(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	if _, err := w.store.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: w.team, SearchProvider: "none", CompactionMessages: 100, CompactionTokensMin: 50_000, CompactionTokensMax: 60_000, SandboxProvider: "none", BrowserProvider: "none",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := w.keeper().Ensure(t.Context(), w.owner, w.team, want())

	if !errors.Is(err, sandbox.ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
	if len(w.rooms.Created()) != 0 {
		t.Error("a room was made")
	}
}

// A team that never saved settings has no sandbox either.
func TestATeamWithNoSettingsHasNoSandbox(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	other, err := w.store.CreateTeam(t.Context(), "unsaved")
	if err != nil {
		t.Fatal(err)
	}

	_, err = w.keeper().Ensure(t.Context(), w.owner, other.ID, want())

	if !errors.Is(err, sandbox.ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

// The room is made at the team's current provider and key: a team that moved to
// another is not handed the room at the first.
func TestARoomAtAnotherProviderIsNotReused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	old := rowOf(t, w)
	// Writing the row back as the old provider's, as another brain that
	// still had the old settings would have: the settings trigger cannot be
	// what catches this.
	w.useSandbox("sandboxd", "https://other.example")
	var stale uuid.UUID
	if err := w.store.Pool().QueryRow(t.Context(),
		`INSERT INTO sandbox_rooms (team_id, owner_kind, owner_id, provider, provider_url, room_id, spec_hash, sandbox_secret_id)
		 VALUES ($1, 'mcp_server', $2, 'sandboxd', 'https://sbx.example', $3, $4, $5)
		 ON CONFLICT (owner_kind, owner_id) DO UPDATE SET provider = 'sandboxd', room_id = EXCLUDED.room_id,
		   spec_hash = EXCLUDED.spec_hash, sandbox_secret_id = EXCLUDED.sandbox_secret_id
		 RETURNING id`,
		w.team, w.owner.ID, old.RoomID, old.SpecHash, old.SandboxSecretID).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	// A room whose token is there and which answers: only its provider and
	// key say it is the wrong one.
	if err := w.secrets.Put(t.Context(), secrets.RoomPath(w.team, stale), keeper.TokenKey, "t"); err != nil {
		t.Fatal(err)
	}

	w.ensure(k, want())

	if n := len(w.rooms.Created()); n != 2 {
		t.Errorf("%d rooms were made, want a new one at the new provider", n)
	}
}

// Images: the brain chooses, and a team names one only to E2B.
func TestTheBrainChoosesTheRoomImage(t *testing.T) {
	t.Parallel()

	commit := keeper.Images{Commit: "abc1234"}
	latest := keeper.Images{}
	override := keeper.Images{Override: "registry.example/room:7", Commit: "abc1234"}

	for name, tc := range map[string]struct {
		images   keeper.Images
		provider sandbox.Provider
		template string
		want     string
	}{
		"sandboxd, built from a commit": {commit, sandbox.Sandboxd, "", "ghcr.io/laplacianai/openarity-sandbox:sha-abc1234"},
		"sandboxd, no commit":           {latest, sandbox.Sandboxd, "", "ghcr.io/laplacianai/openarity-sandbox:latest"},
		"daytona says it is an image":   {commit, sandbox.Daytona, "", "image:ghcr.io/laplacianai/openarity-sandbox:sha-abc1234"},
		"the operator's override":       {override, sandbox.Sandboxd, "", "registry.example/room:7"},
		"the override on daytona":       {override, sandbox.Daytona, "", "image:registry.example/room:7"},
		"e2b's default template":        {commit, sandbox.E2B, "", "openarity-sandbox"},
		"e2b's own template":            {commit, sandbox.E2B, "ours", "ours"},
		"e2b ignores the override":      {override, sandbox.E2B, "", "openarity-sandbox"},
	} {
		if got := tc.images.For(tc.provider, tc.template); got != tc.want {
			t.Errorf("%s: image = %q, want %q", name, got, tc.want)
		}
	}
}

// The room starts from the image the brain chose for the team's provider.
func TestARoomStartsFromTheChosenImage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := keeper.New(w.store, w.secrets, func(sandbox.Provider, string, string) (sandbox.Rooms, error) { return w.rooms, nil },
		http.DefaultClient, keeper.Images{Commit: "abc1234"}, logger)

	w.ensure(k, want())

	if got := w.rooms.Created()[0].Image; got != "ghcr.io/laplacianai/openarity-sandbox:sha-abc1234" {
		t.Errorf("image = %q", got)
	}
}

// The labels find a room again at a provider that keeps them.
func TestARoomIsLabelledWithItsOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.ensure(w.keeper(), want())

	labels := w.rooms.Created()[0].Labels
	if labels["openarity-team"] != w.team.String() || labels["openarity-owner"] != w.owner.ID.String() || labels["openarity-owner-kind"] != "mcp_server" {
		t.Errorf("labels = %v", labels)
	}
}
