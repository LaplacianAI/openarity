package keeper_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// e2bBuilds is E2B's template API: each build it is asked for, and what it
// says of a build's status.
type e2bBuilds struct {
	mu      sync.Mutex
	asked   []string // name image
	keys    []string
	refuse  error
	state   sandbox.BuildState
	reason  string
	readErr error
	n       atomic.Int32
}

func (e *e2bBuilds) BuildTemplate(_ context.Context, name, image string) (sandbox.Build, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, name+" "+image)
	if e.refuse != nil {
		return sandbox.Build{}, e.refuse
	}
	n := e.n.Add(1)
	return sandbox.Build{TemplateID: "tpl", BuildID: fmt.Sprintf("b%d", n)}, nil
}

func (e *e2bBuilds) BuildStatus(context.Context, sandbox.Build) (sandbox.BuildState, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state, e.reason, e.readErr
}

func (e *e2bBuilds) builds() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.asked)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// e2bWorld is a team whose sandbox is E2B, naming no template.
func e2bWorld(t *testing.T) (*world, *e2bBuilds, *keeper.Templates, *clock) {
	t.Helper()
	w := newWorld(t)
	w.useSandbox("e2b", "")
	e := &e2bBuilds{state: sandbox.BuildRunning}
	c := &clock{now: time.Now()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tpl := keeper.NewTemplates(w.store, w.secrets, func(key string) keeper.TemplateClient {
		e.mu.Lock()
		e.keys = append(e.keys, key)
		e.mu.Unlock()
		return e
	}, keeper.Images{Commit: "abc1234"}, logger).At(c.Now)
	return w, e, tpl, c
}

func (w *world) e2bKeeper(tpl *keeper.Templates) *keeper.Keeper {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return keeper.New(w.store, w.secrets, func(sandbox.Provider, string, string) (sandbox.Rooms, error) { return w.rooms, nil },
		http.DefaultClient, keeper.Images{Commit: "abc1234"}, logger).
		Starting(2*time.Second, 10*time.Millisecond).Building(tpl)
}

func (w *world) template(name string) db.E2BTemplate {
	w.t.Helper()
	row, err := w.store.GetE2BTemplate(w.t.Context(), db.GetE2BTemplateParams{TeamID: w.team, SandboxSecretID: w.keyID, Name: name})
	if err != nil {
		w.t.Fatalf("GetE2BTemplate: %v", err)
	}
	return row
}

const builtName = "openarity-sandbox-abc1234"

func TestAFirstE2BRoomAsksForTheTemplateAndWaitsForIt(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	k := w.e2bKeeper(tpl)

	for range 2 {
		if _, err := k.Ensure(t.Context(), w.owner, w.team, want()); !errors.Is(err, sandbox.ErrPreparing) {
			t.Fatalf("Ensure = %v, want ErrPreparing", err)
		}
	}
	if e.builds() != 1 || e.asked[0] != builtName+" ghcr.io/laplacianai/openarity-sandbox:sha-abc1234" {
		t.Fatalf("builds asked = %v, want one of the release's image", e.asked)
	}
	if e.keys[0] != apiKey {
		t.Fatalf("E2B was opened with %q, not the team's key", e.keys[0])
	}
	if len(w.rooms.Created()) != 0 {
		t.Fatal("a room was made while its template was building")
	}
	if row := w.template(builtName); row.Status != keeper.TemplateBuilding || *row.BuildID != "b1" {
		t.Fatalf("row = %+v", row)
	}
}

func TestABuiltTemplateIsWhatTheRoomStartsFrom(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	k := w.e2bKeeper(tpl)
	_, _ = k.Ensure(t.Context(), w.owner, w.team, want())

	e.state = sandbox.BuildReady
	if err := tpl.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if row := w.template(builtName); row.Status != keeper.TemplateReady || row.Error != nil {
		t.Fatalf("row = %+v", row)
	}

	w.ensure(k, want())
	if got := w.rooms.Created()[0].Image; got != builtName {
		t.Fatalf("image = %q, want %q", got, builtName)
	}
	if e.builds() != 1 {
		t.Fatalf("%d builds: a built template was built again", e.builds())
	}
}

func TestAFailedBuildRefusesTheRoomUntilItIsRetried(t *testing.T) {
	t.Parallel()
	w, e, tpl, c := e2bWorld(t)
	k := w.e2bKeeper(tpl)
	_, _ = k.Ensure(t.Context(), w.owner, w.team, want())

	e.state, e.reason = sandbox.BuildFailed, "the image could not be pulled"
	if err := tpl.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}

	_, err := k.Ensure(t.Context(), w.owner, w.team, want())
	if !errors.Is(err, sandbox.ErrRefused) || !strings.Contains(err.Error(), "the image could not be pulled") {
		t.Fatalf("Ensure = %v, want ErrRefused with E2B's reason", err)
	}
	if e.builds() != 1 {
		t.Fatalf("%d builds: a failed build was retried at once", e.builds())
	}

	c.Add(keeper.RetryFailedAfter + time.Minute)
	e.state = sandbox.BuildRunning
	if _, err := k.Ensure(t.Context(), w.owner, w.team, want()); !errors.Is(err, sandbox.ErrPreparing) {
		t.Fatalf("Ensure after the retry wait = %v, want ErrPreparing", err)
	}
	if e.builds() != 2 {
		t.Fatalf("%d builds, want the retry", e.builds())
	}
	if row := w.template(builtName); row.Status != keeper.TemplateBuilding || row.Error != nil || *row.BuildID != "b2" {
		t.Fatalf("row = %+v", row)
	}
}

