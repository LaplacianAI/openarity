// Package keeper keeps a team's sandbox rooms: it makes the room something
// needs the first time it is needed, finds it again for every call after, and
// replaces it when what it was made from has changed or the provider has lost
// it. It knows nothing of what runs in a room. A command MCP server is the
// first thing that does, and says what it wants in a Want; agent code and
// skill scripts will say theirs the same way.
//
// A room belongs to one Owner, and there is at most one room per owner.
// Everything that makes a room correct lives here, once: the lock that
// makes concurrent callers wait for one cold start, the hash of what the room
// was made from, the wait for it to answer, and the token that guards it.
// Deleting rooms is the reaper's: the keeper tombstones, it does not sweep
// (internal/reaper has the effect that deletes a room and its token).
package keeper

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// StartTimeout is how long a new room has to answer its health check.
	// npx fetching a package on a cold cache is most of it.
	StartTimeout = 90 * time.Second
	// PollEvery is how often a starting room is asked.
	PollEvery = time.Second
	// probeTimeout bounds one health check, of a room being reused or of
	// one starting.
	probeTimeout = 5 * time.Second

	// TokenKey is the key of a room's token in its secret.
	TokenKey = "token"
	// KeyField is the key of a team's sandbox key in its secret.
	KeyField = "api_key"

	// RoomImage is the repository the room image is published to; a tag is
	// added to it.
	RoomImage = "ghcr.io/laplacianai/openarity-sandbox"
	// E2BTemplate is the template an E2B team's rooms start from when it
	// names none.
	E2BTemplate = "openarity-sandbox"
)

// An Owner is what a room is for: a kind of thing, and which one.
type Owner struct {
	Kind string
	ID   uuid.UUID
}

// A Want is what an owner needs of its room. Changing any of it gives the
// owner a new room.
type Want struct {
	// Image is what to start from, in the provider's own words. Empty is the
	// brain's room image, which every caller so far wants.
	Image string
	// Argv is the command the room runs, in the background.
	Argv []string
	// Env is the environment of the command; the token is added to it.
	Env map[string]string
	// Secrets are more of the environment, whose values are secrets: they
	// reach the room as Env does, but the stored spec hash holds them only as
	// an HMAC keyed by the room's own token, never as a digest anyone with
	// the database could test a guessed password against.
	Secrets map[string]string `json:"-"`
	Egress  []sandbox.HostPort
	// Network and Workspace are the room's, as sandbox.Spec has them. They
	// are left out of the hash when unset, so a room made before they
	// existed is still the one wanted.
	Network   sandbox.Network `json:",omitempty"`
	Workspace bool            `json:",omitempty"`
	// Port is the room's port that is served, Path the part of its address
	// that callers use, and HealthPath what answers 200 when it is ready.
	Port       int
	Path       string
	HealthPath string
	// TokenEnv names the environment variable the room reads its bearer
	// token from. Callers send the token, and the room refuses without it.
	TokenEnv string
}

// Store is the part of the database the keeper uses.
type Store interface {
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	GetSandboxRoom(ctx context.Context, arg db.GetSandboxRoomParams) (db.SandboxRoom, error)
	TouchSandboxRoom(ctx context.Context, id uuid.UUID) error
	ReplaceSandboxRoom(ctx context.Context, old *db.SandboxRoom, arg db.CreateSandboxRoomParams) (db.SandboxRoom, error)
	RecordDeletedSecret(ctx context.Context, arg db.RecordDeletedSecretParams) error
	WithRoomLock(ctx context.Context, kind string, owner uuid.UUID, fn func(context.Context) error) error
}

// Secrets is the secret store the keeper reads a team's key from and keeps
// room tokens in.
type Secrets interface {
	secrets.Store
	secrets.Writer
}

// Connect makes the client for one provider, as a team's key opens it.
type Connect func(p sandbox.Provider, url, key string) (sandbox.Rooms, error)

// Images is the room image the brain chooses for each provider. A team
// never names one; E2B alone starts from a template of the team's own.
type Images struct {
	// Override is the operator's own image, for sandboxd and Daytona.
	Override string
	// Commit is the commit the brain was built from, which names the room
	// image published with it. Empty is not known, and the latest is used.
	Commit string
}

