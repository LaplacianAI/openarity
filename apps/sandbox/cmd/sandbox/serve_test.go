package main

import (
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func serveEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"SANDBOXD_TOKEN":         "t",
		"SANDBOXD_PUBLIC_URL":    "http://sandboxd:9090",
		"SANDBOXD_DOCKER_SOCKET": "/run/docker.sock",
		"HOSTNAME":               "abc123",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestServeParsesItsSettingsAndDefaults(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"serve"}, envOf(serveEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	s := c.serve
	if c.role != roleServe || c.listen != ":9090" || s.self != "abc123" || s.socket != "/run/docker.sock" {
		t.Fatalf("%+v", c)
	}
	if s.limits.Memory != 768<<20 || s.limits.HomeBytes != 512<<20 || s.limits.TmpBytes != 64<<20 ||
		s.limits.PidsLimit != 256 || s.limits.NanoCPUs != 500_000_000 || s.limits.MemoryBudget != 3<<30 {
		t.Fatalf("limits = %+v", s.limits)
	}
}

func TestServeSettingsOverrideTheDefaults(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"serve", "--listen", ":1234"}, envOf(serveEnv(map[string]string{
		"SANDBOXD_CONTAINER": "sbd", "SANDBOXD_MEMORY_BUDGET": "1073741824", "SANDBOXD_ROOM_MEMORY": "134217728",
		"SANDBOXD_ROOM_HOME": "67108864", "SANDBOXD_ROOM_PIDS": "64", "SANDBOXD_ROOM_CPUS": "1.5",
		"SANDBOXD_WORKSPACE_MEMORY": "536870912", "SANDBOXD_WORKSPACE_BYTES": "268435456", "SANDBOXD_WORKSPACE_CPUS": "2",
	})))
	if err != nil {
		t.Fatal(err)
	}
	s := c.serve
	if c.listen != ":1234" || s.self != "sbd" || s.limits.MemoryBudget != 1<<30 || s.limits.Memory != 128<<20 ||
		s.limits.HomeBytes != 64<<20 ||
		s.limits.PidsLimit != 64 || s.limits.NanoCPUs != 1_500_000_000 ||
		s.limits.WorkspaceMemory != 512<<20 || s.limits.WorkspaceBytes != 256<<20 || s.limits.WorkspaceCPUs != 2_000_000_000 {
		t.Fatalf("%+v", c)
	}
}

// The home is a tmpfs, charged to the room's memory: one larger than the room
// could never be filled, and the room would be killed rather than told so.
func TestServeRefusesAHomeLargerThanTheRoom(t *testing.T) {
	t.Parallel()
	_, err := parse([]string{"serve"}, envOf(serveEnv(map[string]string{
		"SANDBOXD_ROOM_MEMORY": "268435456", "SANDBOXD_ROOM_HOME": "536870912",
	})))
	if err == nil || !strings.Contains(err.Error(), "SANDBOXD_ROOM_HOME is more than SANDBOXD_ROOM_MEMORY") {
		t.Errorf("err = %v", err)
	}
}

// /workspace and the home are both tmpfs in a workspace room, both charged to
// its memory; and a workspace must fit the budget for one to be made at all.
func TestServeRefusesAWorkspaceThatCannotFit(t *testing.T) {
	t.Parallel()
	for want, extra := range map[string]map[string]string{
		"SANDBOXD_WORKSPACE_BYTES and SANDBOXD_ROOM_HOME are more than SANDBOXD_WORKSPACE_MEMORY": {
			"SANDBOXD_WORKSPACE_MEMORY": "805306368", "SANDBOXD_WORKSPACE_BYTES": "536870912",
		},
		"SANDBOXD_WORKSPACE_MEMORY is more than SANDBOXD_MEMORY_BUDGET": {
			"SANDBOXD_MEMORY_BUDGET": "1073741824", "SANDBOXD_WORKSPACE_MEMORY": "2147483648",
		},
	} {
		_, err := parse([]string{"serve"}, envOf(serveEnv(extra)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestServeRefusesWhatItCannotRunWith(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		"SANDBOXD_TOKEN":         {"SANDBOXD_TOKEN": ""},
		"SANDBOXD_DOCKER_SOCKET": {"SANDBOXD_DOCKER_SOCKET": ""},
		"SANDBOXD_PUBLIC_URL":    {"SANDBOXD_PUBLIC_URL": "sandboxd:9090"},
		"SANDBOXD_CONTAINER":     {"HOSTNAME": ""},
		"SANDBOXD_ROOM_PIDS":     {"SANDBOXD_ROOM_PIDS": "many"},
		"SANDBOXD_ROOM_MEMORY":   {"SANDBOXD_ROOM_MEMORY": "0"},
		"SANDBOXD_ROOM_HOME":     {"SANDBOXD_ROOM_HOME": "-1"},
		"SANDBOXD_MEMORY_BUDGET": {"SANDBOXD_MEMORY_BUDGET": "1000"},
	}
	for want, extra := range cases {
		_, err := parse([]string{"serve"}, envOf(serveEnv(extra)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	if _, err := parse([]string{"serve", "extra"}, envOf(serveEnv(nil))); err == nil {
		t.Error("stray arguments were accepted")
	}
}

// A socket sandboxd may not open says what to do, rather than restarting
// with a bare "permission denied": on a rootless daemon that is the uid.
func TestASocketThatRefusesTheUserSaysToRunAsZero(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any socket, so nothing is refused")
	}
	dir, err := os.MkdirTemp("", "perm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}

	c, err := parse([]string{"serve", "--listen", "127.0.0.1:0"}, envOf(serveEnv(map[string]string{
		"SANDBOXD_DOCKER_SOCKET": sock,
	})))
	if err != nil {
		t.Fatal(err)
	}
	err = runServe(t.Context(), c, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "run sandboxd as uid 0") || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want a refusal that says to run as uid 0", err)
	}
}
