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

	"github.com/google/uuid"

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
	sizes   []*sandbox.Size
	keys    []string
	refuse  error
	state   sandbox.BuildState
	reason  string
	readErr error
	n       atomic.Int32
}

func (e *e2bBuilds) BuildTemplate(_ context.Context, name, image string, size *sandbox.Size) (sandbox.Build, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, name+" "+image)
	e.sizes = append(e.sizes, size)
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
	if !errors.Is(err, sandbox.ErrRefused) || errors.Is(err, sandbox.ErrPreparing) ||
		!strings.Contains(err.Error(), "the image could not be pulled") || !strings.Contains(err.Error(), "in Settings") {
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

	row, err := tpl.Want(t.Context(), w.team, w.keyID, nil, keeper.RetryFailedAfter)
	if err != nil || row.Status != keeper.TemplateFailed || !strings.Contains(*row.Error, "403") || row.BuildID != nil {
		t.Fatalf("a refused build: %+v, %v", row, err)
	}
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, nil, keeper.RetryFailedAfter); err != nil || e.builds() != 1 {
		t.Fatalf("a refused build was retried before its wait: %d builds, %v", e.builds(), err)
	}

	e.refuse = nil
	row, err = tpl.Want(t.Context(), w.team, w.keyID, nil, 0)
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
			if _, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
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
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
		t.Fatal(err)
	}
	w.useSandbox("e2b", "")
	if _, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
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