// For is the image a room starts from at a provider; template is the team's
// E2B template, if it named one.
func (i Images) For(p sandbox.Provider, template string) string {
	if p == sandbox.E2B {
		if template != "" {
			return template
		}
		return E2BTemplate
	}

	ref := i.Override
	if ref == "" {
		tag := "latest"
		if i.Commit != "" {
			tag = "sha-" + i.Commit
		}
		ref = RoomImage + ":" + tag
	}
	if p == sandbox.Daytona {
		// Daytona reads a bare name as a snapshot; this prefix says it is an
		// image to build from.
		return "image:" + ref
	}
	return ref
}

// Keeper keeps rooms.
type Keeper struct {
	store   Store
	secrets Secrets
	connect Connect
	probe   *http.Client
	images  Images
	logger  *slog.Logger

	startTimeout time.Duration
	pollEvery    time.Duration

	local keyed
}

// New returns a keeper. probe is the client health checks are made with,
// which decides what a room's address may be: the caller gives it the same
// policy as every other call to an MCP server.
func New(s Store, sec Secrets, connect Connect, probe *http.Client, images Images, logger *slog.Logger) *Keeper {
	if logger == nil {
		panic("keeper: a nil logger")
	}
	return &Keeper{
		store: s, secrets: sec, connect: connect, probe: probe, images: images, logger: logger,
		startTimeout: StartTimeout, pollEvery: PollEvery,
	}
}

// Starting sets how long a new room has to answer, and how often it is
// asked. It is for tests, which cannot wait ninety seconds.
func (k *Keeper) Starting(timeout, every time.Duration) *Keeper {
	k.startTimeout, k.pollEvery = timeout, every
	return k
}

// Ensure returns the endpoint of the owner's room, making the room if there is
// none, or replacing it if it was made from something else or no longer
// answers. Callers for one owner wait for each other, in this brain and in
// every other, so one cold start serves all of them.
func (k *Keeper) Ensure(ctx context.Context, o Owner, team uuid.UUID, w Want) (sandbox.Endpoint, error) {
	// Within a brain a caller waits here and not on the database lock below:
	// that one holds a pooled connection for as long as it is held, so a
	// burst of calls to one cold server would otherwise spend the pool on
	// waiting.
	release, err := k.local.lock(ctx, o.Kind+":"+o.ID.String())
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	defer release()

	var ep sandbox.Endpoint
	err = k.store.WithRoomLock(ctx, o.Kind, o.ID, func(ctx context.Context) error {
		var err error
		ep, err = k.ensure(ctx, o, team, w)
		return err
	})
	return ep, err
}

func (k *Keeper) ensure(ctx context.Context, o Owner, team uuid.UUID, w Want) (sandbox.Endpoint, error) {
	cfg, err := k.settings(ctx, team)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	rooms, err := k.rooms(ctx, team, cfg.provider, cfg.url, cfg.keyID)
	if err != nil {
		return sandbox.Endpoint{}, err
	}

	w.Image = k.image(w, cfg)

	old, err := k.store.GetSandboxRoom(ctx, db.GetSandboxRoomParams{OwnerKind: o.Kind, OwnerID: o.ID})
	var row *db.SandboxRoom
	switch {
	case err == nil:
		row = &old
	case !errors.Is(err, pgx.ErrNoRows):
		return sandbox.Endpoint{}, fmt.Errorf("reading the room: %w", err)
	}

	if row != nil {
		if ep, ok, err := k.reuse(ctx, rooms, row, cfg, team, w); err != nil || ok {
			return ep, err
		}
		k.discard(ctx, rooms, row, cfg)
	}
	return k.start(ctx, rooms, row, o, team, cfg, w)
}

