// Package clitest drives a command the way a shell does. It takes the command
// as a parameter rather than importing the packages that build them, which is
// what stops a cycle: every command package's test imports this one.
//
// It deliberately cannot prove a command is registered on the real root —
// nothing outside cmd/oa knows the whole list. TestEveryCommandIsRegistered in
// cmd/oa is what covers that.
package clitest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
)

// Build is the New function a command package exposes.
type Build func(*cli.Options) *cobra.Command

// Isolate gives a test its own config directory and keeps it away from the
// machine's keychain. It reads the environment, so a test that calls this
// cannot be t.Parallel().
//
// OPENARITY_NO_KEYCHAIN is the one that matters. Without it, a test that seeds
// a credential writes into the developer's real Keychain on macOS and into
// their Secret Service on Linux — surviving the test, visible to every other
// test, and never cleaned up.
func Isolate(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("OPENARITY_NO_KEYCHAIN", "1")
	t.Setenv("OPENARITY_CONFIG_DIR", dir)
	// Kept as a second line of defence: anything reaching os.UserConfigDir
	// directly lands in the temporary directory too.
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // macOS ignores XDG_CONFIG_HOME
}

// Execute runs args against a root carrying the commands the test asked for,
// including the persistent flags and the settings resolution every command
// depends on. A test declares its own root so a test that genuinely needs two
// commands says so, rather than getting them by accident.
func Execute(t *testing.T, builds []Build, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := cli.NewRoot(&out, &out, func(opts *cli.Options) []*cobra.Command {
		commands := make([]*cobra.Command, 0, len(builds))
		for _, build := range builds {
			commands = append(commands, build(opts))
		}
		return commands
	})
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

// Seed runs a command whose failure would make the assertion below meaningless
// rather than merely wrong.
func Seed(t *testing.T, builds []Build, args ...string) {
	t.Helper()

	if _, err := Execute(t, builds, args...); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
}

// Seen is what a stub brain received. The handler runs on the server's
// goroutine, so every field is read under the mutex after the command returns.
type Seen struct {
	mu     sync.Mutex
	Method string
	Path   string
	Query  string
	Body   string
	Auth   string
}

func (s *Seen) Lock()   { s.mu.Lock() }
func (s *Seen) Unlock() { s.mu.Unlock() }

// BrainStub points oa at a fake brain and hands back what it received. The
// token comes from the environment so no discovery call is made — that path
// belongs to internal/auth and is tested there.
func BrainStub(t *testing.T, status int, response string) *Seen {
	t.Helper()

	got := &Seen{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.mu.Lock()
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Query = r.URL.RawQuery
		got.Auth = r.Header.Get("Authorization")
		if r.Body != nil {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			got.Body = string(buf[:n])
		}
		got.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	Isolate(t)
	t.Setenv("OPENARITY_SERVER", server.URL)
	t.Setenv("OPENARITY_TOKEN", "a-token")

	return got
}
