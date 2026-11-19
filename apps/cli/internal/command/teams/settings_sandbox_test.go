package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const daytonaSaved = `{"model":null,
  "search":{"provider":"none","url":null,"key_set":false},
  "sandbox":{"provider":"daytona","url":"https://daytona.example","key_set":true},
  "learning":false,"compaction_messages":100,"updated_at":"2026-11-18T09:00:00Z"}`

func sandbox(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	s, ok := body["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox = %v, want an object", body["sandbox"])
	}
	return s
}

// A command that names no sandbox flag leaves the sandbox out, so the brain
// keeps it, key and all; sending the stored one back would send no key.
func TestSettingsSetWithoutSandboxFlagsSendsNoSandbox(t *testing.T) {
	script := settingsRoutes(t, daytonaSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--learning", "on"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	if s, ok := put(t, script)["sandbox"]; ok {
		t.Errorf("sent sandbox %v, want it left out", s)
	}
}

func TestSettingsSetReadsTheSandboxKeyFromStdin(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "e2b-key\n",
		"teams", "settings", "set", teamID, "--sandbox", "e2b", "--sandbox-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	body := put(t, script)
	s := sandbox(t, body)
	if s["provider"] != "e2b" || s["api_key"] != "e2b-key" {
		t.Errorf("sandbox = %v, want e2b with the piped key", s)
	}
	if _, ok := search(t, body)["api_key"]; ok {
		t.Error("the sandbox key was sent as the search key too")
	}
}

// The URL is the old provider's, and E2B takes none.
func TestSwitchingSandboxProviderLeavesItsURLBehind(t *testing.T) {
	script := settingsRoutes(t, daytonaSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "k\n", "teams", "settings", "set", teamID,
		"--sandbox", "e2b", "--sandbox-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	s := sandbox(t, put(t, script))
	if s["url"] != nil {
		t.Errorf("sandbox = %v, want no url", s)
	}
}

func TestSettingsShowNamesTheSandbox(t *testing.T) {
	settingsRoutes(t, daytonaSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	for _, want := range []string{"daytona", "https://daytona.example", "sandbox key"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestAnEmptyStdinIsNotASandboxKey(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	_, err := clitest.ExecuteWithStdin(t, commands, "\n",
		"teams", "settings", "set", teamID, "--sandbox", "e2b", "--sandbox-key-stdin")
	if err == nil || !strings.Contains(err.Error(), "nothing arrived") {
		t.Fatalf("err = %v, want nothing arrived", err)
	}
	if strings.Contains(err.Error(), "--no-search-key") {
		t.Errorf("suggests the search flag for a sandbox key: %v", err)
	}
	if script.Calls(http.MethodPut, settingsPath) != 0 {
		t.Error("sent a PUT anyway")
	}
}

// Without the check, the search key would take the whole pipe and the sandbox
// key would be refused as empty: refused either way, but for the wrong reason.
func TestTwoKeysOnOneStdinSayWhy(t *testing.T) {
	settingsRoutes(t, neverSaved, http.StatusOK)

	_, err := clitest.ExecuteWithStdin(t, commands, "k\n", "teams", "settings", "set", teamID,
		"--search", "brave", "--search-key-stdin", "--sandbox", "e2b", "--sandbox-key-stdin")
	if err == nil || !strings.Contains(err.Error(), "separate commands") {
		t.Fatalf("err = %v, want it to say to use separate commands", err)
	}
}

func TestSettingsSetSendsAnE2BTemplate(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "k\n", "teams", "settings", "set", teamID,
		"--sandbox", "e2b", "--sandbox-key-stdin", "--e2b-template", " acme/openarity-sandbox "); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}
	if got := sandbox(t, put(t, script))["template"]; got != "acme/openarity-sandbox" {
		t.Errorf("template = %v, want acme/openarity-sandbox", got)
	}
}

const e2bSaved = `{"model":null,
  "search":{"provider":"none","url":null,"key_set":false},
  "sandbox":{"provider":"e2b","url":null,"template":"acme/openarity-sandbox","key_set":true},
  "learning":false,"compaction_messages":100,"updated_at":"2026-11-18T09:00:00Z"}`

// A template is E2B's: switching away leaves it behind, and clearing it
// returns to the default.
func TestAnE2BTemplateGoesWithItsProviderOrWhenCleared(t *testing.T) {
	for name, args := range map[string][]string{
		"switched": {"--sandbox", "daytona", "--sandbox-key-stdin"},
		"cleared":  {"--no-e2b-template"},
	} {
		t.Run(name, func(t *testing.T) {
			script := settingsRoutes(t, e2bSaved, http.StatusOK)
			if _, err := clitest.ExecuteWithStdin(t, commands, "k\n",
				append([]string{"teams", "settings", "set", teamID}, args...)...); err != nil {
				t.Fatal(err)
			}
			if got := sandbox(t, put(t, script))["template"]; got != nil {
				t.Errorf("template = %v, want none", got)
			}
		})
	}
}

func TestSettingsShowNamesTheE2BTemplateOrItsDefault(t *testing.T) {
	for stored, want := range map[string]string{
		e2bSaved: "acme/openarity-sandbox",
		strings.Replace(e2bSaved, `"template":"acme/openarity-sandbox",`, ``, 1): "openarity-sandbox (default)",
	} {
		settingsRoutes(t, stored, http.StatusOK)
		out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