// reuse is the owner's room as it is, when it is the one wanted and it
// answers. A room the provider lost, or that does not answer, is not an
// error: the caller makes another. Any other trouble is, because replacing a
// room on a provider that is only down would pile up rooms.
func (k *Keeper) reuse(
	ctx context.Context, rooms sandbox.Rooms, row *db.SandboxRoom, cfg teamSandbox,
	team uuid.UUID, w Want,
) (sandbox.Endpoint, bool, error) {
	if !cfg.owns(row) {
		return sandbox.Endpoint{}, false, nil
	}

	token, err := k.secrets.Get(ctx, secrets.RoomPath(team, row.ID), TokenKey)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && token == "") {
		return sandbox.Endpoint{}, false, nil
	}
	if err != nil {
		return sandbox.Endpoint{}, false, fmt.Errorf("reading the room's token: %w", err)
	}
	if hash, err := specHash(w, token); err != nil || row.SpecHash != hash {
		return sandbox.Endpoint{}, false, err
	}

	base, err := rooms.URL(ctx, row.RoomID, w.Port)
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		return sandbox.Endpoint{}, false, nil
	case err != nil:
		return sandbox.Endpoint{}, false, fmt.Errorf("finding the room: %w", err)
	}

	if answers := k.healthy(ctx, base, w, token) == nil; !answers {
		k.logger.InfoContext(ctx, "keeper: a room does not answer, replacing it",
			"owner_kind", row.OwnerKind, "owner_id", row.OwnerID, "room", row.RoomID)
		return sandbox.Endpoint{}, false, nil
	}

	if err := k.store.TouchSandboxRoom(ctx, row.ID); err != nil {
		k.logger.WarnContext(ctx, "keeper: a room's use was not recorded",
			"owner_kind", row.OwnerKind, "owner_id", row.OwnerID, "error", err)
	}
	return endpointOf(base, w, token), true, nil
}

// discard deletes a room being replaced, as this brain can when the room is
// at the provider and key the team has now. Otherwise, or when the provider
// refuses, the row's replacement tombstones it and the reaper deletes it with
// the key it was made with.
func (k *Keeper) discard(ctx context.Context, rooms sandbox.Rooms, row *db.SandboxRoom, cfg teamSandbox) {
	if !cfg.owns(row) {
		return
	}
	err := rooms.Delete(ctx, row.RoomID)
	if err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		k.logger.WarnContext(ctx, "keeper: the old room was not deleted, the reaper will",
			"owner_kind", row.OwnerKind, "owner_id", row.OwnerID, "error", err)
	}
}

// start makes a room, runs the command in it and waits for it to answer, then
// saves it in place of old. A room that does not get that far is deleted, and
// so is its token: nothing is left behind by a start that failed.
func (k *Keeper) start(
	ctx context.Context, rooms sandbox.Rooms, old *db.SandboxRoom, o Owner, team uuid.UUID,
	cfg teamSandbox, w Want,
) (sandbox.Endpoint, error) {
	id := uuid.New()
	token, err := newToken()
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	hash, err := specHash(w, token)
	if err != nil {
		return sandbox.Endpoint{}, err
	}

	path := secrets.RoomPath(team, id)
	if err := k.secrets.Put(ctx, path, TokenKey, token); err != nil {
		return sandbox.Endpoint{}, fmt.Errorf("keeping the room's token: %w", err)
	}

	roomID, err := k.create(ctx, rooms, o, team, w, token)
	if err != nil {
		k.forget(ctx, team, path)
		return sandbox.Endpoint{}, err
	}

	ep, err := k.await(ctx, rooms, roomID, w, token)
	if err != nil {
		k.remove(ctx, rooms, roomID)
		k.forget(ctx, team, path)
		return sandbox.Endpoint{}, err
	}

	_, err = k.store.ReplaceSandboxRoom(ctx, old, db.CreateSandboxRoomParams{
		ID: id, TeamID: team, OwnerKind: o.Kind, OwnerID: o.ID,
		Provider: string(cfg.provider), ProviderUrl: cfg.urlPtr(), RoomID: roomID, SpecHash: hash,
		SandboxSecretID: cfg.keyID,
	})
	if err != nil {
		k.remove(ctx, rooms, roomID)
		k.forget(ctx, team, path)
		return sandbox.Endpoint{}, fmt.Errorf("saving the room: %w", err)
	}
	return ep, nil
}

