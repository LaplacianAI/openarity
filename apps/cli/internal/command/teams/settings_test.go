package teams

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const settingsPath = "/teams/" + teamID + "/settings"

const neverSaved = `{"model":null,"search":{"provider":"none","url":null,"key_set":false},
  "learning":false,"compaction_messages":100,"updated_at":null}`

const braveSaved = `{"model":{"name":"gpt-5","max_tokens":4096,"temperature":0.2},
  "search":{"provider":"brave","url":null,"key_set":true},
  "learning":false,"compaction_messages":250,"updated_at":"2026-10-21T09:00:00Z"}`

const ossSaved = `{"model":null,
  "search":{"provider":"firecrawl-oss","url":"http://firecrawl:3002","key_set":true},
  "learning":false,"compaction_messages":100,"updated_at":"2026-10-21T09:00:00Z"}`

func settingsRoutes(t *testing.T, stored string, putStatus int) *clitest.Transcript {
	t.Helper()
	return clitest.Routes(t, map[string]clitest.Reply{
		"GET " + settingsPath: {Status: http.StatusOK, Body: stored},
		"PUT " + settingsPath: {Status: putStatus, Body: stored},
	})
}

// put returns the body the command sent, decoded.
func put(t *testing.T, script *clitest.Transcript) map[string]any {
	t.Helper()

	for _, e := range script.All() {
		if e.Method == http.MethodPut {
			var body map[string]any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("the PUT body is not json: %v\n%s", err, e.Body)
			}
			return body
		}
	}
	t.Fatalf("nothing was sent: %+v", script.All())
	return nil
}

func search(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	s, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search = %v, want an object", body["search"])
	}
	return s
}

// ---------------------------------------------------------------------------
// show

