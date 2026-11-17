package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain runs this binary as a minimal stdio MCP server when asked: it
// answers initialize, then either waits for stdin to close or exits.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fake-mcp-server" {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(in.Bytes(), &msg)
			if msg.Method == "initialize" {
				os.Stdout.WriteString(`{"jsonrpc":"2.0","id":` + string(msg.ID) +
					`,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n")
				if len(os.Args) > 2 && os.Args[2] == "then-exit" {
					os.Exit(4)
				}
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestParse(t *testing.T) {
	withToken := env(map[string]string{"SHIM_TOKEN": "t"})
	c, err := parse([]string{"shim", "--listen", "127.0.0.1:9", "--", "npx", "--offline", "pkg"}, withToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.role != roleShim || c.listen != "127.0.0.1:9" || c.token != "t" || strings.Join(c.argv, " ") != "npx --offline pkg" {
		t.Errorf("parsed %+v", c)
	}
	if c, _ := parse([]string{"shim", "--", "x"}, withToken); c.listen != ":8080" {
		t.Errorf("default listen = %q", c.listen)
	}

	for _, bad := range []struct {
		name string
		args []string
		env  func(string) string
		says string
	}{
		{"no role", nil, withToken, "usage"},
		{"an unknown role", []string{"launch"}, withToken, "unknown role"},
		{"no command", []string{"shim"}, withToken, "command after --"},
		{"an unknown flag", []string{"shim", "--nope", "--", "x"}, withToken, "nope"},
		{"no token", []string{"shim", "--", "x"}, env(nil), "SHIM_TOKEN"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			_, err := parse(bad.args, bad.env)
			if err == nil || !strings.Contains(err.Error(), bad.says) {
				t.Errorf("error = %v, want one saying %q", err, bad.says)
			}
		})
	}
}

// freeAddr reserves an ephemeral port, as the brain's tests do.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestTheShimServesUntilToldToStop(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"shim", "--listen", addr, "--", os.Args[0], "fake-mcp-server"},
			env(map[string]string{"SHIM_TOKEN": "t"}), &bytes.Buffer{})
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
			t.Fatal("the shim never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("a shim told to stop returned %v", err)
	}
}

func TestTheShimFailsWhenItsServerExits(t *testing.T) {
	err := run(t.Context(), []string{"shim", "--listen", freeAddr(t), "--", os.Args[0], "fake-mcp-server", "then-exit"},
		env(map[string]string{"SHIM_TOKEN": "t"}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("error = %v, want the server's exit", err)
	}
}

func TestTheShimFailsWhenItsServerCannotStart(t *testing.T) {
	err := run(t.Context(), []string{"shim", "--", "/no/such/server"},
		env(map[string]string{"SHIM_TOKEN": "t"}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "starting the MCP server") {
		t.Errorf("error = %v", err)
	}
}
