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

	MinIOPath string `yaml:"minio_path,omitempty"`

	SecretsBackend string `yaml:"secrets_backend"`
	SecretsAddr    string `yaml:"secrets_addr,omitempty"`
	SecretsKVMount string `yaml:"secrets_kv_mount,omitempty"`

	ModelBaseURL string `yaml:"model_base_url,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		ObjectsBackend: "filesystem",
		SecretsBackend: "static",
		ModelBaseURL:   "http://127.0.0.1:20128/v1",
	}
}

func (s Settings) Environment() string {
	if s.SecretsBackend != "static" && s.ObjectsBackend != "memory" {
		return "production"
	}
	return "development"
}

// RunsMinIO reports whether this install supervises an object store of its
// own, rather than reaching one somebody else runs.
func (s Settings) RunsMinIO() bool { return s.ObjectsBackend == "minio" }

// The brain has three object backends and minio is not one of them: MinIO is
// S3, and what makes it MinIO is the endpoint. The distinction matters to the
// installer, which has to download and supervise one, and to nothing else.
func (s Settings) brainObjectsBackend() string {
	if s.RunsMinIO() {
		return "s3"
	}
	return s.ObjectsBackend
}

func (s Settings) Env() []string {
	out := []string{
		"OPENARITY_OBJECTS_BACKEND=" + s.brainObjectsBackend(),
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
	case "memory", "filesystem", "s3", "minio":
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
	if s.RunsMinIO() && s.MinIOPath == "" {
		return errors.New("stack: a MinIO of our own needs a directory to keep its data in")
	}
	if s.SecretsBackend != "static" && s.SecretsAddr == "" {
		return errors.New("stack: an external secret store needs an address")
	}
	return nil
}
