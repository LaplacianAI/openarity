package approvals

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const currentSettings = `{"mode":"destructive","ttl_minutes":30}`

func settingsRoutes(save clitest.Reply) map[string]clitest.Reply {
	return routes(map[string]clitest.Reply{
		"GET /teams/{id}/approval-settings": {Status: http.StatusOK, Body: currentSettings},
		"PUT /teams/{id}/approval-settings": save,
	})
}

func TestSettingsShowsTheModeAndTheTTL(t *testing.T) {
	script := clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: currentSettings}))
	out, err := clitest.Execute(t, commands, "approvals", "settings", "platform")
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	for _, want := range []string{"destructive", "30 minutes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if n := script.Calls(http.MethodPut, "/teams/"+teamID+"/approval-settings"); n != 0 {
		t.Errorf("showing the settings saved them %d times", n)
	}
}

// A request carries both fields, so changing one without reading the other
// would reset it to a default nobody chose.
func TestOneFlagChangesOneSettingAndKeepsTheOther(t *testing.T) {
	script := clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: `{"mode":"writes","ttl_minutes":30}`}))
	if _, err := clitest.Execute(t, commands, "approvals", "settings", "platform", "--mode", "writes"); err != nil {
		t.Fatalf("settings --mode: %v", err)
	}
	if body := bodyOf(t, script, http.MethodPut); body["mode"] != "writes" || body["ttl_minutes"] != float64(30) {
		t.Errorf("body = %v", body)
	}

	script = clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: `{"mode":"destructive","ttl_minutes":60}`}))
	out, err := clitest.Execute(t, commands, "approvals", "settings", "platform", "--ttl", "60")
	if err != nil {
		t.Fatalf("settings --ttl: %v", err)
	}
	if body := bodyOf(t, script, http.MethodPut); body["mode"] != "destructive" || body["ttl_minutes"] != float64(60) {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "saved") {
		t.Errorf("no confirmation:\n%s", out)
	}
}

func TestSettingsRefusesWhatTheBrainWouldAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: currentSettings}))
	for _, args := range [][]string{
		{"--mode", "sometimes"},
		{"--ttl", "4"},
		{"--ttl", "1441"},
		{"--ttl", "0"},
		{"--mode", "all", "--ttl", "9999"},
	} {
		if _, err := clitest.Execute(t, commands, append([]string{"approvals", "settings", "platform"}, args...)...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("refused flags made %d requests", n)
	}
}

func TestTheEdgesOfTheTTLRangeAreAccepted(t *testing.T) {
	clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: currentSettings}))
	for _, ttl := range []string{"5", "1440"} {
		if _, err := clitest.Execute(t, commands, "approvals", "settings", "platform", "--ttl", ttl); err != nil {
			t.Errorf("--ttl %s: %v", ttl, err)
		}
	}
}

func TestSavingSettingsWithoutThePermissionNamesIt(t *testing.T) {
	clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusForbidden, Body: "forbidden"}))
	_, err := clitest.Execute(t, commands, "approvals", "settings", "platform", "--mode", "off")
	if err == nil || !strings.Contains(err.Error(), "team:settings") {
		t.Errorf("err = %v", err)
	}
}

func TestSettingsEveryFormat(t *testing.T) {
	clitest.Routes(t, settingsRoutes(clitest.Reply{Status: http.StatusOK, Body: currentSettings}))
	for _, format := range []string{"table", "json", "yaml"} {
		out, err := clitest.Execute(t, commands, "approvals", "settings", "platform", "-o", format)
		if err != nil {
			t.Fatalf("-o %s: %v", format, err)
		}
		if format != "table" && !strings.Contains(out, "ttl_minutes") {
			t.Errorf("-o %s lost the field name:\n%s", format, out)
		}
	}
}