// A team's own template was built at a size of its own, so the team's size
// is not the room's, and changing it does not replace the room.
func TestATeamsOwnTemplateKeepsItsOwnSize(t *testing.T) {
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
	w.sized(8192, 4, 20480)
	k := w.e2bKeeper(tpl)
	first := w.ensure(k, workspace())
	if got := w.rooms.Created()[0].Size; got != nil || e.builds() != 0 {
		t.Fatalf("size = %+v, %d builds; want none", got, e.builds())
	}

	w.sized(2048, 1, 4096)
	if again := w.ensure(k, workspace()); again.URL != first.URL || len(w.rooms.Created()) != 1 {
		t.Fatalf("a size change replaced the room made from the team's own template: %s, then %s", first.URL, again.URL)
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
			if _, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
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
	first, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.FinishE2BTemplate(t.Context(), db.FinishE2BTemplateParams{ID: first.ID, BuildID: first.BuildID, Status: keeper.TemplateFailed, Error: &[]string{"x"}[0]}); err != nil {
		t.Fatal(err)
	}
	second, err := tpl.Want(t.Context(), w.team, w.keyID, nil, 0)
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
	if _, err := old.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
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

	got, err := upgraded.Image(t.Context(), w.team, w.keyID, nil)
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
	if got, err := upgraded.Image(t.Context(), w.team, w.keyID, nil); err != nil || got != "openarity-sandbox-def5678" {
		t.Fatalf("after the upgrade's build: %q, %v", got, err)
	}
}

// A failed upgrade leaves rooms on what was built before, and says nothing.
func TestAFailedUpgradeKeepsTheLastBuiltTemplate(t *testing.T) {
	t.Parallel()
	w, e, old, _ := e2bWorld(t)
	if _, err := old.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
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
	if got, err := upgraded.Image(t.Context(), w.team, w.keyID, nil); err != nil || got != builtName {
		t.Fatalf("%q, %v; want the last built", got, err)
	}
}

// A failed upgrade is logged once, not on each room's start.
func TestAFailedUpgradeIsLoggedOnce(t *testing.T) {
	t.Parallel()
	w, e, old, _ := e2bWorld(t)
	if _, err := old.Want(t.Context(), w.team, w.keyID, nil, 0); err != nil {
		t.Fatal(err)
	}
	e.state = sandbox.BuildReady
	if err := old.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}

	e.refuse = errors.New("e2b: 500")
	var logs syncBuffer
	upgraded := keeper.NewTemplates(w.store, w.secrets, func(string) keeper.TemplateClient { return e },
		keeper.Images{Commit: "def5678"}, slog.New(slog.NewTextHandler(&logs, nil)))
	for range 3 {
		if _, err := upgraded.Image(t.Context(), w.team, w.keyID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "rooms start from the last one built"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, logs.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var teamSize = sandbox.Size{MemoryMB: 8192, CPUs: 4, DiskMB: 20480}

const sizedName = builtName + "-4c-8192m-20480d"

// A team's own size is in its template's name; the default keeps the name it
// had, so templates already built stay in use.
func TestASizedTemplateIsNamedForItsSize(t *testing.T) {
	t.Parallel()
	i := keeper.Images{Commit: "abc1234"}
	if got := i.E2BName(nil); got != builtName {
		t.Fatalf("default = %q, want %q", got, builtName)
	}
	if got := i.E2BName(&teamSize); got != sizedName {
		t.Fatalf("sized = %q, want %q", got, sizedName)
	}
}

// A workspace starts from the template built at its team's size, asked for
// at that size; a command server from the default.
func TestAWorkspaceWaitsForItsTeamsSizedTemplate(t *testing.T) {
	t.Parallel()
	w, e, tpl, _ := e2bWorld(t)
	w.sized(int32(teamSize.MemoryMB), int32(teamSize.CPUs), int32(teamSize.DiskMB))
	k := w.e2bKeeper(tpl)

	if _, err := k.Ensure(t.Context(), w.owner, w.team, workspace()); !errors.Is(err, sandbox.ErrPreparing) {
		t.Fatalf("workspace Ensure = %v, want ErrPreparing", err)
	}
	if e.builds() != 1 || !strings.HasPrefix(e.asked[0], sizedName+" ") || e.sizes[0] == nil || *e.sizes[0] != teamSize {
		t.Fatalf("asked %v at %v, want the team's size", e.asked, e.sizes)
	}
	if row := w.template(sizedName); row.Size != "4c-8192m-20480d" {
		t.Fatalf("row size = %q", row.Size)
	}

	server := w.owner
	server.ID = uuid.New()
	if _, err := k.Ensure(t.Context(), server, w.team, want()); !errors.Is(err, sandbox.ErrPreparing) {
		t.Fatalf("server Ensure = %v, want ErrPreparing", err)
	}
	if e.builds() != 2 || !strings.HasPrefix(e.asked[1], builtName+" ") || e.sizes[1] != nil {
		t.Fatalf("asked %v at %v, want the default for a command server", e.asked, e.sizes)
	}
}

// While an upgrade's template builds, a room falls back to an older one of
// its own size only: never the default for a sized workspace, which would
// give it the wrong memory and disk.
func TestAnUpgradeFallsBackOnlyToATemplateOfTheSameSize(t *testing.T) {
	t.Parallel()
	w, e, old, _ := e2bWorld(t)
	for _, size := range []*sandbox.Size{nil, &teamSize} {
		if _, err := old.Want(t.Context(), w.team, w.keyID, size, 0); err != nil {
			t.Fatal(err)
		}
	}
	e.state = sandbox.BuildReady
	if err := old.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.state = sandbox.BuildRunning
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upgraded := keeper.NewTemplates(w.store, w.secrets, func(string) keeper.TemplateClient { return e },
		keeper.Images{Commit: "def5678"}, logger)

	if got, err := upgraded.Image(t.Context(), w.team, w.keyID, &teamSize); err != nil || got != sizedName {
		t.Fatalf("Image = %q, %v; want the older template of the same size, %q", got, err, sizedName)
	}
	// A size with nothing of its own built waits, though a default is ready.
	other := sandbox.Size{MemoryMB: 1024, CPUs: 1, DiskMB: 2048}
	if got, err := upgraded.Image(t.Context(), w.team, w.keyID, &other); !errors.Is(err, sandbox.ErrPreparing) {
		t.Fatalf("Image at a size never built = %q, %v; want ErrPreparing, not another size's template", got, err)
	}
}