func (k *Keeper) create(ctx context.Context, rooms sandbox.Rooms, o Owner, team uuid.UUID, w Want, token string) (string, error) {
	env := make(map[string]string, len(w.Env)+len(w.Secrets)+1)
	for name, v := range w.Env {
		env[name] = v
	}
	for name, v := range w.Secrets {
		env[name] = v
	}
	// Set last, so nothing a caller passes can replace it.
	env[w.TokenEnv] = token

	roomID, err := rooms.Create(ctx, sandbox.Spec{
		Image: w.Image, Env: env, Egress: w.Egress,
		Network: w.Network, Workspace: w.Workspace,
		Labels: map[string]string{
			"openarity-team":       team.String(),
			"openarity-owner-kind": o.Kind,
			"openarity-owner":      o.ID.String(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("making the room: %w", err)
	}

	if err := rooms.Start(ctx, roomID, w.Argv); err != nil {
		k.remove(ctx, rooms, roomID)
		return "", fmt.Errorf("starting the command in the room: %w", err)
	}
	return roomID, nil
}

// await polls the room until it answers or the start timeout passes.
func (k *Keeper) await(ctx context.Context, rooms sandbox.Rooms, roomID string, w Want, token string) (sandbox.Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, k.startTimeout)
	defer cancel()

	var last error
	for {
		base, err := rooms.URL(ctx, roomID, w.Port)
		if err == nil {
			if last = k.healthy(ctx, base, w, token); last == nil {
				return endpointOf(base, w, token), nil
			}
		} else {
			last = err
		}

		select {
		case <-ctx.Done():
			// The caller going away is theirs to hear, as it was.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return sandbox.Endpoint{}, fmt.Errorf("%w: %w", sandbox.ErrNotReady, last)
			}
			return sandbox.Endpoint{}, ctx.Err()
		case <-time.After(k.pollEvery):
		}
	}
}

// healthy asks the room whether it is ready.
func (k *Keeper) healthy(ctx context.Context, base sandbox.Endpoint, w Want, token string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, join(base.URL, w.HealthPath), http.NoBody)
	if err != nil {
		return err
	}
	for name, vs := range endpointOf(base, w, token).Header {
		req.Header[name] = vs
	}

	resp, err := k.probe.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the room answered %d", resp.StatusCode)
	}
	return nil
}

// remove deletes a room this call made, on a context that survives the call
// being cancelled; the room is useless and costs money.
func (k *Keeper) remove(ctx context.Context, rooms sandbox.Rooms, roomID string) {
	err := rooms.Delete(context.WithoutCancel(ctx), roomID)
	if err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		k.logger.WarnContext(ctx, "keeper: a room that failed to start was not deleted", "error", err)
	}
}

// forget records a token this call wrote and no row names, so the sweep
// deletes it.
func (k *Keeper) forget(ctx context.Context, team uuid.UUID, path string) {
	err := k.store.RecordDeletedSecret(context.WithoutCancel(ctx), db.RecordDeletedSecretParams{Path: path, TeamID: team})
	if err != nil {
		k.logger.ErrorContext(ctx, "keeper: a room's token outlived its failed start", "team_id", team, "error", err)
	}
}

// Delete deletes a room at a provider with the key it was made with, for the
// reaper. A room already gone is not an error; a key that is gone is
// secrets.ErrNotFound, and the reaper decides what that means.
func (k *Keeper) Delete(ctx context.Context, team uuid.UUID, provider sandbox.Provider, url string, keyID uuid.UUID, room string) error {
	rooms, err := k.rooms(ctx, team, provider, url, keyID)
	if err != nil {
		return err
	}
	if err := rooms.Delete(ctx, room); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return err
	}
	return nil
}

// teamSandbox is a team's sandbox settings as the keeper uses them.
type teamSandbox struct {
	provider sandbox.Provider
	url      string
	template string
	keyID    uuid.UUID
}

