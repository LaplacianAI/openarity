package watchers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID    = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	watcherID = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
	sessionID = "7d2e8c90-1a2b-4c3d-8e4f-5a6b7c8d9e0f"
	secret    = "whsec_0123456789abcdef"
	hookURL   = "https://brain.example/hooks/watchers/abc"
)

const watchersPath = "/teams/" + teamID + "/watchers"

func watcher(extra string) string {
	return `{"id":"` + watcherID + `","name":"prices","objective":"Tell me what moved.","source":"web_page",
  "url":"https://example.com/prices","every_seconds":7200,"cooldown_seconds":3600,"hub":true,"target":{"kind":"own"},
  "session_id":"` + sessionID + `","status":"active","created_at":"2026-11-28T00:00:00Z","updated_at":"2026-11-28T00:00:00Z"` + extra + `}`
}

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":                       {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"acme","role":"admin"}]}`},
		"GET /teams/{id}/watchers":         {Status: http.StatusOK, Body: `{"items":[` + watcher("") + `]}`},
		"POST /teams/{id}/watchers":        {Status: http.StatusCreated, Body: watcher("")},
		"PATCH /teams/{id}/watchers/{wid}": {Status: http.StatusOK, Body: watcher("")},
		"GET /teams/{id}/watchers/{wid}":   {Status: http.StatusOK, Body: watcher("")},
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func sentJSON(t *testing.T, script *clitest.Transcript, method, path string) string {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method && e.Path == path {
			var body any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("not json: %s", e.Body)
			}
			b, _ := json.Marshal(body)
			return string(b)
		}
	}
	t.Fatalf("no %s %s", method, path)
	return ""
}

func TestCreateSendsEachSourceAsItIs(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"a page": {
			[]string{"--source", "web_page", "--url", "https://example.com/prices", "--selector", "#table", "--every", "2h", "--cooldown", "30m"},
			`{"cooldown_seconds":1800,"every_seconds":7200,"name":"prices","objective":"Tell me what moved.","selector":"#table","source":"web_page","url":"https://example.com/prices"}`,
		},
		"a feed":    {[]string{"--source", "feed", "--url", "https://example.com/feed"}, `{"name":"prices","objective":"Tell me what moved.","source":"feed","url":"https://example.com/feed"}`},
		"a webhook": {[]string{"--source", "webhook"}, `{"name":"prices","objective":"Tell me what moved.","source":"webhook"}`},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			clitest.Seed(t, commands, append([]string{"watchers", "create", "acme", "--name", "prices", "--objective", "Tell me what moved."}, tc.args...)...)
			if got := sentJSON(t, script, http.MethodPost, watchersPath); got != tc.want {
				t.Errorf("sent %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestWhatASourceCannotTakeIsRefusedAndNothingSent(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no source":          {nil, "--source is web_page, feed or webhook"},
		"a page with no url": {[]string{"--source", "web_page"}, "needs --url"},
		"a webhook's url":    {[]string{"--source", "webhook", "--url", "https://x.example"}, "a webhook watcher has no --url"},
		"a feed's selector":  {[]string{"--source", "feed", "--url", "https://x.example/f", "--selector", "p"}, "for a web_page watcher"},
		"a short look":       {[]string{"--source", "feed", "--url", "https://x.example/f", "--every", "30s"}, "whole minutes"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			_, err := clitest.Execute(t, commands, append([]string{"watchers", "create", "acme", "--name", "p", "--objective", "o"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if n := script.Calls(http.MethodPost, watchersPath); n != 0 {
				t.Errorf("%d watchers sent", n)
			}
		})
	}
}

// A webhook watcher's secret comes once, at create; it is printed then, and
// said to be shown once.
func TestAWebhooksSecretIsPrintedOnceAtCreate(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/watchers": {Status: http.StatusCreated, Body: watcher(`,"hook_url":"` + hookURL + `","secret":"` + secret + `"`)},
	}))
	out, err := clitest.Execute(t, commands, "watchers", "create", "acme", "--name", "deploys", "--objective", "o", "--source", "webhook")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{hookURL, secret, "shown this once"} {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
}

// A new secret goes to stdout and nothing else does, so it can be piped to a
// file; what is said about it goes to stderr.
func TestANewSecretIsAllThatReachesStdout(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/watchers/{wid}/secret": {Status: http.StatusCreated, Body: `{"secret":"` + secret + `","url":"` + hookURL + `"}`},
	}))
	var stdout, stderr bytes.Buffer
	root := cli.NewRoot(&stdout, &stderr, func(opts *cli.Options) []*cobra.Command { return []*cobra.Command{New(opts)} })
	root.SetArgs([]string{"watchers", "secret", "acme", watcherID})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(stdout.String())
	if want := []string{"url", hookURL, "secret", secret}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("stdout = %q, want only the url and the secret", stdout.String())
	}
	if !strings.Contains(stderr.String(), "the old one no longer works") || strings.Contains(stderr.String(), secret) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestUpdateSendsOnlyWhatIsGiven(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"clear the selector": {[]string{"update", "acme", watcherID, "--selector", ""}, `{"selector":""}`},
		"how often":          {[]string{"update", "acme", watcherID, "--every", "3h"}, `{"every_seconds":10800}`},
		"the hub":            {[]string{"update", "acme", watcherID, "--hub"}, `{"hub":true}`},
		"pause":              {[]string{"pause", "acme", watcherID}, `{"status":"paused"}`},
		"resume":             {[]string{"resume", "acme", watcherID}, `{"status":"active"}`},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			clitest.Seed(t, commands, append([]string{"watchers"}, tc.args...)...)
			if got := sentJSON(t, script, http.MethodPatch, watchersPath+"/"+watcherID); got != tc.want {
				t.Errorf("sent %s, want %s", got, tc.want)
			}
		})
	}
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "watchers", "update", "acme", watcherID); err == nil ||
		script.Calls(http.MethodPatch, watchersPath+"/"+watcherID) != 0 {
		t.Errorf("an empty update: %v", err)
	}
}

