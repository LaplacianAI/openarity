package config

import (
	"strings"
	"testing"
)

func embedding(model, dims, ref string) map[string]string {
	env := map[string]string{}
	if model != "" {
		env["OPENARITY_EMBEDDING_MODEL"] = model
	}
	if dims != "" {
		env["OPENARITY_EMBEDDING_DIMENSIONS"] = dims
	}
	if ref != "" {
		env["OPENARITY_EMBEDDING_KEY_REF"] = ref
	}
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
	if cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 || cfg.EmbeddingKeyRef != "" {
		t.Errorf("defaults = %q %d %q, want none", cfg.EmbeddingModel, cfg.EmbeddingDimensions, cfg.EmbeddingKeyRef)
	}
}

func TestAnEmbeddingModelWithItsDimensionsIsAccepted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ model, dims, ref string }{
		{"text-embedding-3-small", "1536", ""},
		{"ollama/nomic-embed-text", "768", ""},
		{"m", "1", ""},
		{"m", "4096", ""},
		{"openai/text-embedding-3-large", "3072", "platform/embeddings#key"},
	} {
		cfg, err := load(embedding(tc.model, tc.dims, tc.ref))
		if err != nil {
			t.Errorf("%+v: %v", tc, err)
			continue
		}
		if cfg.EmbeddingModel != tc.model || cfg.EmbeddingKeyRef != tc.ref {
			t.Errorf("%+v: loaded %q %d %q", tc, cfg.EmbeddingModel, cfg.EmbeddingDimensions, cfg.EmbeddingKeyRef)
		}
	}
}

// Each refusal says what to change. The index is built for one model's
// dimensions, so half the pair is the commonest way to get this wrong.
func TestEmbeddingSettingsThatCannotWorkAreRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"a model without dimensions":    {embedding("m", "", ""), "set together or not at all"},
		"dimensions without a model":    {embedding("", "768", ""), "set together or not at all"},
		"zero dimensions with a model":  {embedding("m", "0", ""), "set together or not at all"},
		"negative dimensions":           {embedding("m", "-1", ""), "EMBEDDING_DIMENSIONS is -1, and must be between 1 and 4096"},
		"too many dimensions":           {embedding("m", "4097", ""), "EMBEDDING_DIMENSIONS is 4097, and must be between 1 and 4096"},
		"a key with no model":           {embedding("", "", "platform/embeddings#key"), "EMBEDDING_KEY_REF is set but EMBEDDING_MODEL is not"},
		"a key that is not a reference": {embedding("m", "768", "sk-live-0123456789"), "EMBEDDING_KEY_REF"},
		"a reference with no key":       {embedding("m", "768", "platform/embeddings"), "path#key"},
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

// A pasted key is refused without being repeated: an error reaches stderr
// and the log shipper.
func TestAPastedEmbeddingKeyIsNotEchoed(t *testing.T) {
	t.Parallel()

	_, err := load(embedding("m", "768", "sk-live-0123456789"))
	if err == nil || strings.Contains(err.Error(), "sk-live-0123456789") {
		t.Errorf("err = %v, want a refusal that does not repeat the value", err)
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

// All three are printed: the reference is a path, which is the point of
// keeping a reference rather than the key. And the line stays one well-formed
// Config{...}, so a log parser reading field:value pairs is not thrown.
func TestStringShowsTheEmbeddingSettings(t *testing.T) {
	t.Parallel()

	cfg, err := load(embedding("text-embedding-3-small", "1536", "platform/embeddings#key"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.String()
	for _, want := range []string{" EmbeddingModel:text-embedding-3-small ", "EmbeddingDimensions:1536 ", "EmbeddingKeyRef:platform/embeddings#key}"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %s, want it to contain %q", s, want)
		}
	}
	if strings.Count(s, "{") != 1 || strings.Count(s, "}") != 1 || !strings.HasSuffix(s, "}") {
		t.Errorf("String() = %s, want exactly one Config{...}", s)
	}
}

// deployment/docker-compose.yml passes every one of these whether or not it
// is set, as an empty string (and dimensions as 0). Those have to mean "not
// configured", or a stack with nothing chosen would not start.
func TestWhatComposePassesWhenNothingIsSetIsAccepted(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_SKILL_IMPORT_HOSTS":            "",
		"OPENARITY_SKILL_IMPORT_GITHUB_TOKEN_REF": "",
		"OPENARITY_MCP_PRIVATE_NETWORKS":          "",
		"OPENARITY_EMBEDDING_MODEL":               "",
		"OPENARITY_EMBEDDING_DIMENSIONS":          "0",
		"OPENARITY_EMBEDDING_KEY_REF":             "",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.SkillImportHosts) != 0 || len(cfg.MCPPrivateNetworks) != 0 || cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 {
		t.Errorf("loaded %v %v %q %d, want nothing configured", cfg.SkillImportHosts, cfg.MCPPrivateNetworks, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	}
}
