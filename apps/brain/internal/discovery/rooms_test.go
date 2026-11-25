package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type fakeRooms struct {
	ep    sandbox.Endpoint
	err   error
	delay time.Duration
	asked atomic.Int32
	team  uuid.UUID
}

func (f *fakeRooms) Ensure(ctx context.Context, team uuid.UUID, _ db.McpServer) (sandbox.Endpoint, error) {
	f.asked.Add(1)
	f.team = team
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return sandbox.Endpoint{}, ctx.Err()
	}
	return f.ep, f.err
}

func commandRow() db.McpServer {
	return db.McpServer{ID: uuid.New(), TeamID: uuid.New(), Name: "mail", Command: []string{"npx", "mail"}, Env: []byte("{}")}
}

func roomHeader() http.Header {
	return http.Header{"X-Room": {"1"}, "Authorization": {"Bearer room-token"}}
}

// A command server is discovered at its room: the endpoint the keeper gave,
// with the provider's headers and the room's own token on every request,
// though the address is plain http and a server's own token would be refused
// there.
func TestACommandServerIsDiscoveredAtItsRoom(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil, served{name: "send_mail"})
	rooms := &fakeRooms{ep: sandbox.Endpoint{URL: m.URL, Header: roomHeader()}}
	d := New(&fakeSecrets{}, m.policy(), time.Second).RunsCommandsIn(rooms)
	row := commandRow()

	tools, err := d.Discover(t.Context(), row)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if !slices.Equal(names(tools), []string{"send_mail"}) {
		t.Errorf("tools = %v", names(tools))
	}
	if rooms.team != row.TeamID {
		t.Errorf("the room was asked for team %s, want the server's %s", rooms.team, row.TeamID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.auth) == 0 {
		t.Fatal("nothing reached the room")
	}
	for _, a := range m.auth {
		if a != "Bearer room-token" {
			t.Errorf("Authorization = %q, want the room's token on every request", a)
		}
	}
}

// A run reaches the same room: its address and a client that carries the
// headers.
func TestARunReachesACommandServerAtItsRoom(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil, served{name: "send_mail"})
	rooms := &fakeRooms{ep: sandbox.Endpoint{URL: m.URL + "/mcp", Header: roomHeader()}}
	d := New(&fakeSecrets{}, m.policy(), time.Second).RunsCommandsIn(rooms)

	address, client, err := d.Reach(t.Context(), commandRow())
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}

	if address != m.URL+"/mcp" {
		t.Errorf("address = %q, want the room's", address)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, m.URL, http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// A round tripper does not change the request it was handed.
	if req.Header.Get("Authorization") != "" {
		t.Error("the caller's request was written to")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.auth) != 1 || m.auth[0] != "Bearer room-token" {
		t.Errorf("Authorization = %v, want the room's token", m.auth)
	}
}

// A URL server never asks for a room.
func TestAURLServerNeverAsksForARoom(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil, served{name: "t"})
	rooms := &fakeRooms{}
	d := New(&fakeSecrets{}, m.policy(), time.Second).RunsCommandsIn(rooms)

	if _, err := d.Discover(t.Context(), urlRow(m.URL)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Reach(t.Context(), urlRow(m.URL)); err != nil {
		t.Fatal(err)
	}
	if rooms.asked.Load() != 0 {
		t.Error("a room was asked for")
	}
}

// With nowhere to run it, and for a team with no sandbox, a command server is
// refused with the error that tells them to choose one; any other trouble is
// a room that could not be made, which keeps its cause.
func TestACommandServerWithNoSandboxIsToldSo(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]*Discoverer{
		"no rooms":         New(&fakeSecrets{}, Policy{Allow: allowAll}, time.Second),
		"a team with none": New(&fakeSecrets{}, Policy{Allow: allowAll}, time.Second).RunsCommandsIn(&fakeRooms{err: sandbox.ErrNoProvider}),
	} {
		if _, err := d.Discover(t.Context(), commandRow()); !errors.Is(err, ErrCommandServer) {
			t.Errorf("%s: Discover = %v, want ErrCommandServer", name, err)
		}
		if _, _, err := d.Reach(t.Context(), commandRow()); !errors.Is(err, ErrCommandServer) {
			t.Errorf("%s: Reach = %v, want ErrCommandServer", name, err)
		}
	}
}

func TestARoomThatCouldNotBeMadeKeepsItsCause(t *testing.T) {
	t.Parallel()

	d := New(&fakeSecrets{}, Policy{Allow: allowAll}, time.Second).
		RunsCommandsIn(&fakeRooms{err: sandbox.ErrNotReady})

	_, err := d.Discover(t.Context(), commandRow())

	if !errors.Is(err, ErrRoom) || !errors.Is(err, sandbox.ErrNotReady) || errors.Is(err, ErrCommandServer) {
		t.Errorf("err = %v, want ErrRoom wrapping ErrNotReady", err)
	}
}

// A cold start takes longer than discovery's own timeout allows a server to
// answer in, and is not counted against it: the timeout starts when the room
// is ready.
func TestAColdStartIsNotCountedAgainstTheDiscoveryTimeout(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil, served{name: "send_mail"})
	rooms := &fakeRooms{ep: sandbox.Endpoint{URL: m.URL, Header: roomHeader()}, delay: 400 * time.Millisecond}
	d := New(&fakeSecrets{}, m.policy(), 200*time.Millisecond).RunsCommandsIn(rooms)

	if _, err := d.Discover(t.Context(), commandRow()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
}

// The room's address is held to the same policy as every other: a provider's
// address that is not allowed is refused, and nothing is sent to it.
func TestARoomAtAForbiddenAddressIsRefused(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil, served{name: "send_mail"})
	rooms := &fakeRooms{ep: sandbox.Endpoint{URL: m.URL, Header: roomHeader()}}
	d := New(&fakeSecrets{}, Policy{Allow: func(netip.Addr) bool { return false }}, time.Second).RunsCommandsIn(rooms)

	_, err := d.Discover(t.Context(), commandRow())

	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if m.hits.Load() != 0 {
		t.Error("a request reached the room")
	}
}

// The plain client dials only what the policy allows and carries nothing.
func TestThePlainClientHoldsThePolicy(t *testing.T) {
	t.Parallel()

	m := newMCPServer(t, false, nil)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, m.URL, http.NoBody)

	refused, err := PlainClient(Policy{Allow: func(netip.Addr) bool { return false }}).Do(req)
	if refused != nil {
		_ = refused.Body.Close()
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	resp, err := PlainClient(m.policy()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// The room client dials only what the policy allows, takes no redirect, and
// reads a body whole past the plain client's 8 MiB: a room's file is up to
// 25 MiB, and its reader bounds it.
func TestTheRoomClientHoldsThePolicyAndReadsAFileWhole(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 9<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			http.Redirect(w, r, "/file", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, big)
	}))
	t.Cleanup(srv.Close)
	allow := Policy{Allow: func(netip.Addr) bool { return true }}
	get := func(c *http.Client, path string) (string, error) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, http.NoBody)
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if _, err := get(RoomClient(Policy{Allow: func(netip.Addr) bool { return false }}), "/file"); !errors.Is(err, ErrRefused) {
		t.Errorf("a refused address = %v, want ErrRefused", err)
	}
	if _, err := get(RoomClient(allow), "/elsewhere"); !errors.Is(err, ErrRefused) {
		t.Errorf("a redirect = %v, want ErrRefused", err)
	}
	if got, err := get(RoomClient(allow), "/file"); err != nil || len(got) != len(big) {
		t.Errorf("a 9 MiB body = %d bytes, %v", len(got), err)
	}
}
