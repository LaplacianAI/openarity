package config

import (
	"strings"
	"testing"
)

// Which implementation to use is named, not inferred.
//
// secrets used to be inferred — AppRole credentials present meant OpenBao,
// absent meant the in-memory map. That works for two options where one is
// clearly "not configured". It does not survive a third: with a volume and an
// S3 store both real choices, "endpoint set" and "path set" can both be true,
// both false, or contradict each other, and every answer is a guess.
func TestBackendDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.SecretsBackend != SecretsBackendStatic {
		t.Errorf("SecretsBackend = %q, want %q", cfg.SecretsBackend, SecretsBackendStatic)
	}
	if cfg.ObjectsBackend != ObjectsBackendMemory {
		t.Errorf("ObjectsBackend = %q, want %q", cfg.ObjectsBackend, ObjectsBackendMemory)
	}
}

func TestEveryBackendValueParses(t *testing.T) {
	t.Parallel()

	for _, backend := range []SecretsBackend{
		SecretsBackendStatic, SecretsBackendOpenBao, SecretsBackendVault,
	} {
		t.Run("secrets/"+string(backend), func(t *testing.T) {
			t.Parallel()

			environ := map[string]string{"OPENARITY_SECRETS_BACKEND": string(backend)}
			if backend != SecretsBackendStatic {
				environ["OPENARITY_SECRETS_APPROLE_ID"] = "role-abc"
				environ["OPENARITY_SECRETS_APPROLE_SECRET"] = "approlesecret"
			}

			cfg, err := load(environ)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.SecretsBackend != backend {
				t.Errorf("SecretsBackend = %q, want %q", cfg.SecretsBackend, backend)
			}
		})
	}

	for _, backend := range []ObjectsBackend{
		ObjectsBackendMemory, ObjectsBackendFilesystem, ObjectsBackendS3,
	} {
		t.Run("objects/"+string(backend), func(t *testing.T) {
			t.Parallel()

			environ := map[string]string{"OPENARITY_OBJECTS_BACKEND": string(backend)}
			if backend == ObjectsBackendS3 {
				environ["OPENARITY_OBJECTS_ENDPOINT"] = "http://minio:9000"
			}

			cfg, err := load(environ)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.ObjectsBackend != backend {
				t.Errorf("ObjectsBackend = %q, want %q", cfg.ObjectsBackend, backend)
			}
		})
	}
}

// A typo has to fail at boot naming what is allowed. Silently falling back to
// the store that holds nothing is how a staging brain looks healthy and loses
// every attachment.
func TestAnUnknownBackendIsRefusedByName(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ key, value, wants string }{
		"secrets, wrong case": {"OPENARITY_SECRETS_BACKEND", "OpenBao", "openbao"},
		"secrets, invented":   {"OPENARITY_SECRETS_BACKEND", "consul", "vault"},
		"objects, wrong case": {"OPENARITY_OBJECTS_BACKEND", "S3", "s3"},
		"objects, invented":   {"OPENARITY_OBJECTS_BACKEND", "gcs", "filesystem"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(map[string]string{tc.key: tc.value})
			if err == nil {
				t.Fatalf("load accepted %s=%s", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Errorf("the error does not repeat the bad value %q: %v", tc.value, err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not list the valid values (%q missing): %v", tc.wants, err)
			}
		})
	}
}

// Both fallbacks hold their contents in memory and lose them on restart. In
// development that is the point. Outside it, a brain that starts and quietly
// loses every attachment is worse than one that refuses to start.
func TestTheInMemoryFallbacksAreRefusedOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ key, value, wants string }{
		"static secrets": {"OPENARITY_SECRETS_BACKEND", "static", "SECRETS_BACKEND"},
		"memory objects": {"OPENARITY_OBJECTS_BACKEND", "memory", "OBJECTS_BACKEND"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(prodEnv(EnvironmentProduction, map[string]string{
				"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
				"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
				tc.key:                             tc.value,
			}))
			if err == nil {
				t.Fatalf("load accepted %s=%s in production", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not name %s: %v", tc.wants, err)
			}
		})
	}
}

// The bug this replaces. "OBJECTS_ENDPOINT is required outside development"
// was written when S3 was the only reachable backend, and it would refuse to
// start a single-host deployment that keeps attachments on a volume — a
// configuration the design explicitly allows.
func TestAVolumeNeedsNoEndpointOutsideDevelopment(t *testing.T) {
	t.Parallel()

	cfg, err := load(prodEnv(EnvironmentProduction, map[string]string{
		"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
		"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
		"OPENARITY_OBJECTS_BACKEND":        "filesystem",
		"OPENARITY_OBJECTS_PATH":           "/var/lib/openarity/objects",
	}))
	if err != nil {
		t.Fatalf("load rejected a volume-backed production brain: %v", err)
	}
	if cfg.ObjectsPath != "/var/lib/openarity/objects" {
		t.Errorf("ObjectsPath = %q", cfg.ObjectsPath)
	}
}

// Each backend requires only its own settings, and says which is missing.
func TestABackendRequiresItsOwnSettings(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		environ map[string]string
		wants   string
	}{
		"s3 with no endpoint": {
			map[string]string{"OPENARITY_OBJECTS_BACKEND": "s3"},
			"OBJECTS_ENDPOINT",
		},
		"openbao with no credentials": {
			map[string]string{"OPENARITY_SECRETS_BACKEND": "openbao"},
			"SECRETS_APPROLE_ID",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(tc.environ)
			if err == nil {
				t.Fatalf("load accepted %s", name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not name %s: %v", tc.wants, err)
			}
		})
	}
}

// A setting for a backend that is not selected is ignored rather than
// validated. Leaving an old OBJECTS_ENDPOINT behind while switching to a
// volume is untidy, not an error, and refusing it would make switching
// backends a two-step change.
func TestSettingsForAnUnselectedBackendAreIgnored(t *testing.T) {
	t.Parallel()

	if _, err := load(map[string]string{
		"OPENARITY_OBJECTS_BACKEND":  "filesystem",
		"OPENARITY_OBJECTS_PATH":     "/var/lib/openarity/objects",
		"OPENARITY_OBJECTS_ENDPOINT": "",
		"OPENARITY_OBJECTS_BUCKET":   "left-over",
	}); err != nil {
		t.Errorf("load rejected a leftover setting for an unselected backend: %v", err)
	}
}

// The backend name is not a secret and an operator needs it in the first line
// of a startup log — it answers "why are my attachments not appearing" before
// anything else does.
func TestStringNamesBothBackends(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OBJECTS_BACKEND": "filesystem",
		"OPENARITY_OBJECTS_PATH":    "/var/lib/openarity/objects",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()
	for _, want := range []string{"static", "filesystem"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() omits the backend %q: %s", want, s)
		}
	}
}

// A volume needs no configuration at all: OBJECTS_PATH has a default, and an
// env var set to the empty string falls back to it rather than overriding it
// with emptiness. That is what makes the "path is required" check unreachable
// and why validate does not carry one.
func TestAVolumeAlwaysHasAPath(t *testing.T) {
	t.Parallel()

	for name, environ := range map[string]map[string]string{
		"unset":        {"OPENARITY_OBJECTS_BACKEND": "filesystem"},
		"set to empty": {"OPENARITY_OBJECTS_BACKEND": "filesystem", "OPENARITY_OBJECTS_PATH": ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg, err := load(environ)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.ObjectsPath == "" {
				t.Error("ObjectsPath is empty, so the volume adapter has nowhere to write")
			}
		})
	}
}