func TestASaveRetriesAFailedBuildAtOnce(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	e.refuse = errors.New("e2b: POST /v3/templates: 403: the plan has no templates")

	row, err := tpl.Want(t.Context(), w.team, w.keyID, keeper.RetryFailedAfter)
	if err != nil || row.Status != keeper.TemplateFailed || !strings.Contains(*row.Error, "403") || row.BuildID != nil {
		t.Fatalf("a refused build: %+v, %v", row, err)
	}
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, keeper.RetryFailedAfter); err != nil || e.builds() != 1 {
		t.Fatalf("a refused build was retried before its wait: %d builds, %v", e.builds(), err)
	}

	e.refuse = nil
	row, err = tpl.Want(t.Context(), w.team, w.keyID, 0)
	if err != nil || row.Status != keeper.TemplateBuilding || e.builds() != 2 {
		t.Fatalf("a save did not retry: %+v, %d builds, %v", row, e.builds(), err)
	}
}

func TestCallersAtOnceStartOneBuild(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := tpl.Want(t.Context(), w.team, w.keyID, 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if e.builds() != 1 {
		t.Fatalf("%d builds, want 1", e.builds())
	}
}

func TestAKeyFromAnotherAccountBuildsItsOwn(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, 0); err != nil {
		t.Fatal(err)
	}
	w.useSandbox("e2b", "")
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, 0); err != nil {
		t.Fatal(err)
	}
	if e.builds() != 2 {
		t.Fatalf("%d builds: a new key reused the old account's template", e.builds())
	}
}

func TestATeamsOwnTemplateIsNeverBuilt(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	tmpl := "ours"
	_, err := w.store.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: w.team, SearchProvider: "none", CompactionMessages: 100, CompactionTokensMin: 50_000, CompactionTokensMax: 60_000,
		SandboxProvider: "e2b", BrowserProvider: "none", SandboxSecretID: &w.keyID, SandboxTemplate: &tmpl,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.ensure(w.e2bKeeper(tpl), want())
	if e.builds() != 0 || w.rooms.Created()[0].Image != "ours" {
		t.Fatalf("%d builds, image %q", e.builds(), w.rooms.Created()[0].Image)
	}
}

