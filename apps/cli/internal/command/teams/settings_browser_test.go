package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const lightpandaSaved = `{"model":null,
  "search":{"provider":"none","url":null,"key_set":false},
  "sandbox":{"provider":"none","url":null,"key_set":false},
  "browser":{"provider":"lightpanda","url":"ws://lightpanda:9222","key_set":false},
  "improvement":false,"compaction_messages":100,"updated_at":"2026-11-19T09:00:00Z"}`

func browser(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	b, ok := body["browser"].(map[string]any)
	if !ok {
		t.Fatalf("browser = %v, want an object", body["browser"])
	}
	return b
}

// A command that names no browser flag leaves the browser out, so the brain
// keeps it, key and all.
func TestSettingsSetWithoutBrowserFlagsSendsNoBrowser(t *testing.T) {
	script := settingsRoutes(t, lightpandaSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--improvement", "on"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	if b, ok := put(t, script)["browser"]; ok {
		t.Errorf("sent browser %v, want it left out", b)
	}
}

func TestSettingsSetReadsTheBrowserKeyFromStdin(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "bb-key\n",
		"teams", "settings", "set", teamID, "--browser", "browserbase", "--browser-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	body := put(t, script)
	b := browser(t, body)
	if b["provider"] != "browserbase" || b["api_key"] != "bb-key" || b["url"] != nil {
		t.Errorf("browser = %v, want browserbase with the piped key and no url", b)
	}
	if _, ok := search(t, body)["api_key"]; ok {
		t.Error("the browser key was sent as the search key too")
	}
	if _, ok := body["sandbox"]; ok {
		t.Error("a browser change sent a sandbox")
	}
}

// A self-hosted browser at a URL, with no key: --no-browser-key sends an empty
// one, which drops any stored.
func TestASelfHostedBrowserIsSetWithItsURL(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--browser", "cdp", "--browser-url", "ws://chrome:9222", "--no-browser-key"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	b := browser(t, put(t, script))
	if b["provider"] != "cdp" || b["url"] != "ws://chrome:9222" || b["api_key"] != "" {
		t.Errorf("browser = %v, want cdp at its url with an empty key", b)
	}
}

// The URL is the old provider's; a hosted one takes none.
func TestSwitchingBrowserProviderLeavesItsURLBehind(t *testing.T) {
	script := settingsRoutes(t, lightpandaSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "k\n", "teams", "settings", "set", teamID,
		"--browser", "kernel", "--browser-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	if b := browser(t, put(t, script)); b["url"] != nil || b["provider"] != "kernel" {
		t.Errorf("browser = %v, want kernel and no url", b)
	}
}

// Moving the URL alone keeps the provider, and sends no key: the brain asks
// for it again if one was saved for the old URL.
func TestMovingTheBrowserURLKeepsItsProvider(t *testing.T) {
	script := settingsRoutes(t, lightpandaSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--browser-url", "ws://lightpanda-2:9222"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	b := browser(t, put(t, script))
	if b["provider"] != "lightpanda" || b["url"] != "ws://lightpanda-2:9222" {
		t.Errorf("browser = %v, want lightpanda at the new url", b)
	}
	if _, ok := b["api_key"]; ok {
		t.Errorf("sent a key nobody gave: %v", b)
	}
}

func TestSettingsShowNamesTheBrowser(t *testing.T) {
	settingsRoutes(t, lightpandaSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	for _, want := range []string{"lightpanda", "ws://lightpanda:9222", "browser key"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// A brain that predates browsers sends none, and show prints no blank row.
func TestSettingsShowOmitsABrowserTheBrainDoesNotKnow(t *testing.T) {
	settingsRoutes(t, daytonaSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	if strings.Contains(out, "browser") {
		t.Errorf("printed a browser the brain never sent:\n%s", out)
	}
}

func TestBrowserKeyFlagsThatCannotGoTogether(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		says string
	}{
		"a key and no key": {
			[]string{"--browser", "steel", "--browser-key-stdin", "--no-browser-key"}, "contradict",
		},
		"two keys on stdin": {
			[]string{"--sandbox", "e2b", "--sandbox-key-stdin", "--browser", "kernel", "--browser-key-stdin"}, "separate commands",
		},
	} {
		t.Run(name, func(t *testing.T) {
			script := settingsRoutes(t, neverSaved, http.StatusOK)
			_, err := clitest.ExecuteWithStdin(t, commands, "k\n",
				append([]string{"teams", "settings", "set", teamID}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want it to say %q", err, tc.says)
			}
			if script.Calls(http.MethodPut, settingsPath) != 0 {
				t.Error("sent a PUT anyway")
			}
		})
	}
}

func TestAnEmptyStdinIsNotABrowserKey(t *testing.T) {
	settingsRoutes(t, neverSaved, http.StatusOK)

	_, err := clitest.ExecuteWithStdin(t, commands, "\n",
		"teams", "settings", "set", teamID, "--browser", "anchor", "--browser-key-stdin")
	if err == nil || !strings.Contains(err.Error(), "--no-browser-key") {
		t.Fatalf("err = %v, want it to point at --no-browser-key", err)
	}
}
