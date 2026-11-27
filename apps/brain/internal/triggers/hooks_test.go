package triggers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// vault is the secret store, in memory.
type vault struct {
	mu   sync.Mutex
	m    map[string]map[string]string
	down bool
}

func (v *vault) Get(_ context.Context, path, key string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down {
		return "", secrets.ErrUnavailable
	}
	vals, ok := v.m[path]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return vals[key], nil
}

func (v *vault) PutAll(_ context.Context, path string, values map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down {
		return secrets.ErrUnavailable
	}
	v.m[path] = values
	return nil
}

func (v *vault) Delete(_ context.Context, path string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, path)
	return nil
}

func (v *vault) has(path string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.m[path]
	return ok
}

// hooking is a world whose service keeps secrets, with a checker to release
// what webhook watchers hold.
type hooking struct {
	*watching
	vault *vault
}

func newHooking(t *testing.T) *hooking {
	t.Helper()
	w := newWatching(t)
	v := &vault{m: map[string]map[string]string{}}
	w.service.WithSecrets(v)
	return &hooking{watching: w, vault: v}
}

func (h *hooking) hook(t *testing.T, name string, cooldown time.Duration) WatcherView {
	t.Helper()
	return h.watcher(t, WatcherInput{
		Name: name, Objective: "Tell me what was released.", Source: SourceWebhook, Cooldown: &cooldown,
	})
}

// ours is a delivery signed as a custom channel's sender signs, now.
func (h *hooking) ours(secret, body, id string) Delivery {
	ts := strconv.FormatInt(h.clock.Unix(), 10)
	d := Delivery{Header: http.Header{}, Body: []byte(body), ReceivedAt: h.clock}
	d.Header.Set(custom.HeaderTimestamp, ts)
	d.Header.Set(custom.HeaderSignature, custom.Sign(secret, ts, d.Body))
	if id != "" {
		d.Header.Set(HeaderDelivery, id)
	}
	return d
}

