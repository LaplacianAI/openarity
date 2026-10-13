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

// The commonest way a credential reaches this list is a clone URL pasted
// whole: https://x-access-token:<token>@github.com. An entry that is merely
// malformed is echoed, because seeing it is how it gets fixed; one with an @
// in it is not.
func TestAHostEntryCarryingACredentialIsNotRepeated(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://x-access-token:ghp_hostsecret@github.com",
		"ghp_hostsecret@hub.example.com",
		"good.example.com,deploy:ghp_hostsecret@files.example.org",
	} {
		_, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_HOSTS": raw})
		if err == nil || !strings.Contains(err.Error(), "SKILL_IMPORT_HOSTS entry") {
			t.Errorf("%q accepted, or refused without naming the entry: %v", raw, err)
			continue
		}
		if strings.Contains(err.Error(), "hostsecret") {
			t.Errorf("the refusal repeats the credential: %v", err)
		}
	}
}

// The token is the token. One carrying a space or a line break is a paste
// gone wrong — a trailing newline from a file, a quoted blank — and would be
// refused by the first request that tried to send it as a header, long after
// the boot that could have said so.
func TestTheGitHubTokenIsCheckedAsAValue(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"ghp_0123456789abcdefghijklmnopqrstuvwxyz", "github_pat_11ABC_def-ghi"} {
		cfg, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN": raw})
		if err != nil {
			t.Errorf("SKILL_IMPORT_GITHUB_TOKEN: %v", err)
			continue
		}
		if cfg.SkillImportGitHubToken != raw {
			t.Errorf("loaded %q, want the token as given", cfg.SkillImportGitHubToken)
		}
	}

	const token = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	for _, raw := range []string{token + "\n", " " + token, token + " ", "ghp_0123 456789", token + "\r", token + "\x00", "\t"} {
		_, err := load(map[string]string{"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN": raw})
		if err == nil || !strings.Contains(err.Error(), "SKILL_IMPORT_GITHUB_TOKEN") {
			t.Errorf("SKILL_IMPORT_GITHUB_TOKEN=%q: err = %v, want a refusal naming the variable", raw, err)
			continue
		}
		// An error reaches stderr and the log shipper.
		if strings.Contains(err.Error(), "ghp_0123") {
			t.Errorf("the refusal repeats the token: %v", err)
		}
	}
}

// Neither check hides behind another setting: both run with every other
// field at its default, in production as in development.
func TestSkillImportSettingsAreCheckedInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"development", "production"} {
		for name, value := range map[string]string{
			"OPENARITY_SKILL_IMPORT_HOSTS":        "https://hub.example.com",
			"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN": "ghp_abc\n",
		} {
			_, err := load(map[string]string{"OPENARITY_ENVIRONMENT": env, name: value})
			if err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(name, "OPENARITY_")) {
				t.Errorf("%s: %s=%q: err = %v", env, name, value, err)
			}
		}
	}
}

// The hosts are an operator's list and are printed. The token is a bearer
// credential with nothing about it worth keeping in a log line, so String()
// does not list it at all. If it ever does, this says so.
func TestStringShowsTheImportHostsAndNeverTheToken(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_SKILL_IMPORT_HOSTS":        "hub.example.com,files.example.org",
		"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN": "ghp_importtokenvalue",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.String()
	if !strings.Contains(s, "SkillImportHosts:[hub.example.com files.example.org]") {
		t.Errorf("String() = %s, want the hosts", s)
	}
	if strings.Contains(s, "ghp_importtokenvalue") {
		t.Errorf("String() printed the GitHub token: %s", s)
	}
}
