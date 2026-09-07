package stack

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// The address the stack binds to. 127.0.0.1 rather than localhost or
// 0.0.0.0: Web Crypto only exists in a secure context, so PKCE works at a
// loopback literal and fails at a LAN address — and a personal install has no
// business listening on the network anyway.
const loopback = "127.0.0.1"

// Preferred ports, which are starting points and not guarantees. PickPort
// returns something else whenever one is taken.
const (
	DefaultAPIPort      = 21120
	DefaultWebhookPort  = 21121
	DefaultDexPort      = 5556
	DefaultPostgresPort = 21432
	DefaultMinIOPort    = 21900
)

// PortAvailable reports whether a listener can be opened on the port right
// now. Binding is the only honest test: a port can be held by a process this
// one cannot see, and asking the process table would miss it.
func PortAvailable(ctx context.Context, port int) bool {
	for _, address := range []string{"0.0.0.0:" + strconv.Itoa(port), net.JoinHostPort(loopback, strconv.Itoa(port))} {
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp4", address)
		if err != nil {
			return false
		}
		if err := ln.Close(); err != nil {
			return false
		}
	}
	return true
}

// FreePort asks the operating system for an unused port by binding to zero.
func FreePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(loopback, "0"))
	if err != nil {
		return 0, fmt.Errorf("stack: finding a free port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("stack: listener returned a %T rather than a TCP address", ln.Addr())
	}
	return addr.Port, nil
}

// PickPort returns preferred if it is free and something else if it is not.
//
// Postgres defaults to 21432 here and not 5432 for a reason this repository
// has already paid for twice: a Homebrew Postgres owning 5432 sent both a
// migration and a verification run at the wrong database, and neither failed
// in a way that said so. Choosing a port nobody else conventionally uses, and
// then still checking it, is cheaper than the afternoon that costs.
//
// The port it returns was free a moment ago, not a port it holds. Something
// else can still take it before Postgres binds; setup binds soon after and
// reports the real failure. This narrows the window rather than closing it,
// which is the most a bind test can do.
func PickPort(ctx context.Context, preferred int) (int, error) {
	if PortAvailable(ctx, preferred) {
		return preferred, nil
	}
	return FreePort(ctx)
}
