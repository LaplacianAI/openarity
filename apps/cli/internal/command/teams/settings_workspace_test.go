package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func TestSettingsSetSendsTheWorkspacesSizeAndStopTimes(t *testing.T) {
	script := settingsRoutes(t, daytonaSaved, http.StatusOK)
	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--workspace-memory-mb", "8192", "--workspace-cpus", "4", "--workspace-disk-mb", "20480",
		"--keep-stopped-hours", "72", "--stop-idle-minutes", "10"); err != nil {
		t.Fatal(err)
	}
	s := sandbox(t, put(t, script))
	z, _ := s["workspace_size"].(map[string]any)
	if z["memory_mb"] != float64(8192) || z["cpus"] != float64(4) || z["disk_mb"] != float64(20480) ||
		s["keep_hours"] != float64(72) || s["idle_minutes"] != float64(10) || s["provider"] != "daytona" {
		t.Fatalf("sandbox = %v", s)
	}
	if _, ok := s["api_key"]; ok {
		t.Fatal("a key was sent that nobody piped in")
	}
}

// --no-... sends null, which the brain reads as the provider's or the
// deployment's; leaving the flag out sends nothing, which keeps the stored.
func TestSettingsSetResetsWithNullAndKeepsWhatItLeavesOut(t *testing.T) {
	script := settingsRoutes(t, daytonaSaved, http.StatusOK)
	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--no-workspace-size", "--no-stop-idle-minutes"); err != nil {
		t.Fatal(err)
	}
	s := sandbox(t, put(t, script))
	for _, k := range []string{"workspace_size", "idle_minutes"} {
		if v, ok := s[k]; !ok || v != nil {
			t.Errorf("%s = %v (sent %v), want null", k, v, ok)
		}
	}
	if _, ok := s["keep_hours"]; ok {
		t.Error("keep_hours was sent, though no flag named it")
	}
}

func TestSettingsSetRefusesHalfASizeOrAContradiction(t *testing.T) {
	for want, args := range map[string][]string{
		"together":   {"--workspace-memory-mb", "8192"},
		"cannot be":  {"--no-workspace-size", "--workspace-memory-mb", "1024", "--workspace-cpus", "1", "--workspace-disk-mb", "512"},
		"contradict": {"--stop-idle-minutes", "5", "--no-stop-idle-minutes"},
	} {
		settingsRoutes(t, daytonaSaved, http.StatusOK)
		_, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, args...)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

// Each value is held to its range here, before anything is sent, as the
// other flags are.
func TestSettingsSetRefusesAStopTimeOrSizeOutOfRange(t *testing.T) {
	size := func(m, c, d string) []string {
		return []string{"--workspace-memory-mb", m, "--workspace-cpus", c, "--workspace-disk-mb", d}
	}
	for want, args := range map[string][]string{
		"--keep-stopped-hours is":  {"--keep-stopped-hours", "721"},
		"--stop-idle-minutes is":   {"--stop-idle-minutes", "0"},
		"--workspace-memory-mb is": size("128", "1", "512"),
		"--workspace-cpus is":      size("1024", "17", "512"),
		"--workspace-disk-mb is":   size("1024", "1", "300000"),
	} {
		settingsRoutes(t, daytonaSaved, http.StatusOK)
		_, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, args...)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestSettingsShowSaysTheWorkspacesSizeAndStopTimes(t *testing.T) {
	stored := strings.Replace(daytonaSaved, `"key_set":true}`,
		`"key_set":true,"keep_hours":72,"idle_minutes":10,"workspace_size":{"memory_mb":8192,"cpus":4,"disk_mb":20480}}`, 1)
	settingsRoutes(t, stored, http.StatusOK)
	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"8192 MB · 4 CPU · 20480 MB disk", "after 10 min", "72 h"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	settingsRoutes(t, strings.Replace(daytonaSaved, `"key_set":true}`, `"key_set":true,"keep_hours":24}`, 1), http.StatusOK)
	out, _ = clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	for _, want := range []string{"the provider's", "the deployment's", "24 h"} {
		if !strings.Contains(out, want) {
			t.Errorf("defaults: missing %q:\n%s", want, out)
		}
	}
}
