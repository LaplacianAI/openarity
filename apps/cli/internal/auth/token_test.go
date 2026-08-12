package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// fakeDiscoverer stands in for the brain's /auth/config. It records whether it
// was called, because the whole point of the precedence order is that a token
// the user already has costs no round trip.
type fakeDiscoverer struct {
	config *client.AuthConfig
	err    error
	calls  int
}

func (f *fakeDiscoverer) GetAuthConfigWithResponse(
	_ context.Context, _ ...client.RequestEditorFn,
) (*client.GetAuthConfigResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	response := &client.GetAuthConfigResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200:      f.config,
	}
	if f.config == nil {
		response.HTTPResponse = &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"}
	}
	return response, nil
}

func development() *fakeDiscoverer {
	return &fakeDiscoverer{config: &client.AuthConfig{
		Environment:      client.Development,
		DevTokenAccepted: true,
	}}
}

func production() *fakeDiscoverer {
	return &fakeDiscoverer{config: &client.AuthConfig{
		Environment:      client.Production,
		DevTokenAccepted: false,
	}}
}

func envOf(pairs map[string]string) Env {
	return func(key string) string { return pairs[key] }
}

var noEnv = envOf(nil)

// What the user typed wins. A --token flag that lost to a saved credential
// would make the flag untestable by the person holding it.
func TestTheFlagWinsOverEverything(t *testing.T) {
	t.Parallel()

	api := development()
	env := envOf(map[string]string{"OPENARITY_TOKEN": "from-env", "OPENARITY_DEV_TOKEN": "from-dev"})

	got, err := Resolve(t.Context(), api, "from-flag", "from-file", env)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "from-flag" {
		t.Errorf("Resolve = %q, want the flag", got)
	}
	if api.calls != 0 {
		t.Errorf("the server was asked %d times for a token the caller already had", api.calls)
	}
}

func TestTheEnvironmentWinsOverTheSavedFile(t *testing.T) {
	t.Parallel()

	api := development()
	env := envOf(map[string]string{"OPENARITY_TOKEN": "from-env", "OPENARITY_DEV_TOKEN": "from-dev"})

	got, err := Resolve(t.Context(), api, "", "from-file", env)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "from-env" {
		t.Errorf("Resolve = %q, want the environment", got)
	}
	if api.calls != 0 {
		t.Errorf("the server was asked %d times unnecessarily", api.calls)
	}
}

// The saved token is the normal path for a logged-in user, and it must not
// cost a round trip either.
func TestTheSavedTokenIsUsedWithoutAskingTheServer(t *testing.T) {
	t.Parallel()

	api := development()

	got, err := Resolve(t.Context(), api, "", "from-file", noEnv)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "from-file" {
		t.Errorf("Resolve = %q, want the saved token", got)
	}
	if api.calls != 0 {
		t.Errorf("the server was asked %d times for a saved token", api.calls)
	}
}

// The auto-onboarding path: against a development brain the shared token is
// already in the shell that started it, so there is nothing to copy and no
// login to run.
func TestTheDevelopmentTokenIsPickedUpFromTheEnvironment(t *testing.T) {
	t.Parallel()

	api := development()
	env := envOf(map[string]string{"OPENARITY_DEV_TOKEN": "letmein"})

	got, err := Resolve(t.Context(), api, "", "", env)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "letmein" {
		t.Errorf("Resolve = %q, want the development token", got)
	}
	if api.calls != 1 {
		t.Errorf("the server was asked %d times, want exactly once", api.calls)
	}
}

// The server decides, not the client. A CLI that sent OPENARITY_DEV_TOKEN
// because the variable happened to be set would get a 401 with nothing in it
// explaining why — the variable is routinely left in a shell after the brain
// it belonged to was reconfigured.
func TestTheDevelopmentTokenIsNotSentWhenTheServerRefusesIt(t *testing.T) {
	t.Parallel()

	api := production()
	env := envOf(map[string]string{"OPENARITY_DEV_TOKEN": "letmein"})

	_, err := Resolve(t.Context(), api, "", "", env)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("Resolve error = %v, want ErrNoCredential", err)
	}
}

// The failure a first-time user hits. It has to name the way out, because
// nothing about "unauthorized" tells anyone to run a login.
func TestNoCredentialAnywhereSaysWhatToDo(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.Context(), production(), "", "", noEnv)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("Resolve error = %v, want ErrNoCredential", err)
	}
	if !strings.Contains(err.Error(), "oa login") {
		t.Errorf("the error does not name the command that fixes it: %v", err)
	}
}

// Accepted, but the variable is not set. That is a different problem from
// "this deployment has no shared token", and saying so saves the user
// checking the server's configuration instead of their own shell.
func TestAnAcceptedButMissingDevelopmentTokenIsItsOwnMessage(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.Context(), development(), "", "", noEnv)
	if err == nil {
		t.Fatal("Resolve accepted an empty development token")
	}
	if !strings.Contains(err.Error(), "OPENARITY_DEV_TOKEN") {
		t.Errorf("the error does not name the variable to set: %v", err)
	}
}

// An unreachable brain is not a credential problem, and telling someone to
// log in when the server is down sends them to a page that will not load
// either.
func TestAnUnreachableServerIsNotReportedAsAMissingCredential(t *testing.T) {
	t.Parallel()

	api := &fakeDiscoverer{err: errors.New("dial tcp 127.0.0.1:21120: connect: connection refused")}

	_, err := Resolve(t.Context(), api, "", "", noEnv)
	if err == nil {
		t.Fatal("Resolve succeeded against an unreachable server")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Errorf("a connection failure was reported as a missing credential: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the underlying failure is not in the message: %v", err)
	}
}

// An older brain, or something else entirely on that port, answers 404. The
// CLI must not treat an unparsed body as "no development token" and send the
// user to a login flow that this server may not have either.
func TestAServerWithoutTheEndpointIsReported(t *testing.T) {
	t.Parallel()

	api := &fakeDiscoverer{config: nil}

	_, err := Resolve(t.Context(), api, "", "", noEnv)
	if err == nil {
		t.Fatal("Resolve succeeded against a server that did not answer the question")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Errorf("a missing endpoint was reported as a missing credential: %v", err)
	}
}

// Whitespace is not a token. A pasted value with a trailing newline — which
// is what `cat token.txt` gives — would otherwise reach the Authorization
// header and fail the scheme parse on the server.
func TestSurroundingWhitespaceIsTrimmed(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"trailing newline": "a-token\n",
		"leading space":    "  a-token",
		"both":             "\t a-token \n",
	} {
		got, err := Resolve(t.Context(), development(), in, "", noEnv)
		if err != nil {
			t.Fatalf("%s: Resolve: %v", name, err)
		}
		if got != "a-token" {
			t.Errorf("%s: Resolve = %q, want it trimmed", name, got)
		}
	}
}

// A flag holding only whitespace is empty, not a credential, and must fall
// through to the next source rather than being sent as a blank token.
func TestABlankFlagFallsThrough(t *testing.T) {
	t.Parallel()

	got, err := Resolve(t.Context(), development(), "   ", "from-file", noEnv)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "from-file" {
		t.Errorf("Resolve = %q, want it to fall through to the saved token", got)
	}
}
