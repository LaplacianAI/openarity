package rooms

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAllowedMatchesOnABoundaryOfTheReference(t *testing.T) {
	t.Parallel()
	const p = "ghcr.io/laplacianai/openarity-sandbox"
	allowed := []string{
		p, p + ":sha-abc", p + ":latest", p + "@sha256:0123abcd", p + ":1@sha256:0123abcd", p + "/sub:1",
	}
	for _, img := range allowed {
		if !Allowed(img, []string{p}) {
			t.Errorf("%q was refused", img)
		}
	}
	refused := []string{
		p + "-evil:1", p + "x", p + "-evil", p + "_x@sha256:ab", "ghcr.io/laplacianai/openarity-sandbo", "evil/" + p,
		"", "docker.io/library/alpine:3",
	}
	for _, img := range refused {
		if Allowed(img, []string{p}) {
			t.Errorf("%q was allowed", img)
		}
	}
	// a registry's port is not a tag: "localhost" must not allow another host
	if Allowed("localhost:5000/evil", []string{"localhost"}) {
		t.Error("a registry with a port was read as a tag")
	}
	if !Allowed("b/room:1", []string{"a/room", "b/room"}) || Allowed("b/room:1", nil) {
		t.Error("several prefixes, or none")
	}
}

func TestCreateOfAnImageNotAllowedIsRefusedBeforeTheDaemonIsAsked(t *testing.T) {
	t.Parallel()
	s, e, g := service(DefaultLimits())
	// present locally makes no difference
	for _, img := range []string{"evil:1", "room-evil:1", "roomx", "evil/room:1"} {
		_, err := s.Create(t.Context(), Spec{Image: img})
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), img) {
			t.Errorf("Create(%q) = %v, want ErrRefused naming it", img, err)
		}
	}
	if e.calls != 0 || len(e.networks) != 0 || len(g.opened) != 0 {
		t.Fatalf("a refused image reached the daemon: %d calls, %v", e.calls, e.networks)
	}
}

func TestStartPullsAnImageTheDaemonLacks(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	e.absent = map[string]bool{"room:1": true}
	id, err := s.Create(t.Context(), Spec{Image: "room:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.pulled) != 0 {
		t.Fatalf("create pulled: %v", e.pulled)
	}
	if err := s.Start(t.Context(), id, []string{"shim"}); err != nil {
		t.Fatal(err)
	}
	if len(e.pulled) != 1 || e.pulled[0] != "room:1" || e.containers[containerName(id)].Image != "room:1" {
		t.Fatalf("pulled %v, containers %v", e.pulled, e.containers)
	}
}

func TestStartDoesNotPullAnImageTheDaemonHas(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id, _ := s.Create(t.Context(), Spec{Image: "room:1"})
	if err := s.Start(t.Context(), id, []string{"shim"}); err != nil {
		t.Fatal(err)
	}
	if len(e.pulled) != 0 {
		t.Fatalf("pulled %v", e.pulled)
	}
}

func TestAFailedPullFailsTheStartNamingTheImageAndMakesNoContainer(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	e.absent = map[string]bool{"room:1": true}
	e.pullErr = errors.New("unauthorized: denied")
	id, _ := s.Create(t.Context(), Spec{Image: "room:1"})
	err := s.Start(t.Context(), id, []string{"shim"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), `"room:1"`) || !strings.Contains(err.Error(), "unauthorized: denied") {
		t.Fatalf("err = %v", err)
	}
	if len(e.containers) != 0 {
		t.Fatalf("containers = %v", e.containers)
	}
	// and the room is still there to start once the registry is back
	e.pullErr = nil
	if err := s.Start(t.Context(), id, []string{"shim"}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestARoomWhoseImageIsNoLongerAllowedDoesNotStart(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	id, _ := s.Create(t.Context(), Spec{Image: "room:1"})
	s.images = []string{"other"}
	if err := s.Start(t.Context(), id, []string{"shim"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	if len(e.containers) != 0 || len(e.pulled) != 0 {
		t.Fatal("a disallowed image was pulled or run")
	}
}

func TestConcurrentStartsOfOneImagePullItOnce(t *testing.T) {
	t.Parallel()
	const n = 6
	// Room for all six: this is about the pull, not the budget.
	l := DefaultLimits()
	l.MemoryBudget = n * l.Memory
	s, e, _ := service(l)
	e.absent = map[string]bool{"room:1": true}
	e.pullWait = make(chan struct{})
	ids := make([]string, n)
	for i := range ids {
		var err error
		if ids[i], err = s.Create(t.Context(), Spec{Image: "room:1"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Go(func() { errs[i] = s.Start(t.Context(), ids[i], []string{"shim"}) })
	}
	// Starts that would pull on their own have to reach the daemon while the
	// first pull is held, so let them; there is nothing to poll for, because
	// the correct outcome is that nothing more happens.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		started := len(e.pulled)
		e.mu.Unlock()
		if started > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(e.pullWait)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("start %d: %v", i, err)
		}
	}
	if len(e.pulled) != 1 {
		t.Fatalf("pulled %d times: %v", len(e.pulled), e.pulled)
	}
	if len(e.containers) != n {
		t.Fatalf("%d containers", len(e.containers))
	}
}

func TestAWaiterThatGivesUpDoesNotCancelThePullOthersWaitOn(t *testing.T) {
	t.Parallel()
	s, e, _ := service(DefaultLimits())
	e.absent = map[string]bool{"room:1": true}
	e.pullWait = make(chan struct{})
	first, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.ensureImage(first, "room:1") }()
	for {
		e.mu.Lock()
		n := len(e.pulled)
		e.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("the cancelled caller saw success")
	}
	second := make(chan error, 1)
	go func() { second <- s.ensureImage(t.Context(), "room:1") }()
	close(e.pullWait)
	if err := <-second; err != nil {
		t.Fatalf("the second caller: %v", err)
	}
	if len(e.pulled) != 1 {
		t.Fatalf("pulled %v", e.pulled)
	}
}

func TestWarmPullsWhatIsMissingLogsFailuresAndGoesOn(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	s, e, _ := service(DefaultLimits())
	s.logger = slog.New(slog.NewTextHandler(&log, nil))
	e.absent = map[string]bool{"room:1": true, "room:2": true}
	e.pullErr = errors.New("registry down")
	s.Warm(t.Context(), []string{"room:1", "evil:1", "room:2"})
	if len(e.pulled) != 2 {
		t.Fatalf("pulled %v: the failure of the first stopped the rest, or the disallowed one was pulled", e.pulled)
	}
	for _, want := range []string{"warming an image failed", "registry down", "not pulling an image", "evil:1"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
	e.pullErr = nil
	log.Reset()
	s.Warm(t.Context(), []string{"room:1"})
	if !strings.Contains(log.String(), "image ready") {
		t.Fatalf("log = %s", log.String())
	}
}
