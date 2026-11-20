package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWorkspace(t *testing.T) {
	withToken := env(map[string]string{WorkspaceTokenEnv: "t"})
	c, err := parse([]string{"workspace"}, withToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.role != roleWorkspace || c.listen != ":8081" || c.root != "/workspace" || c.token != "t" {
		t.Errorf("defaults %+v", c)
	}
	c, err = parse([]string{"workspace", "--listen", "127.0.0.1:9", "--root", "/w"}, withToken)
	if err != nil || c.listen != "127.0.0.1:9" || c.root != "/w" {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []struct {
		name string
		args []string
		env  func(string) string
		says string
	}{
		{"no token", []string{"workspace"}, env(nil), WorkspaceTokenEnv},
		{"a relative root", []string{"workspace", "--root", "w"}, withToken, "absolute"},
		{"an argument", []string{"workspace", "extra"}, withToken, "no arguments"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if _, err := parse(bad.args, bad.env); err == nil || !strings.Contains(err.Error(), bad.says) {
				t.Errorf("error = %v, want one saying %q", err, bad.says)
			}
		})
	}
}

// The token guards the room from outside; a command run inside it must not
// be able to read it from its own environment.
func TestACommandNeverSeesTheWorkspaceToken(t *testing.T) {
	t.Setenv(WorkspaceTokenEnv, "the-secret-token")
	addr := freeAddr(t)
	root := filepath.Join(t.TempDir(), "workspace")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"workspace", "--listen", addr, "--root", root}, os.Getenv, &bytes.Buffer{})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the workspace never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/exec",
		strings.NewReader(`{"command":"echo \"[${`+WorkspaceTokenEnv+`:-}]\"; env"}`))
	req.Header.Set("Authorization", "Bearer the-secret-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Stdout string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Stdout, "[]\n") || strings.Contains(out.Stdout, "the-secret-token") {
		t.Fatalf("a command saw the token:\n%s", out.Stdout)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("a workspace told to stop returned %v", err)
	}
}
