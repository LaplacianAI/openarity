package main

import (
	"strings"
	"testing"
)

func TestSandboxdRunsOnlyTheOpenaritySandboxImageByDefault(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"serve"}, envOf(serveEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.serve.images; len(got) != 1 || got[0] != "ghcr.io/laplacianai/openarity-sandbox" || len(c.serve.pull) != 0 {
		t.Fatalf("images %v, pull %v", got, c.serve.pull)
	}
}

func TestImagesAndPullAreReadAsLists(t *testing.T) {
	t.Parallel()
	c, err := parse([]string{"serve"}, envOf(serveEnv(map[string]string{
		"SANDBOXD_IMAGES": "ghcr.io/a/room, localhost:5000/b/room",
		"SANDBOXD_PULL":   "ghcr.io/a/room:1, localhost:5000/b/room@sha256:ab",
	})))
	if err != nil {
		t.Fatal(err)
	}
	s := c.serve
	if len(s.images) != 2 || s.images[1] != "localhost:5000/b/room" || len(s.pull) != 2 || s.pull[0] != "ghcr.io/a/room:1" {
		t.Fatalf("images %v, pull %v", s.images, s.pull)
	}
}

func TestBadImagesAndPullAreRefusedAtStart(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		"empty entry in images":       {"SANDBOXD_IMAGES": "ghcr.io/a/room,"},
		"tag in a prefix":             {"SANDBOXD_IMAGES": "ghcr.io/a/room:1"},
		"digest in a prefix":          {"SANDBOXD_IMAGES": "ghcr.io/a/room@sha256:ab"},
		"trailing slash":              {"SANDBOXD_IMAGES": "ghcr.io/a/"},
		"empty entry in pull":         {"SANDBOXD_PULL": "ghcr.io/laplacianai/openarity-sandbox:1,,"},
		"pull outside the images":     {"SANDBOXD_PULL": "docker.io/library/alpine:3"},
		"pull across the boundary":    {"SANDBOXD_PULL": "ghcr.io/laplacianai/openarity-sandbox-evil:1"},
		"pull of a bare registry":     {"SANDBOXD_PULL": "ghcr.io"},
		"pull with a different image": {"SANDBOXD_IMAGES": "ghcr.io/a/room", "SANDBOXD_PULL": "ghcr.io/laplacianai/openarity-sandbox:1"},
	}
	for name, extra := range cases {
		_, err := parse([]string{"serve"}, envOf(serveEnv(extra)))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "SANDBOXD_") {
			t.Errorf("%s: err = %v does not name the setting", name, err)
		}
	}
}