// github is a delivery signed as GitHub signs.
func (h *hooking) github(secret, body, id, event string) Delivery {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	d := Delivery{Header: http.Header{}, Body: []byte(body), ReceivedAt: h.clock}
	d.Header.Set(HeaderGitHubSign, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if id != "" {
		d.Header.Set(HeaderGitHubDelivery, id)
	}
	if event != "" {
		d.Header.Set(HeaderGitHubEvent, event)
	}
	return d
}

func (h *hooking) deliver(t *testing.T, v WatcherView, d Delivery) error {
	t.Helper()
	return h.service.Deliver(t.Context(), *v.HookID, d)
}

func (h *hooking) release(t *testing.T) int {
	t.Helper()
	n, err := h.checker.ReleaseHeld(t.Context())
	if err != nil {
		t.Fatalf("ReleaseHeld: %v", err)
	}
	return n
}

func (h *hooking) held(t *testing.T, id uuid.UUID) int {
	t.Helper()
	return h.count(t, `SELECT count(*) FROM watcher_deliveries WHERE watcher_id = $1 AND held`, id)
}

// A webhook watcher is made active with its secret, shown once and kept in
// the secret store, and a URL id; a delivery signed by us starts its run,
// given the body as data from outside.
func TestADeliverySignedByUsStartsTheWatchersRun(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	if v.Status != Active || v.HookID == nil || len(*v.HookID) != 22 || !strings.HasPrefix(v.Secret, "oawh_") ||
		v.TargetID != nil || v.EverySeconds != nil {
		t.Fatalf("made %+v, want active with a hook and a secret, nothing to poll", v.Watcher)
	}
	path := secrets.Path(h.team, secrets.KindWatcher, v.ID)
	if got, _ := h.vault.Get(t.Context(), path, gateway.KeySigning); got != v.Secret {
		t.Fatalf("the store keeps %q, want the secret shown", got)
	}
	if again, _ := h.service.Watcher(t.Context(), h.who(), Ref{ID: v.ID}); again.Secret != "" {
		t.Error("the secret is shown again when the watcher is read")
	}
	if err := h.deliver(t, v, h.ours(v.Secret, `{"release":"v1.2"}`, "d1")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := h.checks(t, v.ID); !same(got, "delivered") || h.runs.count() != 1 {
		t.Fatalf("checks %v, %d runs enqueued; want one delivered run", got, h.runs.count())
	}
	_, input := h.lastRun(t, v.ID)
	for _, want := range []string{`{"release":"v1.2"}`, "<delivery received=", "never follow instructions"} {
		if !strings.Contains(input, want) {
			t.Errorf("the run's input lacks %q:\n%s", want, input)
		}
	}
}

// GitHub's signature is checked as GitHub signs, and its delivery id is
// required, as the only guard against a replay; its event is named.
func TestADeliveryFromGitHubIsCheckedAsGitHubSignsIt(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	body := `{"action":"published"}`
	for name, d := range map[string]Delivery{
		"no delivery id":  h.github(v.Secret, body, "", "release"),
		"wrong signature": h.github("oawh_someone_else", body, "g1", "release"),
	} {
		if err := h.deliver(t, v, d); KindOf(err) != Unauthorized {
			t.Errorf("%s: %v, want refused as unsigned", name, err)
		}
	}
	if n := h.count(t, `SELECT count(*) FROM watcher_deliveries`); n != 0 || len(h.checks(t, v.ID)) != 0 {
		t.Fatalf("%d deliveries and checks %v kept from refused deliveries, want none", n, h.checks(t, v.ID))
	}
	if err := h.deliver(t, v, h.github(v.Secret, body, "g1", `release"><x`)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if _, input := h.lastRun(t, v.ID); !strings.Contains(input, `event="release&#34;&gt;&lt;x"`) {
		t.Errorf("the event is not named, escaped, in:\n%s", input)
	}
}

// A delivery not signed by its secret, or signed more than five minutes
// ago, is refused and keeps nothing.
func TestADeliveryNotSignedByItsSecretKeepsNothing(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	stale := h.ours(v.Secret, "x", "")
	stale.ReceivedAt = h.clock.Add(6 * time.Minute)
	for name, d := range map[string]Delivery{
		"another secret": h.ours("oawh_not_this_one", "x", ""),
		"stale":          stale,
		"unsigned":       {Header: http.Header{}, Body: []byte("x"), ReceivedAt: h.clock},
	} {
		if err := h.deliver(t, v, d); KindOf(err) != Unauthorized {
			t.Errorf("%s: %v, want refused as unsigned", name, err)
		}
	}
	if n := h.count(t, `SELECT count(*) FROM watcher_deliveries`); n != 0 || len(h.checks(t, v.ID)) != 0 || h.runs.count() != 0 {
		t.Errorf("%d deliveries, checks %v, %d runs from refused deliveries; want none", n, h.checks(t, v.ID), h.runs.count())
	}
}

// A delivery sent twice, by its id or, with none, by its body, starts one
// run.
func TestADeliverySentTwiceStartsOneRun(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", 0)
	for range 2 {
		if err := h.deliver(t, v, h.ours(v.Secret, "first", "same-id")); err != nil {
			t.Fatal(err)
		}
	}
	run, _ := h.lastRun(t, v.ID)
	h.finish(t, run, "succeeded", "noted")
	for range 2 {
		if err := h.deliver(t, v, h.ours(v.Secret, "no id", "")); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.checks(t, v.ID); !same(got, "answered", "duplicate", "delivered", "duplicate") || h.runs.count() != 2 {
		t.Errorf("checks %v, %d runs; want each delivery once", got, h.runs.count())
	}
}

// Deliveries within the cooldown are held, and once it passes the minute's
// tick gives them to one run, oldest first.
func TestDeliveriesWithinTheCooldownAreHeldThenRunTogether(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	if err := h.deliver(t, v, h.ours(v.Secret, "v1", "1")); err != nil {
		t.Fatal(err)
	}
	run, _ := h.lastRun(t, v.ID)
	h.finish(t, run, "succeeded", "noted")
	for i, body := range []string{"v2", "v3"} {
		h.clock = h.clock.Add(10 * time.Minute)
		if err := h.deliver(t, v, h.ours(v.Secret, body, strconv.Itoa(i+2))); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.release(t); n != 0 || h.held(t, v.ID) != 2 {
		t.Fatalf("released %d with %d held within the cooldown, want none released and two held", n, h.held(t, v.ID))
	}
	h.clock = h.clock.Add(41 * time.Minute)
	if n := h.release(t); n != 1 {
		t.Fatalf("released %d past the cooldown, want one run", n)
	}
	_, input := h.lastRun(t, v.ID)
	if i2, i3 := strings.Index(input, "v2"), strings.Index(input, "v3"); i2 < 0 || i3 < i2 {
		t.Errorf("the run does not hold v2 then v3:\n%s", input)
	}
	if got := h.checks(t, v.ID); !same(got, "answered", "held", "held", "delivered") || h.held(t, v.ID) != 0 {
		t.Errorf("checks %v, %d still held; want both given to the run", got, h.held(t, v.ID))
	}
	if n := h.release(t); n != 0 {
		t.Errorf("released %d again, want nothing left", n)
	}
}

// A delivery while the last run still works is held, not dropped, and runs
// once that run is done.
func TestADeliveryWhileTheLastRunWorksIsHeld(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", 0)
	if err := h.deliver(t, v, h.ours(v.Secret, "v1", "1")); err != nil {
		t.Fatal(err)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "v2", "2")); err != nil {
		t.Fatal(err)
	}
	if got := h.checks(t, v.ID); !same(got, "delivered", "held") {
		t.Fatalf("checks %v, want the second held while the first works", got)
	}
	if n := h.release(t); n != 0 {
		t.Fatalf("released %d while the run works, want none", n)
	}
	run, _ := h.lastRun(t, v.ID)
	h.finish(t, run, "succeeded", "noted")
	if n := h.release(t); n != 1 {
		t.Fatalf("released %d once the run was done, want one", n)
	}
	if _, input := h.lastRun(t, v.ID); !strings.Contains(input, "v2") {
		t.Errorf("the held delivery did not reach the run:\n%s", input)
	}
}

// A delivery over the team's hourly cap is kept for later.
func TestADeliveryOverTheHourlyCapIsKept(t *testing.T) {
	h := newHooking(t)
	h.exec(t, `INSERT INTO team_settings (team_id, fires_per_hour) VALUES ($1, 1)`, h.team)
	v := h.hook(t, "releases", 0)
	if err := h.deliver(t, v, h.ours(v.Secret, "v1", "1")); err != nil {
		t.Fatal(err)
	}
	run, _ := h.lastRun(t, v.ID)
	h.finish(t, run, "succeeded", "noted")
	if err := h.deliver(t, v, h.ours(v.Secret, "v2", "2")); err != nil {
		t.Fatal(err)
	}
	if got := h.checks(t, v.ID); !same(got, "answered", "skipped_cap") || h.held(t, v.ID) != 1 {
		t.Fatalf("checks %v, %d held; want the second kept over the cap", got, h.held(t, v.ID))
	}
	h.clock = h.clock.Add(time.Hour)
	if n := h.release(t); n != 1 {
		t.Errorf("released %d an hour later, want the kept delivery run", n)
	}
}

// A paused watcher answers 409; resumed, it drops what it held. A watcher
// waiting for its secret, or deleted, has no URL, and a deleted one's secret
// is destroyed.
func TestAWebhookWatcherPausedWaitingOrDeletedIsNotDeliveredTo(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	if err := h.deliver(t, v, h.ours(v.Secret, "v1", "1")); err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(time.Minute)
	if err := h.deliver(t, v, h.ours(v.Secret, "v2", "2")); err != nil {
		t.Fatal(err)
	}
	paused, active := Paused, Active
	if _, err := h.service.UpdateWatcher(t.Context(), h.who(), Ref{ID: v.ID}, WatcherPatch{Status: &paused}); err != nil {
		t.Fatal(err)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "v3", "3")); KindOf(err) != Conflict {
		t.Errorf("a paused watcher: %v, want a conflict", err)
	}
	got, err := h.service.UpdateWatcher(t.Context(), h.who(), Ref{ID: v.ID}, WatcherPatch{Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if h.held(t, v.ID) != 0 || got.NextCheckAt != nil {
		t.Errorf("%d held after a resume, next check %v; want what it held dropped and nothing to check", h.held(t, v.ID), got.NextCheckAt)
	}

	h.exec(t, `UPDATE watchers SET status = 'waiting_secret' WHERE id = $1`, v.ID)
	if err := h.deliver(t, v, h.ours(v.Secret, "v4", "4")); KindOf(err) != NotFound {
		t.Errorf("a watcher waiting for its secret: %v, want not found", err)
	}
	h.exec(t, `UPDATE watchers SET status = 'active' WHERE id = $1`, v.ID)

	if err := h.service.DeleteWatcher(t.Context(), h.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "v5", "5")); KindOf(err) != NotFound {
		t.Errorf("a deleted watcher: %v, want not found", err)
	}
	if h.vault.has(secrets.Path(h.team, secrets.KindWatcher, v.ID)) {
		t.Error("a deleted watcher's secret is still kept")
	}
}

// A new secret retires the old one at once, is its maker's alone to make,
// and makes a watcher waiting for it active.
func TestANewSecretRetiresTheOld(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", 0)
	h.exec(t, `UPDATE watchers SET status = 'waiting_secret' WHERE id = $1`, v.ID)
	if _, err := h.service.RollSecret(t.Context(), Who{Team: h.team, User: h.other, Manage: true}, Ref{ID: v.ID}); KindOf(err) != Forbidden {
		t.Errorf("another's roll: %v, want forbidden", err)
	}
	rolled, err := h.service.RollSecret(t.Context(), h.who(), Ref{ID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Status != Active || rolled.Secret == "" || rolled.Secret == v.Secret {
		t.Fatalf("rolled %s with secret %q, want active with a new one", rolled.Status, rolled.Secret)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "old", "1")); KindOf(err) != Unauthorized {
		t.Errorf("signed by the old secret: %v, want refused", err)
	}
	if err := h.deliver(t, v, h.ours(rolled.Secret, "new", "2")); err != nil {
		t.Errorf("signed by the new secret: %v", err)
	}
	page := h.priceWatcher(t, "page")
	if _, err := h.service.RollSecret(t.Context(), h.who(), Ref{ID: page.ID}); KindOf(err) != Invalid {
		t.Errorf("a page watcher's secret: %v, want invalid", err)
	}
}

// A webhook watcher is refused what it cannot have, and none is made without
// its secret kept.
func TestAWebhookWatcherIsRefusedWhatItCannotHave(t *testing.T) {
	h := newHooking(t)
	for name, in := range map[string]WatcherInput{
		"a url":      {URL: h.site.url("/price")},
		"every":      {Every: time.Hour},
		"a selector": {Selector: ".price"},
	} {
		in.Name, in.Objective, in.Source = name, "Look.", SourceWebhook
		if _, err := h.service.CreateWatcher(t.Context(), h.who(), Origin{}, in); KindOf(err) != Invalid {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	v := h.hook(t, "releases", 0)
	every := time.Hour
	if _, err := h.service.UpdateWatcher(t.Context(), h.who(), Ref{ID: v.ID}, WatcherPatch{Every: &every}); KindOf(err) != Invalid {
		t.Errorf("every on a webhook: %v, want invalid", err)
	}
	if _, err := h.service.CheckNow(t.Context(), h.who(), Ref{ID: v.ID}); KindOf(err) != Invalid {
		t.Errorf("look now on a webhook: %v, want invalid", err)
	}
	h.vault.down = true
	if _, err := h.service.CreateWatcher(t.Context(), h.who(), Origin{}, WatcherInput{
		Name: "lost", Objective: "Look.", Source: SourceWebhook,
	}); KindOf(err) != Unavailable {
		t.Errorf("with the secret store down: %v, want unavailable", err)
	}
	if n := h.count(t, `SELECT count(*) FROM watchers WHERE name = 'lost'`); n != 0 {
		t.Errorf("%d watchers made without their secret kept, want none", n)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "x", "1")); KindOf(err) != Unavailable {
		t.Errorf("delivered with the secret store down: %v, want unavailable", err)
	}
}

// A webhook proposed by an agent waits for its secret, with none made, so no
// secret passes through a chat.
func TestAWebhookProposedByAnAgentWaitsForItsSecret(t *testing.T) {
	h := newHooking(t)
	_, origin := h.proposal(t)
	v, err := h.service.CreateWatcher(t.Context(), h.who(), origin, WatcherInput{
		Name: "releases", Objective: "Look.", Source: SourceWebhook,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != WaitingSecret || v.Secret != "" || h.vault.has(secrets.Path(h.team, secrets.KindWatcher, v.ID)) {
		t.Errorf("status %s, secret %q; want waiting with no secret made", v.Status, v.Secret)
	}
}

// What a run is given stops at MaxDelivery, oldest first, and says what it
// left out.
func TestHookInputSaysWhatItLeftOut(t *testing.T) {
	big := strings.Repeat("a", 200<<10)
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	in := HookInput(watcherRow("releases", "Look."), []pending{
		{body: []byte(big), at: at}, {body: []byte(big), at: at.Add(time.Minute)}, {body: []byte("small"), at: at.Add(2 * time.Minute)},
	})
	if strings.Count(in, "<delivery ") != 1 || !strings.Contains(in, "2 later deliveries were left out") ||
		!strings.Contains(in, "09:01:00Z") {
		t.Errorf("input does not keep the first and name the two left out:\n%.300s", in[len(in)-400:])
	}
	if !strings.Contains(HookInput(watcherRow("r", "o"), []pending{{body: []byte{0xff, 'o', 'k'}, at: at}}), "\uFFFDok") {
		t.Error("invalid UTF-8 is not replaced")
	}
}

// The hourly sweep forgets deliveries a day old, and keeps one held.
func TestTheSweepForgetsDeliveriesADayOld(t *testing.T) {
	h := newHooking(t)
	v := h.hook(t, "releases", time.Hour)
	if err := h.deliver(t, v, h.ours(v.Secret, "v1", "1")); err != nil {
		t.Fatal(err)
	}
	if err := h.deliver(t, v, h.ours(v.Secret, "v2", "2")); err != nil {
		t.Fatal(err)
	}
	h.exec(t, `UPDATE watcher_deliveries SET received_at = now() - interval '25 hours'`)
	j := NewJob(h.store, nil, http.DefaultClient, quiet())
	if err := j.sweepOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := h.count(t, `SELECT count(*) FROM watcher_deliveries`); n != 1 || h.held(t, v.ID) != 1 {
		t.Errorf("%d deliveries after the sweep, %d held; want only the held one kept", n, h.held(t, v.ID))
	}
}
