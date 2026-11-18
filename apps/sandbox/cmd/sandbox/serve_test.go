package main

import (
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
	if s.limits.Memory != 256<<20 || s.limits.PidsLimit != 256 || s.limits.NanoCPUs != 500_000_000 || s.limits.MemoryBudget != 3<<30 {
		t.Fatalf("limits = %+v", s.limits)
	}
}

func TestServeSettingsOverrideTheDefaults(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"serve", "--listen", ":1234"}, envOf(serveEnv(map[string]string{
		"SANDBOXD_CONTAINER": "sbd", "SANDBOXD_MEMORY_BUDGET": "1073741824", "SANDBOXD_ROOM_MEMORY": "134217728",
		"SANDBOXD_ROOM_PIDS": "64", "SANDBOXD_ROOM_CPUS": "1.5",
	})))
	if err != nil {
		t.Fatal(err)
	}
	s := c.serve
	if c.listen != ":1234" || s.self != "sbd" || s.limits.MemoryBudget != 1<<30 || s.limits.Memory != 128<<20 ||
		s.limits.PidsLimit != 64 || s.limits.NanoCPUs != 1_500_000_000 {
		t.Fatalf("%+v", c)
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
