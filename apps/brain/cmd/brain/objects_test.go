package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

func objectsAt(backend config.ObjectsBackend, cfg *config.Config) *config.Config {
	cfg.ObjectsBackend = backend
	return cfg
}

// Each name has to produce a store that works, or naming it is theatre.
func TestEachObjectBackendProducesAWorkingStore(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		backend config.ObjectsBackend
		build   func(t *testing.T) *config.Config
	}{
		"memory": {
			config.ObjectsBackendMemory,
			func(*testing.T) *config.Config { return &config.Config{} },
		},
		"filesystem": {
			config.ObjectsBackendFilesystem,
			func(t *testing.T) *config.Config {
				return &config.Config{ObjectsPath: t.TempDir()}
			},
		},
		"s3": {
			config.ObjectsBackendS3,
			func(*testing.T) *config.Config {
				return &config.Config{
					ObjectsEndpoint: "http://127.0.0.1:19000",
					ObjectsRegion:   "us-east-1",
					ObjectsBucket:   "openarity",
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := newObjectStore(objectsAt(tc.backend, tc.build(t)), discardLogger())
			if err != nil {
				t.Fatalf("newObjectStore: %v", err)
			}
			if store == nil {
				t.Fatal("newObjectStore returned no store and no error")
			}
			if _, ok := store.(objects.Writer); !ok {
				t.Errorf("store is %T, want one that implements objects.Writer", store)
			}
		})
	}
}

// The two durable backends must be different from the fallback and from each
// other, or the switch could return the same thing for every name and every
// test above would still pass.
func TestEachObjectBackendProducesADifferentStore(t *testing.T) {
	t.Parallel()

	memory, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendMemory,
	}, discardLogger())
	if err != nil {
		t.Fatalf("memory: %v", err)
	}

	volume, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendFilesystem,
		ObjectsPath:    t.TempDir(),
	}, discardLogger())
	if err != nil {
		t.Fatalf("filesystem: %v", err)
	}

	if kind(memory) == kind(volume) {
		t.Errorf("memory and filesystem both produced %s", kind(memory))
	}

	// The volume writes where it was told. A store that silently used a
	// different path would pass every assertion above.
	root := t.TempDir()
	at, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendFilesystem,
		ObjectsPath:    root,
	}, discardLogger())
	if err != nil {
		t.Fatalf("filesystem at %s: %v", root, err)
	}

	key := "teams/11111111-1111-1111-1111-111111111111/objects/abc"
	writer, ok := at.(objects.Writer)
	if !ok {
		t.Fatalf("store is %T, want objects.Writer", at)
	}
	if err := writer.Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := filepath.Glob(filepath.Join(root, "teams", "*", "objects", "*")); err != nil {
		t.Fatalf("Glob: %v", err)
	}
	got, err := at.Get(t.Context(), key)
	if err != nil || string(got) != "hello" {
		t.Errorf("Get = %q, %v — the volume did not use ObjectsPath", got, err)
	}
}

// The package a store came from, which is what distinguishes the adapters now
// that each of their types is unexported and called the same thing.
func kind(s objects.Store) string {
	return strings.SplitN(strings.TrimPrefix(fmt.Sprintf("%T", s), "*"), ".", 2)[0]
}

// A misconfigured store is a boot failure, not a first-attachment failure.
func TestAMisconfiguredObjectBackendFailsAtStartup(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]*config.Config{
		"s3 with no endpoint": {
			ObjectsBackend: config.ObjectsBackendS3,
			ObjectsBucket:  "openarity",
		},
		"s3 with no bucket": {
			ObjectsBackend:  config.ObjectsBackendS3,
			ObjectsEndpoint: "http://127.0.0.1:19000",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := newObjectStore(cfg, discardLogger()); err == nil {
				t.Errorf("newObjectStore accepted %s", name)
			}
		})
	}
}

// A path that cannot be created is the volume's version of the same thing.
func TestAVolumeThatCannotBeCreatedFailsAtStartup(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendFilesystem,
		ObjectsPath:    filepath.Join(file, "objects"),
	}, discardLogger())
	if err == nil {
		t.Error("newObjectStore accepted a path underneath a regular file")
	}
}

// The in-memory store loses everything on restart with no error at either
// end, so the warning is the only thing that says so. Stronger wording than
// the secret store's on purpose: that one fails loudly, this one does not.
func TestTheMemoryStoreSaysAttachmentsAreLost(t *testing.T) {
	t.Parallel()

	logger, buf := capturingLogger()
	if _, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendMemory,
	}, logger); err != nil {
		t.Fatalf("newObjectStore: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("the in-memory object store was not announced at WARN: %s", out)
	}
	if !strings.Contains(out, "lost on restart") {
		t.Errorf("the warning does not say attachments are lost: %s", out)
	}
}

// The durable backends must not warn: a warning on every start is a warning
// nobody reads by the third deploy.
func TestADurableObjectBackendIsSilent(t *testing.T) {
	t.Parallel()

	logger, buf := capturingLogger()
	if _, err := newObjectStore(&config.Config{
		ObjectsBackend: config.ObjectsBackendFilesystem,
		ObjectsPath:    t.TempDir(),
	}, logger); err != nil {
		t.Fatalf("newObjectStore: %v", err)
	}

	if out := buf.String(); strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("a durable object store warned at startup: %s", out)
	}
}