func TestRunChecksConfirmAndDeleteCallTheirRoutes(t *testing.T) {
	base := watchersPath + "/" + watcherID
	for name, tc := range map[string]struct {
		args         []string
		method, path string
		reply        clitest.Reply
	}{
		"run":     {[]string{"run", "acme", watcherID}, http.MethodPost, base + "/run", clitest.Reply{Status: http.StatusOK, Body: watcher("")}},
		"confirm": {[]string{"confirm", "acme", watcherID}, http.MethodPost, base + "/confirm", clitest.Reply{Status: http.StatusOK, Body: watcher("")}},
		"delete":  {[]string{"delete", "acme", watcherID}, http.MethodDelete, base, clitest.Reply{Status: http.StatusNoContent}},
		"checks": {[]string{"checks", "acme", watcherID}, http.MethodGet, base + "/checks", clitest.Reply{
			Status: http.StatusOK,
			Body:   `{"items":[{"checked_at":"2026-11-28T03:30:00Z","outcome":"changed","detail":"price moved"}]}`,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(map[string]clitest.Reply{tc.method + " " + tc.path: tc.reply}))
			clitest.Seed(t, commands, append([]string{"watchers"}, tc.args...)...)
			if n := script.Calls(tc.method, tc.path); n != 1 {
				t.Errorf("%s %s called %d times", tc.method, tc.path, n)
			}
		})
	}
}

// What a page said cannot drive the terminal.
func TestWhatAPageSaidIsMadeSafe(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/watchers/{wid}/checks": {
			Status: http.StatusOK,
			Body:   `{"items":[{"checked_at":"2026-11-28T03:30:00Z","outcome":"changed","detail":"\u001b[2Jprice\u0007"}]}`,
		},
	}))
	out, err := clitest.Execute(t, commands, "watchers", "checks", "acme", watcherID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Errorf("an escape reached the terminal: %q", out)
	}
}

func TestEveryVerbIsRegistered(t *testing.T) {
	root := New(cli.NewOptions(&bytes.Buffer{}, &bytes.Buffer{}))
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"list", "get", "create", "update", "pause", "resume", "delete", "run", "checks", "confirm", "take-over", "secret"} {
		if !have[want] {
			t.Errorf("no %q", want)
		}
	}
}