func TestSettingsShowSaysAKeyIsSet(t *testing.T) {
	settingsRoutes(t, braveSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	for _, want := range []string{"gpt-5", "4096", "brave", "set", "250"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not set") {
		t.Errorf("says the key is not set:\n%s", out)
	}
}

func TestSettingsShowOfATeamThatNeverSaved(t *testing.T) {
	settingsRoutes(t, neverSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	for _, want := range []string{"none", "not set", "off", "100"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// json carries what arrived, nothing more: there is no key in it because the
// brain never sends one.
func TestSettingsShowAsJSONIsTheBrainsDocument(t *testing.T) {
	settingsRoutes(t, braveSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID, "-o", "json")
	if err != nil {
		t.Fatalf("teams settings show -o json: %v", err)
	}
	var got struct {
		Search struct {
			Provider string `json:"provider"`
			KeySet   bool   `json:"key_set"`
		} `json:"search"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got.Search.Provider != "brave" || !got.Search.KeySet {
		t.Errorf("search = %+v", got.Search)
	}
}

// ---------------------------------------------------------------------------
// set: read, change what was asked, write the whole

// Review Focus 1, from the CLI's side: flipping learning must send everything
// else as it was, and no key, so the brain keeps the stored one.
func TestSettingsSetChangesOnlyTheFlagsGiven(t *testing.T) {
	script := settingsRoutes(t, braveSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--learning", "on"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	body := put(t, script)
	if body["learning"] != true {
		t.Errorf("learning = %v, want true", body["learning"])
	}
	if body["compaction_messages"] != float64(250) {
		t.Errorf("compaction_messages = %v, want the stored 250", body["compaction_messages"])
	}
	model, _ := body["model"].(map[string]any)
	if model["name"] != "gpt-5" || model["max_tokens"] != float64(4096) || model["temperature"] != 0.2 {
		t.Errorf("model = %v, want the stored gpt-5 / 4096 / 0.2", body["model"])
	}
	s := search(t, body)
	if s["provider"] != "brave" {
		t.Errorf("provider = %v, want the stored brave", s["provider"])
	}
	if _, sent := s["api_key"]; sent {
		t.Errorf("sent api_key %v: leaving it out is what keeps the stored key", s["api_key"])
	}
	if _, sent := s["url"]; sent {
		t.Errorf("sent url %v for brave", s["url"])
	}
}

func TestSettingsSetReadsTheKeyFromStdin(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "tvly-key\n",
		"teams", "settings", "set", teamID, "--search", "tavily", "--search-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	s := search(t, put(t, script))
	if s["provider"] != "tavily" || s["api_key"] != "tvly-key" {
		t.Errorf("search = %v, want tavily with the key from stdin, newline trimmed", s)
	}
}

// Arguments are readable through ps and land in shell history, so a key can
// only come in on stdin.
func TestThereIsNoFlagThatTakesAKey(t *testing.T) {
	settingsRoutes(t, neverSaved, http.StatusOK)

	_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--search", "brave", "--search-key", "k")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("err = %v, want an unknown flag", err)
	}
}

func TestAnEmptyStdinIsNotAKey(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	_, err := clitest.ExecuteWithStdin(t, commands, "\n",
		"teams", "settings", "set", teamID, "--search", "brave", "--search-key-stdin")
	if err == nil {
		t.Fatal("an empty stdin was sent as a key")
	}
	if script.Calls(http.MethodPut, settingsPath) != 0 {
		t.Error("sent a PUT anyway")
	}
}

// For a self-hosted Firecrawl, "no key" has to be said: leaving the key out
// keeps the stored one.
func TestNoSearchKeySendsAnEmptyKey(t *testing.T) {
	script := settingsRoutes(t, ossSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--no-search-key"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	s := search(t, put(t, script))
	if key, sent := s["api_key"]; !sent || key != "" {
		t.Errorf("api_key = %v (sent %v), want an empty string", key, sent)
	}
	if s["url"] != "http://firecrawl:3002" {
		t.Errorf("url = %v, want the stored one kept", s["url"])
	}
}

func TestMovingASelfHostedProviderSendsTheNewURL(t *testing.T) {
	script := settingsRoutes(t, ossSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "fc-new\n", "teams", "settings", "set", teamID,
		"--search-url", "https://firecrawl.example.com", "--search-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	s := search(t, put(t, script))
	if s["provider"] != "firecrawl-oss" || s["url"] != "https://firecrawl.example.com" || s["api_key"] != "fc-new" {
		t.Errorf("search = %v", s)
	}
}

// A URL belongs to the provider it was given for. Switching to Brave from
// SearXNG must not carry SearXNG's address along, which the brain refuses.
func TestSwitchingProviderLeavesTheOldURLBehind(t *testing.T) {
	script := settingsRoutes(t, ossSaved, http.StatusOK)

	if _, err := clitest.ExecuteWithStdin(t, commands, "k\n", "teams", "settings", "set", teamID,
		"--search", "brave", "--search-key-stdin"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	if url, sent := search(t, put(t, script))["url"]; sent {
		t.Errorf("sent url %v to brave", url)
	}
}

func TestSettingsSetModelFlags(t *testing.T) {
	script := settingsRoutes(t, neverSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--model", "claude-opus-5-5", "--max-tokens", "8192", "--temperature", "0.3"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	model, _ := put(t, script)["model"].(map[string]any)
	if model["name"] != "claude-opus-5-5" || model["max_tokens"] != float64(8192) || model["temperature"] != 0.3 {
		t.Errorf("model = %v", model)
	}
}

func TestChangingMaxTokensKeepsTheStoredModel(t *testing.T) {
	script := settingsRoutes(t, braveSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--max-tokens", "1024"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	model, _ := put(t, script)["model"].(map[string]any)
	if model["name"] != "gpt-5" || model["max_tokens"] != float64(1024) || model["temperature"] != 0.2 {
		t.Errorf("model = %v, want gpt-5 at 1024 with its temperature kept", model)
	}
}

func TestNoModelSendsNoModel(t *testing.T) {
	script := settingsRoutes(t, braveSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--no-model"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	if model, sent := put(t, script)["model"]; sent && model != nil {
		t.Errorf("model = %v, want null or absent", model)
	}
}

// ---------------------------------------------------------------------------
// set: refusals

func TestSettingsSetRefusesBeforeSending(t *testing.T) {
	for name, args := range map[string][]string{
		"no flags at all":             {},
		"a model and no model":        {"--model", "gpt-5", "--no-model"},
		"a key and no key":            {"--search-key-stdin", "--no-search-key"},
		"learning that is not on/off": {"--learning", "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			script := settingsRoutes(t, braveSaved, http.StatusOK)

			_, err := clitest.ExecuteWithStdin(t, commands, "k\n",
				append([]string{"teams", "settings", "set", teamID}, args...)...)
			if err == nil {
				t.Fatal("accepted")
			}
			if script.Calls(http.MethodPut, settingsPath) != 0 {
				t.Error("sent a PUT anyway")
			}
		})
	}
}

func TestSettingsSetNamesThePermissionItNeeded(t *testing.T) {
	settingsRoutes(t, braveSaved, http.StatusForbidden)

	_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--learning", "on")
	if err == nil || !strings.Contains(err.Error(), "team:settings") {
		t.Errorf("err = %v, want it to name team:settings", err)
	}
}

// The brain's sentence is the useful part of a refusal: it names the field.
func TestSettingsSetShowsTheBrainsReason(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET " + settingsPath: {Status: http.StatusOK, Body: braveSaved},
		"PUT " + settingsPath: {Status: http.StatusBadRequest, Body: "search.api_key is required: no tavily key is saved"},
	})

	_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--search", "tavily")
	if err == nil || !strings.Contains(err.Error(), "search.api_key") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}