func TestTheSweepRecordsWhatE2BSays(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		state   sandbox.BuildState
		readErr error
		after   time.Duration
		noKey   bool
		status  string
		reason  string
	}{
		"still building":           {state: sandbox.BuildRunning, status: keeper.TemplateBuilding},
		"unreadable is building":   {readErr: errors.New("e2b: 503"), status: keeper.TemplateBuilding},
		"building past its time":   {state: sandbox.BuildRunning, after: keeper.BuildTimeout + time.Minute, status: keeper.TemplateFailed, reason: "longer than"},
		"unreadable past its time": {readErr: errors.New("e2b: 503"), after: keeper.BuildTimeout + time.Minute, status: keeper.TemplateFailed, reason: "longer than"},
		"its key gone":             {noKey: true, status: keeper.TemplateFailed, reason: "key it was built with is gone"},
		"ready past its time":      {state: sandbox.BuildReady, after: keeper.BuildTimeout + time.Minute, status: keeper.TemplateReady},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w, e, tpl, c := e2bWorld(t)
			if _, err := tpl.Want(t.Context(), w.team, w.keyID, 0); err != nil {
				t.Fatal(err)
			}
			e.state, e.readErr = tc.state, tc.readErr
			c.Add(tc.after)
			if tc.noKey {
				if err := w.secrets.Delete(t.Context(), secrets.Path(w.team, secrets.KindSandbox, w.keyID)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tpl.Sweep(t.Context()); err != nil {
				t.Fatal(err)
			}
			row := w.template(builtName)
			if row.Status != tc.status || (tc.reason != "") != (row.Error != nil) ||
				(row.Error != nil && !strings.Contains(*row.Error, tc.reason)) {
				t.Fatalf("row = %s %v, want %s %q", row.Status, row.Error, tc.status, tc.reason)
			}
		})
	}
}

func TestAFinishedBuildDoesNotFinishTheOneThatReplacedIt(t *testing.T) {
	t.Parallel()
	w, _, tpl, _ := e2bWorld(t)
	first, err := tpl.Want(t.Context(), w.team, w.keyID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.FinishE2BTemplate(t.Context(), db.FinishE2BTemplateParams{ID: first.ID, BuildID: first.BuildID, Status: keeper.TemplateFailed, Error: &[]string{"x"}[0]}); err != nil {
		t.Fatal(err)
	}
	second, err := tpl.Want(t.Context(), w.team, w.keyID, 0)
	if err != nil {
		t.Fatal(err)
	}

	n, err := w.store.FinishE2BTemplate(t.Context(), db.FinishE2BTemplateParams{ID: first.ID, BuildID: first.BuildID, Status: keeper.TemplateReady})
	if err != nil || n != 0 {
		t.Fatalf("the old build finished the new one: %d rows, %v", n, err)
	}
	if row := w.template(builtName); row.Status != keeper.TemplateBuilding || *row.BuildID != *second.BuildID {
		t.Fatalf("row = %+v", row)
	}
}

// An upgrade names a new template; rooms keep starting from the one already
// built while it builds, and move to it once it is.
func TestAnUpgradeKeepsRoomsOnTheLastBuiltTemplateWhileItBuilds(t *testing.T) {
	t.Parallel()
	w, e, old, _ := e2bWorld(t)
	if _, err := old.Want(t.Context(), w.team, w.keyID, 0); err != nil {
		t.Fatal(err)
	}
	e.state = sandbox.BuildReady
	if err := old.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}

	e.state = sandbox.BuildRunning
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upgraded := keeper.NewTemplates(w.store, w.secrets, func(string) keeper.TemplateClient { return e },
		keeper.Images{Commit: "def5678"}, logger)

	got, err := upgraded.Image(t.Context(), w.team, w.keyID)
	if err != nil || got != builtName {
		t.Fatalf("during the upgrade's build: %q, %v; want the last built, %q", got, err, builtName)
	}
	if e.builds() != 2 || !strings.HasPrefix(e.asked[1], "openarity-sandbox-def5678 ") {
		t.Fatalf("builds asked = %v, want the upgrade's", e.asked)
	}

	e.state = sandbox.BuildReady
	if err := upgraded.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := upgraded.Image(t.Context(), w.team, w.keyID); err != nil || got != "openarity-sandbox-def5678" {
		t.Fatalf("after the upgrade's build: %q, %v", got, err)
	}
}

// A failed upgrade leaves rooms on what was built before, and says nothing.
func TestAFailedUpgradeKeepsTheLastBuiltTemplate(t *testing.T) {
	t.Parallel()
	w, e, old, _ := e2bWorld(t)
	if _, err := old.Want(t.Context(), w.team, w.keyID, 0); err != nil {
		t.Fatal(err)
	}
	e.state = sandbox.BuildReady
	if err := old.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}

	e.refuse = errors.New("e2b: 500")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upgraded := keeper.NewTemplates(w.store, w.secrets, func(string) keeper.TemplateClient { return e },
		keeper.Images{Commit: "def5678"}, logger)
	if got, err := upgraded.Image(t.Context(), w.team, w.keyID); err != nil || got != builtName {
		t.Fatalf("%q, %v; want the last built", got, err)
	}
}
