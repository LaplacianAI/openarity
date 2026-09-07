package stack

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// The wizard writes nowhere and asks nothing in these tests: every one of them
// supplies a complete set of answers, which is the path the installer window
// takes.
func quietOptions() *cli.Options {
	return &cli.Options{
		Stdout:         io.Discard,
		Stderr:         io.Discard,
		NonInteractive: true,
	}
}

// The window fills every answer, because a sidecar has no terminal to prompt
// at. Anything short of that has to fall back to asking or to the defaults.
func TestAnswersAreOnlyCompleteWithBothBackends(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a    Answers
		want bool
	}{
		{"nothing", Answers{}, false},
		{"only objects", Answers{Objects: "filesystem"}, false},
		{"only secrets", Answers{Secrets: "static"}, false},
		{"both", Answers{Objects: "filesystem", Secrets: "static"}, true},
	} {
		if got := tc.a.complete(); got != tc.want {
			t.Errorf("%s: complete() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnswersOverrideOnlyWhatTheyGive(t *testing.T) {
	t.Parallel()

	base := engine.DefaultSettings()
	got := Answers{
		Objects: "s3",
		Bucket:  "openarity",
		Secrets: "static",
	}.settings(base)

	if got.ObjectsBackend != "s3" || got.ObjectsBucket != "openarity" {
		t.Errorf("the given answers were not applied: %+v", got)
	}
	// Left empty, so the default survives rather than becoming an empty
	// string, which the brain validates differently from absent.
	if got.ModelBaseURL != base.ModelBaseURL {
		t.Errorf("ModelBaseURL = %q, want the default %q", got.ModelBaseURL, base.ModelBaseURL)
	}
}

// A window that offers S3 must collect a bucket with it. Failing here is the
// difference between an error before anything is created and an install that
// finishes and cannot store a file.
func TestIncompleteAnswersAreRefusedRatherThanInstalled(t *testing.T) {
	t.Parallel()

	err := Answers{Objects: "s3", Secrets: "static"}.
		settings(engine.DefaultSettings()).
		Validate()

	if err == nil {
		t.Error("an S3 answer with no bucket validated")
	}
}

// argv is readable by every process on the machine. A secret key given as a
// flag would be a secret key in `ps`, so there is no flag for one.
func TestNoCredentialIsAFlag(t *testing.T) {
	t.Parallel()

	cmd := newSetupCmd(nil, func() (engine.Layout, error) { return engine.Layout{}, nil })

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		for _, word := range []string{"secret-key", "access-key", "password", "passphrase", "approle"} {
			if f.Name == word {
				t.Errorf("--%s is a flag, and a credential on a command line is visible in ps", f.Name)
			}
		}
	})
}

// The installer window says "Blank puts it inside the install" and sends an
// empty string. Nothing turned that into anything, so setup refused with "a
// gateway of our own needs a directory to install into" — which reached a
// person as "setup exited with Some(1)".
func TestABlankGatewayPathBecomesOneInsideTheInstall(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"litellm", "omniroute"} {
		opts := quietOptions()
		w := newWizard(opts, true, Answers{
			Objects:      "filesystem",
			Secrets:      "static",
			ModelBackend: backend,
			ModelPath:    "",
		}, "/an/install")

		settings, _, err := w.Run()
		if err != nil {
			t.Fatalf("Run() with %s and no path = %v", backend, err)
		}
		if settings.ModelPath != filepath.Join("/an/install", "gateway") {
			t.Errorf("ModelPath = %q, want it inside the install", settings.ModelPath)
		}
	}
}

// The same shape, for the object store. The window says "Blank puts them
// inside the install" and sends an empty string; setup refused with "a MinIO
// of our own needs a directory to keep its data in", which reached a person as
// an exit code.
//
// This is here because fixing the gateway and not looking for its siblings
// cost a second report of the same bug.
func TestABlankMinIOPathBecomesOneInsideTheInstall(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects: "minio", Secrets: "static", Bucket: "openarity", MinIOPath: "",
	}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() with MinIO and no path = %v", err)
	}
	if settings.MinIOPath != filepath.Join("/an/install", "minio") {
		t.Errorf("MinIOPath = %q, want it inside the install", settings.MinIOPath)
	}
}

// Every answer the window can leave empty and setup insists on, in one place.
// If a third one is added, this is what says so.
func TestEveryRequiredDirectoryIsFilledWhenBlank(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects: "minio", Secrets: "static", Bucket: "openarity",
		ModelBackend: "litellm",
	}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() with both and neither path = %v", err)
	}
	if err := settings.Validate(); err != nil {
		t.Errorf("Validate() = %v, want every directory supplied", err)
	}
}

// A path that was chosen is left alone. Somebody putting a gigabyte on an
// external drive meant it.
func TestAGatewayPathThatWasGivenIsKept(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects:      "filesystem",
		Secrets:      "static",
		ModelBackend: "omniroute",
		ModelPath:    "/Volumes/big-disk/gateway",
	}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if settings.ModelPath != "/Volumes/big-disk/gateway" {
		t.Errorf("ModelPath = %q, want the one that was given", settings.ModelPath)
	}
}

func TestAMinIOPathThatWasGivenIsKept(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects: "minio", Secrets: "static", Bucket: "openarity",
		MinIOPath: "/Volumes/big-disk/minio",
	}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if settings.MinIOPath != "/Volumes/big-disk/minio" {
		t.Errorf("MinIOPath = %q, want the one that was given", settings.MinIOPath)
	}
}

// Files on this machine rather than in a MinIO of our own: no MinIO, so no
// directory, and inventing one would put a path in the state file for
// something that does not exist.
func TestFilesystemStorageGetsNoMinIODirectory(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{Objects: "filesystem", Secrets: "static"}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if settings.MinIOPath != "" {
		t.Errorf("MinIOPath = %q, want nothing — no MinIO is run", settings.MinIOPath)
	}
}

// Pointing at a gateway needs no directory, and inventing one would put a
// path in the state file for something that does not exist.
func TestPointingAtAGatewayGetsNoDirectory(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects: "filesystem", Secrets: "static", ModelBackend: "external",
	}, "/an/install")

	settings, _, err := w.Run()
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if settings.ModelPath != "" {
		t.Errorf("ModelPath = %q, want nothing — no gateway is installed", settings.ModelPath)
	}
}
