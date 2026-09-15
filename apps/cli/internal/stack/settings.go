package stack

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Settings struct {
	ObjectsBackend  string `yaml:"objects_backend"`
	ObjectsEndpoint string `yaml:"objects_endpoint,omitempty"`
	ObjectsBucket   string `yaml:"objects_bucket,omitempty"`
	ObjectsRegion   string `yaml:"objects_region,omitempty"`

	SecretsBackend string `yaml:"secrets_backend"`
	SecretsAddr    string `yaml:"secrets_addr,omitempty"`
	SecretsKVMount string `yaml:"secrets_kv_mount,omitempty"`

	// Where the model gateway comes from. "external" points at one somebody
	// else runs, which is every install today; "litellm" and "omniroute"
	// install and supervise one here.
	ModelBackend string `yaml:"model_backend,omitempty"`
	ModelBaseURL string `yaml:"model_base_url,omitempty"`

	// Where a gateway of our own keeps its runtime and its packages. Separate
	// from the install root's bin/ because it is a gigabyte of somebody
	// else's software, and a person moving it to another disk should be able
	// to.
	ModelPath string `yaml:"model_path,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		ObjectsBackend: "filesystem",
		SecretsBackend: "static",

		// Pointing at one rather than running one, because running one is a
		// gigabyte and nobody should download that without being asked.
		ModelBackend: "external",
		ModelBaseURL: "http://127.0.0.1:20128/v1",
	}
}

func (s Settings) Environment() string {
	if s.SecretsBackend != "static" && s.ObjectsBackend != "memory" {
		return "production"
	}
	return "development"
}

// RunsGateway reports whether this install has a model gateway to provision
// and supervise. Neither of them ships a binary, so "running one" means
// downloading a runtime and installing into it.
func (s Settings) RunsGateway() bool {
	return s.ModelBackend == "litellm" || s.ModelBackend == "omniroute"
}

// GatewayRuntime is what has to be fetched before the gateway can be
// installed: uv provisions a Python for LiteLLM, Node runs OmniRoute.
func (s Settings) GatewayRuntime() string {
	switch s.ModelBackend {
	case "litellm":
		return "uv"
	case "omniroute":
		return "node"
	default:
		return ""
	}
}

func (s Settings) Env() []string {
	out := []string{
		"OPENARITY_OBJECTS_BACKEND=" + s.ObjectsBackend,
		"OPENARITY_SECRETS_BACKEND=" + s.SecretsBackend,
	}

	for key, value := range map[string]string{
		"OPENARITY_OBJECTS_ENDPOINT": s.ObjectsEndpoint,
		"OPENARITY_OBJECTS_BUCKET":   s.ObjectsBucket,
		"OPENARITY_OBJECTS_REGION":   s.ObjectsRegion,
		"OPENARITY_SECRETS_ADDR":     s.SecretsAddr,
		"OPENARITY_SECRETS_KV_MOUNT": s.SecretsKVMount,
		"OPENARITY_MODEL_BASE_URL":   s.ModelBaseURL,
	} {
		if value != "" {
			out = append(out, key+"="+value)
		}
	}

	sort.Strings(out)
	return out
}

func WriteCredentials(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + values[key] + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func ReadCredentials(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path from Layout
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			continue
		}
		out[key] = value
	}
	return out, nil
}

func (s Settings) Validate() error {
	switch s.ObjectsBackend {
	case "memory", "filesystem", "s3":
	case "minio":
		// Named rather than folded into "unknown", because an install made
		// before this was removed says minio in its own stack.yaml and the
		// person reading that has done nothing wrong. MinIO's publisher
		// archived the open-source server and took every binary down —
		// dl.min.io answers 410 for every version and every platform — so
		// there is nothing left to run.
		return errors.New("stack: MinIO is no longer published — its own project was archived and every binary withdrawn; point objects_backend at s3 with an endpoint instead")
	default:
		return fmt.Errorf("stack: unknown objects backend %q", s.ObjectsBackend)
	}

	switch s.SecretsBackend {
	case "static", "openbao", "vault":
	default:
		return fmt.Errorf("stack: unknown secrets backend %q", s.SecretsBackend)
	}

	if s.ObjectsBackend == "s3" && s.ObjectsBucket == "" {
		return errors.New("stack: an S3 object store needs a bucket")
	}
	if s.SecretsBackend != "static" && s.SecretsAddr == "" {
		return errors.New("stack: an external secret store needs an address")
	}

	switch s.ModelBackend {
	case "", "external", "litellm", "omniroute":
	default:
		return fmt.Errorf("stack: unknown model backend %q", s.ModelBackend)
	}

	if s.RunsGateway() && s.ModelPath == "" {
		return errors.New("stack: a gateway of our own needs a directory to install into")
	}
	return nil
}
