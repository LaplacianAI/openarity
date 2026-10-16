package config

import (
	"strings"
	"testing"
)

const embeddingURL = "https://gateway.example.com/v1"

// A model set the way it is meant to be: with where to ask for it. Cases
// that need one without the other build the map themselves.
func embedding(model, dims, key string) map[string]string {
	env := map[string]string{}
	if model != "" {
		env["OPENARITY_EMBEDDING_MODEL"] = model
		env["OPENARITY_EMBEDDING_BASE_URL"] = embeddingURL
	}
	if dims != "" {
		env["OPENARITY_EMBEDDING_DIMENSIONS"] = dims
	}
	if key != "" {
		env["OPENARITY_EMBEDDING_API_KEY"] = key
	}
	return env
}

func with(env map[string]string, key, value string) map[string]string {
	env[key] = value
	return env
}

// No model is a configuration, not a mistake: the worker runs no projector
// and says so. Every install that has not chosen a model still has to boot.
func TestNoEmbeddingModelIsAValidConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 || cfg.EmbeddingBaseURL != "" || cfg.EmbeddingAPIKey != "" {
		t.Errorf("defaults = %q %d %q %q, want none",
			cfg.EmbeddingModel, cfg.EmbeddingDimensions, cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey)
	}
}

func TestAnEmbeddingModelWithItsDimensionsIsAccepted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ model, dims, key string }{
		{"text-embedding-3-small", "1536", ""},
		{"ollama/nomic-embed-text", "768", ""},
		{"m", "1", ""},
		{"m", "4096", ""},
		{"openai/text-embedding-3-large", "3072", "sk-live-0123456789"},
	} {
		cfg, err := load(embedding(tc.model, tc.dims, tc.key))
		if err != nil {
			t.Errorf("%+v: %v", tc, err)
			continue
		}
		if cfg.EmbeddingModel != tc.model || cfg.EmbeddingAPIKey != tc.key || cfg.EmbeddingBaseURL != embeddingURL {
			t.Errorf("%+v: loaded %q %d %q", tc, cfg.EmbeddingModel, cfg.EmbeddingDimensions, cfg.EmbeddingBaseURL)
		}
	}
}

// A model on the machine and one at a provider are the same setting.
func TestTheEmbeddingBaseURLIsHTTPOrHTTPS(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"http://ollama:11434/v1", "https://api.openai.com/v1", "http://127.0.0.1:4000/v1/"} {
		if _, err := load(with(embedding("m", "768", ""), "OPENARITY_EMBEDDING_BASE_URL", raw)); err != nil {
			t.Errorf("EMBEDDING_BASE_URL=%q: %v", raw, err)
		}
	}
}

// Each refusal says what to change. The index is built for one model's
// dimensions, so half the pair is the commonest way to get this wrong.
func TestEmbeddingSettingsThatCannotWorkAreRefused(t *testing.T) {
	t.Parallel()

	model := func() map[string]string {
		return map[string]string{"OPENARITY_EMBEDDING_MODEL": "m", "OPENARITY_EMBEDDING_DIMENSIONS": "768"}
	}

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"a model without dimensions":   {embedding("m", "", ""), "set together or not at all"},
		"dimensions without a model":   {embedding("", "768", ""), "set together or not at all"},
		"zero dimensions with a model": {embedding("m", "0", ""), "set together or not at all"},
		"negative dimensions":          {embedding("m", "-1", ""), "EMBEDDING_DIMENSIONS is -1, and must be between 1 and 4096"},
		"too many dimensions":          {embedding("m", "4097", ""), "EMBEDDING_DIMENSIONS is 4097, and must be between 1 and 4096"},
		"a model with nowhere to ask":  {model(), "EMBEDDING_MODEL is set but EMBEDDING_BASE_URL is not"},
		"a base URL with no model": {
			map[string]string{"OPENARITY_EMBEDDING_BASE_URL": embeddingURL},
			"EMBEDDING_BASE_URL is set but EMBEDDING_MODEL is not",
		},
		"a key with no model": {embedding("", "", "sk-live-0123456789"), "EMBEDDING_API_KEY is set but EMBEDDING_MODEL is not"},
		"a base URL that is not http": {
			with(model(), "OPENARITY_EMBEDDING_BASE_URL", "redis://gateway:6379"),
			"EMBEDDING_BASE_URL must have one of the following schemes",
		},
		"a base URL with no scheme": {
			with(model(), "OPENARITY_EMBEDDING_BASE_URL", "gateway.example.com/v1"),
			"EMBEDDING_BASE_URL must have a scheme",
		},
		"a base URL with no host": {
			with(model(), "OPENARITY_EMBEDDING_BASE_URL", "http:///v1"),
			"EMBEDDING_BASE_URL must have a host",
		},
		"a key ending in a newline": {embedding("m", "768", "sk-live-0123456789\n"), "EMBEDDING_API_KEY holds a space"},
		"a key with a space in it":  {embedding("m", "768", "sk-live 0123456789"), "EMBEDDING_API_KEY holds a space"},
	} {
		_, err := load(tc.env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
	}
}