func (k *Keeper) settings(ctx context.Context, team uuid.UUID) (teamSandbox, error) {
	s, err := k.store.GetTeamSettings(ctx, team)
	if errors.Is(err, pgx.ErrNoRows) {
		return teamSandbox{}, sandbox.ErrNoProvider
	}
	if err != nil {
		return teamSandbox{}, fmt.Errorf("reading the team's sandbox: %w", err)
	}
	// A team with a provider has a key and one with none has not, which the
	// table holds to (team_settings_sandbox_secret_iff_provider), so the key
	// is what says there is a sandbox.
	if s.SandboxSecretID == nil {
		return teamSandbox{}, sandbox.ErrNoProvider
	}

	cfg := teamSandbox{provider: sandbox.Provider(s.SandboxProvider), keyID: *s.SandboxSecretID}
	if s.SandboxUrl != nil {
		cfg.url = *s.SandboxUrl
	}
	if s.SandboxTemplate != nil {
		cfg.template = *s.SandboxTemplate
	}
	return cfg, nil
}

func (c teamSandbox) urlPtr() *string {
	if c.url == "" {
		return nil
	}
	return &c.url
}

// owns is whether a room was made at the provider, address and key the team
// has now.
func (c teamSandbox) owns(r *db.SandboxRoom) bool {
	return r.Provider == string(c.provider) && r.SandboxSecretID == c.keyID &&
		(r.ProviderUrl == nil) == (c.url == "") && (r.ProviderUrl == nil || *r.ProviderUrl == c.url)
}

func (k *Keeper) image(w Want, cfg teamSandbox) string {
	if w.Image != "" {
		return w.Image
	}
	return k.images.For(cfg.provider, cfg.template)
}

// rooms is the client for a provider, with the key a team saved under keyID.
// The key goes to the provider and nowhere else.
func (k *Keeper) rooms(ctx context.Context, team uuid.UUID, p sandbox.Provider, url string, keyID uuid.UUID) (sandbox.Rooms, error) {
	key, err := k.secrets.Get(ctx, secrets.Path(team, secrets.KindSandbox, keyID), KeyField)
	if err != nil {
		return nil, fmt.Errorf("reading the team's sandbox key: %w", err)
	}
	if key == "" {
		return nil, fmt.Errorf("%w: the team's sandbox key is empty", secrets.ErrNotFound)
	}
	return k.connect(p, url, key)
}

// endpointOf is where a room's caller is sent: the provider's address of the
// port with the Path appended, the provider's own headers, and the room's
// token as a bearer.
func endpointOf(base sandbox.Endpoint, w Want, token string) sandbox.Endpoint {
	h := base.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Authorization", "Bearer "+token)
	return sandbox.Endpoint{URL: join(base.URL, w.Path), Header: h}
}

func join(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// specHash is a digest of everything a room is made from but its token. It is
// stored, and a room is reused only while it equals what is wanted now. The
// encoding is JSON of a struct, whose maps encoding/json writes in key order,
// so the same Want always gives the same digest.
//
// The secrets are in it as an HMAC keyed by the room's token, which lives in
// the secret store: a rotated secret still replaces the room, and the
// database alone cannot confirm a guess at one.
func specHash(w Want, token string) (string, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("hashing what the room is made from: %w", err)
	}
	s, err := json.Marshal(w.Secrets)
	if err != nil {
		return "", fmt.Errorf("hashing what the room is made from: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(s)

	h := sha256.New()
	h.Write(b)
	h.Write(mac.Sum(nil))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("making the room's token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// keyed is a mutex per key, for callers within one process.
type keyed struct {
	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	ch   chan struct{} // holds one token while the lock is free
	refs int
}

// lock waits for key's lock, or for the context. The release function it
// returns must be called once.
func (l *keyed) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = map[string]*entry{}
	}
	e, ok := l.entries[key]
	if !ok {
		e = &entry{ch: make(chan struct{}, 1)}
		e.ch <- struct{}{}
		l.entries[key] = e
	}
	e.refs++
	l.mu.Unlock()

	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if e.refs--; e.refs == 0 {
			delete(l.entries, key)
		}
	}

	select {
	case <-e.ch:
		return func() { e.ch <- struct{}{}; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
