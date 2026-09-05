package stack

import (
	"net"
	"strconv"
	"testing"
)

// hold occupies a port for the duration of the test and returns it.
func hold(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort(loopback, "0"))
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener returned a %T", ln.Addr())
	}
	return addr.Port
}

func TestAHeldPortIsNotAvailable(t *testing.T) {
	t.Parallel()

	port := hold(t)
	if PortAvailable(t.Context(), port) {
		t.Errorf("PortAvailable(%d) = true while a listener holds it", port)
	}
}

// The failure this exists to prevent: an installed Postgres already owning
// the port the installer wanted, so every later connection reaches a database
// nobody meant to touch. Nothing about that fails loudly, which is why the
// port is chosen by binding rather than by assuming.
func TestPickPortAvoidsAPortSomethingElseHolds(t *testing.T) {
	t.Parallel()

	taken := hold(t)

	got, err := PickPort(t.Context(), taken)
	if err != nil {
		t.Fatalf("PickPort(%d) = %v", taken, err)
	}
	if got == taken {
		t.Fatalf("PickPort(%d) returned the port that is already held", taken)
	}
	if !PortAvailable(t.Context(), got) {
		t.Errorf("PickPort returned %d, which is not free either", got)
	}
}

func TestPickPortKeepsThePreferredOneWhenItIsFree(t *testing.T) {
	t.Parallel()

	free, err := FreePort(t.Context())
	if err != nil {
		t.Fatalf("FreePort(t.Context()) = %v", err)
	}

	got, err := PickPort(t.Context(), free)
	if err != nil {
		t.Fatalf("PickPort(%d) = %v", free, err)
	}
	if got != free {
		t.Errorf("PickPort(%d) = %d, want the preferred port when nothing holds it", free, got)
	}
}

func TestPostgresDoesNotDefaultTo5432(t *testing.T) {
	t.Parallel()

	// Not a style preference. An existing Postgres on 5432 is the common
	// case on a developer's machine, and a personal install that assumes the
	// port is free reaches the wrong database silently.
	if DefaultPostgresPort == 5432 {
		t.Error("the default Postgres port is 5432, which is the one most likely to be taken")
	}
	for _, p := range []int{DefaultAPIPort, DefaultWebhookPort, DefaultDexPort, DefaultPostgresPort} {
		if p < 1024 {
			t.Errorf("port %d is privileged, and a personal install must not need root", p)
		}
	}
}

func TestFreePortReturnsSomethingBindable(t *testing.T) {
	t.Parallel()

	port, err := FreePort(t.Context())
	if err != nil {
		t.Fatalf("FreePort(t.Context()) = %v", err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort(loopback, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("FreePort returned %d, which does not bind: %v", port, err)
	}
	_ = ln.Close()
}