// A padded model name is named for what it is. The pairing check comes
// after, so it is not misreported as "set without dimensions".
func TestAPaddedEmbeddingModelIsNamedAsPadded(t *testing.T) {
	t.Parallel()

	for _, env := range []map[string]string{
		{"OPENARITY_EMBEDDING_MODEL": "m ", "OPENARITY_EMBEDDING_DIMENSIONS": "768"},
		{"OPENARITY_EMBEDDING_MODEL": "\tm", "OPENARITY_EMBEDDING_DIMENSIONS": "768"},
		{"OPENARITY_EMBEDDING_MODEL": " "},
	} {
		_, err := load(env)
		if err == nil || !strings.Contains(err.Error(), "has a space at one end") {
			t.Errorf("%q: err = %v, want the padding named", env["OPENARITY_EMBEDDING_MODEL"], err)
		}
	}
}

// A key refused for its shape is not repeated: an error reaches stderr and
// the log shipper, and everything but the stray newline is the real key.
func TestARefusedKeyIsNotEchoed(t *testing.T) {
	t.Parallel()

	for name, env := range map[string]map[string]string{
		"EMBEDDING_API_KEY": embedding("m", "768", "sk-live-0123456789\n"),
		"LLM_API_KEY":       {"OPENARITY_LLM_API_KEY": "sk-live-0123456789\n"},
	} {
		_, err := load(env)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want a refusal naming it", name, err)
			continue
		}
		if strings.Contains(err.Error(), "sk-live") {
			t.Errorf("%s: the refusal repeats the key: %v", name, err)
		}
	}
}

func TestEmbeddingSettingsAreCheckedInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"development", "production"} {
		e := embedding("m", "", "")
		e["OPENARITY_ENVIRONMENT"] = env
		if _, err := load(e); err == nil || !strings.Contains(err.Error(), "EMBEDDING_MODEL") {
			t.Errorf("%s: err = %v", env, err)
		}
	}
}

// Nothing reads LLM_API_KEY until the runtime exists, and it is checked all
// the same: a key that cannot be sent is wrong on the day it is written.
func TestTheLLMKeyLoadsAsGiven(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_LLM_BASE_URL": "https://api.example.com/v1",
		"OPENARITY_LLM_API_KEY":  "sk-live-0123456789",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLMBaseURL != "https://api.example.com/v1" || cfg.LLMAPIKey != "sk-live-0123456789" {
		t.Errorf("loaded %q and a key of %d bytes", cfg.LLMBaseURL, len(cfg.LLMAPIKey))
	}
}

// Where and what are printed, the address without any password in it. And
// the line stays one well-formed Config{...}, so a log parser reading
// field:value pairs is not thrown.
func TestStringShowsTheEmbeddingSettings(t *testing.T) {
	t.Parallel()

	cfg, err := load(with(embedding("text-embedding-3-small", "1536", ""),
		"OPENARITY_EMBEDDING_BASE_URL", "https://user:urlsecret@gateway.example.com/v1"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.String()
	for _, want := range []string{
		" EmbeddingBaseURL:https://user:xxxxx@gateway.example.com/v1 ",
		" EmbeddingModel:text-embedding-3-small ",
		"EmbeddingDimensions:1536}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %s, want it to contain %q", s, want)
		}
	}
	if strings.Count(s, "{") != 1 || strings.Count(s, "}") != 1 || !strings.HasSuffix(s, "}") {
		t.Errorf("String() = %s, want exactly one Config{...}", s)
	}
}

// A key is a bearer credential: no host or user in it worth keeping, so
// String() lists neither rather than redacting them. Config is logged at
// startup, and adding a field to that format string is a one-word change
// nobody would question in review.
func TestStringNeverPrintsAnAPIKey(t *testing.T) {
	t.Parallel()

	env := embedding("m", "768", "embeddingkeyvalue")
	env["OPENARITY_LLM_API_KEY"] = "llmkeyvalue"
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.String()
	for _, secret := range []string{"embeddingkeyvalue", "llmkeyvalue"} {
		if strings.Contains(s, secret) {
			t.Errorf("String() printed %q: %s", secret, s)
		}
	}
}

// deployment/docker-compose.yml passes every one of these whether or not it
// is set, as an empty string (and dimensions as 0). Those have to mean "not
// configured", or a stack with nothing chosen would not start.
func TestWhatComposePassesWhenNothingIsSetIsAccepted(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_LLM_API_KEY":               "",
		"OPENARITY_SKILL_IMPORT_HOSTS":        "",
		"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN": "",
		"OPENARITY_MCP_PRIVATE_NETWORKS":      "",
		"OPENARITY_EMBEDDING_BASE_URL":        "",
		"OPENARITY_EMBEDDING_API_KEY":         "",
		"OPENARITY_EMBEDDING_MODEL":           "",
		"OPENARITY_EMBEDDING_DIMENSIONS":      "0",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.SkillImportHosts) != 0 || len(cfg.MCPPrivateNetworks) != 0 || cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 {
		t.Errorf("loaded %v %v %q %d, want nothing configured", cfg.SkillImportHosts, cfg.MCPPrivateNetworks, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	}
}
