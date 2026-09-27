package config

import (
	"strings"
	"testing"
)

// The allowlist compares a request's bare host name, so every entry has to
// be one. Anything else would boot cleanly and refuse every zip from that
// host, with nothing anywhere saying why.
func TestSkillImportHostsAreBareHostNames(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"hub.example.com",
		"HUB.Example.com",
		"hub.example.com,files.example.org",
		"localhost",
		"203.0.113.7",
		"xn--bcher-kva.example",
		"a-b.example",
	} {
		if _, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_HOSTS": raw}); err != nil {
			t.Errorf("SKILL_IMPORT_HOSTS=%q: %v", raw, err)
		}
	}
}

func TestSkillImportHostsRefuseWhatCouldNeverMatch(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://hub.example.com",
		"hub.example.com:443",
		"hub.example.com/skills",
		"*.example.com",
		" hub.example.com",
		"hub.example.com ",
		"a,,b",
		"hub.example.com,",
		"user@hub.example.com",
		"-hub.example.com",
		"hub-.example.com",
		"hub..example.com",
		"hub.example.com.",
		".example.com",
		"[::1]",
		"hub_example.com",
	} {
		_, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_HOSTS": raw})
		if err == nil || !strings.Contains(err.Error(), "SKILL_IMPORT_HOSTS") {
			t.Errorf("SKILL_IMPORT_HOSTS=%q: err = %v, want a refusal naming the variable", raw, err)
		}
	}
}

// Which entry, and what it was, so a long list is fixed in one edit.
func TestSkillImportHostsErrorIdentifiesTheEntry(t *testing.T) {
	t.Parallel()

	_, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_HOSTS": "good.example.com,https://bad.example.com"})
	if err == nil || !strings.Contains(err.Error(), "entry 1") || !strings.Contains(err.Error(), `"https://bad.example.com"`) {
		t.Errorf("err = %v, want entry 1 and its value", err)
	}
}

// The token is a reference into the secret store, never the token itself: a
// value that is not path#key is refused, so a pasted token fails the boot
// rather than being read as a path that holds nothing.
func TestTheGitHubTokenIsASecretReference(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"platform/github#token", "github#token", "a/b/c#key-1"} {
		if _, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN_REF": raw}); err != nil {
			t.Errorf("SKILL_IMPORT_GITHUB_TOKEN_REF=%q: %v", raw, err)
		}
	}

	for _, raw := range []string{
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"platform/github",
		"#token",
		"platform/github#",
		"platform/github#token#again",
	} {
		_, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN_REF": raw})
		if err == nil || !strings.Contains(err.Error(), "SKILL_IMPORT_GITHUB_TOKEN_REF") || !strings.Contains(err.Error(), "path#key") {
			t.Errorf("SKILL_IMPORT_GITHUB_TOKEN_REF=%q: err = %v, want a refusal naming the variable and the form", raw, err)
		}
	}
}

// Neither check hides behind another setting: both run with every other
// field at its default, in production as in development.
func TestSkillImportSettingsAreCheckedInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"development", "production"} {
		for name, value := range map[string]string{
			"OPENARITY_SKILL_IMPORT_HOSTS":            "https://hub.example.com",
			"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN_REF": "platform/github",
		} {
			_, err := load(map[string]string{"OPENARITY_ENVIRONMENT": env, name: value})
			if err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(name, "OPENARITY_")) {
				t.Errorf("%s: %s=%q: err = %v", env, name, value, err)
			}
		}
	}
}

// Both are printed: the hosts are an operator's list, and the reference is a
// path, which is the point of keeping a reference rather than the token.
func TestStringShowsTheSkillImportSettings(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_SKILL_IMPORT_HOSTS":            "hub.example.com,files.example.org",
		"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN_REF": "platform/github#token",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.String()
	for _, want := range []string{"SkillImportHosts:[hub.example.com files.example.org]", "SkillImportGitHubTokenRef:platform/github#token"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %s, want it to contain %q", s, want)
		}
	}
}
