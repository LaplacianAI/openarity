package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultSettingsAreWhatAPersonalInstallWants(t *testing.T) {
	t.Parallel()

	s := DefaultSettings()

	// Artifacts on disk, not in memory: memory loses them on every restart,
	// which for a personal install means losing them nightly.
	if s.ObjectsBackend != "filesystem" {
		t.Errorf("ObjectsBackend = %q, want filesystem", s.ObjectsBackend)
	}
	// static, because OpenBao is a fifth process to download, run and unseal.
	if s.SecretsBackend != "static" {
		t.Errorf("SecretsBackend = %q, want static", s.SecretsBackend)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
}

// The brain refuses a static secret store or an in-memory object store
// outside development, so the installer must not claim to be production while
// choosing those. Equally, an install with a real Vault and a real bucket
// should not be labelled development.
func TestTheEnvironmentFollowsTheBackendsThatWereChosen(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		settings Settings
		want     string
	}{
		{"defaults", DefaultSettings(), "development"},
		{
			"a real secret store but artifacts in memory",
			Settings{SecretsBackend: "openbao", ObjectsBackend: "memory"},
			"development",
		},
		{
			"a static store with artifacts on disk",
			Settings{SecretsBackend: "static", ObjectsBackend: "filesystem"},
			"development",
		},
		{
			"both real",
			Settings{SecretsBackend: "openbao", ObjectsBackend: "s3"},
			"production",
		},
	} {
		if got := tc.settings.Environment(); got != tc.want {
			t.Errorf("%s: Environment() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSettingsBecomeTheBrainsEnvironment(t *testing.T) {
	t.Parallel()

	s := Settings{
		ObjectsBackend:  "s3",
		ObjectsEndpoint: "http://127.0.0.1:19000",
		ObjectsBucket:   "openarity",
		ObjectsRegion:   "us-east-1",
		SecretsBackend:  "openbao",
		SecretsAddr:     "http://127.0.0.1:8200",
		SecretsKVMount:  "secret",
		ModelBaseURL:    "http://127.0.0.1:20128/v1",
	}

	env := map[string]bool{}
	for _, entry := range s.Env() {
		env[entry] = true
	}

	for _, want := range []string{
		"OPENARITY_OBJECTS_BACKEND=s3",
		"OPENARITY_OBJECTS_ENDPOINT=http://127.0.0.1:19000",
		"OPENARITY_OBJECTS_BUCKET=openarity",
		"OPENARITY_SECRETS_BACKEND=openbao",
		"OPENARITY_SECRETS_ADDR=http://127.0.0.1:8200",
		"OPENARITY_MODEL_BASE_URL=http://127.0.0.1:20128/v1",
	} {
		if !env[want] {
			t.Errorf("Env() does not contain %q", want)
		}
	}
}

// An empty value would override the brain's own default with an empty string,
// which validates differently from the setting being absent.
func TestBlankSettingsAreLeftOutOfTheEnvironment(t *testing.T) {
	t.Parallel()

	for _, entry := range DefaultSettings().Env() {
		if strings.HasSuffix(entry, "=") {
			t.Errorf("Env() contains an empty value: %q", entry)
		}
	}
}

// The credential file is the reason stack.yaml can stay readable. Keys go in
// one 0600 file and nowhere else.
func TestCredentialsRoundTripThroughTheirOwnFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secrets.env")
	want := map[string]string{
		"OPENARITY_OBJECTS_ACCESS_KEY":     "AKIAEXAMPLE",
		"OPENARITY_OBJECTS_SECRET_KEY":     "a value with spaces and = signs",
		"OPENARITY_SECRETS_APPROLE_SECRET": "another",
	}

	if err := WriteCredentials(path, want); err != nil {
		t.Fatalf("WriteCredentials() = %v", err)
	}

	got, err := ReadCredentials(path)
	if err != nil {
		t.Fatalf("ReadCredentials() = %v", err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}

	// The round trip above is the point and it matters everywhere. Only the
	// mode assertion below is Unix-only: Windows does not carry permission
	// bits, and os.WriteFile there reports 0666 whatever it was given.
	if runtime.GOOS == "windows" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %04o, want 0600", mode)
	}
}

func TestAMissingCredentialFileIsEmptyRatherThanAnError(t *testing.T) {
	t.Parallel()

	got, err := ReadCredentials(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("ReadCredentials() on a missing file = %v, want no error", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadCredentials() = %v, want nothing", got)
	}
}

func TestNoCredentialsWritesNoFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secrets.env")
	if err := WriteCredentials(path, map[string]string{}); err != nil {
		t.Fatalf("WriteCredentials() = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an empty credential set still wrote a file")
	}
}

func TestBlankValuesAreNotWritten(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secrets.env")
	if err := WriteCredentials(path, map[string]string{
		"OPENARITY_OBJECTS_ACCESS_KEY": "real",
		"OPENARITY_OBJECTS_SECRET_KEY": "",
	}); err != nil {
		t.Fatalf("WriteCredentials() = %v", err)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if strings.Contains(string(raw), "SECRET_KEY") {
		t.Errorf("a blank value was written:\n%s", raw)
	}
}

// An S3 store with no bucket and an external secret store with no address are
// both installs that finish and then fail at first use.
func TestSettingsThatCannotWorkAreRefusedAtSetup(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		s    Settings
	}{
		{"s3 with no bucket", Settings{ObjectsBackend: "s3", SecretsBackend: "static"}},
		{"openbao with no address", Settings{ObjectsBackend: "filesystem", SecretsBackend: "openbao"}},
		{"an invented object store", Settings{ObjectsBackend: "minio", SecretsBackend: "static"}},
		{"an invented secret store", Settings{ObjectsBackend: "filesystem", SecretsBackend: "1password"}},
	} {
		if err := tc.s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", tc.name)
		}
	}
}

// The brain has three object backends and minio is not one of them. What makes
// an S3 store MinIO is the endpoint, so the distinction exists for the
// installer — which has to download and supervise one — and for nothing else.
func TestMinIOReachesTheBrainAsS3(t *testing.T) {
	t.Parallel()

	s := Settings{
		ObjectsBackend:  "minio",
		MinIOPath:       "/data/minio",
		ObjectsEndpoint: "http://127.0.0.1:21900",
		ObjectsBucket:   "openarity",
		SecretsBackend:  "static",
	}

	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !s.RunsMinIO() {
		t.Error("RunsMinIO() = false for a minio install")
	}

	for _, entry := range s.Env() {
		if entry == "OPENARITY_OBJECTS_BACKEND=minio" {
			t.Error("the brain was told about a backend it does not have")
		}
	}

	env := strings.Join(s.Env(), " ")
	if !strings.Contains(env, "OPENARITY_OBJECTS_BACKEND=s3") {
		t.Errorf("a minio install did not reach the brain as s3:\n%s", env)
	}
	if !strings.Contains(env, "OPENARITY_OBJECTS_ENDPOINT=http://127.0.0.1:21900") {
		t.Errorf("a minio install carried no endpoint:\n%s", env)
	}
}

// Someone who points at a bucket they already have is not running one, and
// must not have a MinIO started for them.
func TestPlainS3DoesNotRunMinIO(t *testing.T) {
	t.Parallel()

	s := Settings{ObjectsBackend: "s3", ObjectsBucket: "theirs", SecretsBackend: "static"}
	if s.RunsMinIO() {
		t.Error("RunsMinIO() = true for a bucket somebody else runs")
	}
}

// Without a directory there is nothing for MinIO to serve, and the failure
// would arrive when the first file is written rather than at setup.
func TestMinIOWithNowhereToPutFilesIsRefused(t *testing.T) {
	t.Parallel()

	s := Settings{ObjectsBackend: "minio", SecretsBackend: "static"}
	if err := s.Validate(); err == nil {
		t.Error("a MinIO install with no data directory validated")
	}
}
