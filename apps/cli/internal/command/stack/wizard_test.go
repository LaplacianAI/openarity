package stack

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

		settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
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

	settings, _, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if settings.ModelPath != "" {
		t.Errorf("ModelPath = %q, want nothing — no gateway is installed", settings.ModelPath)
	}
}

// A secret store is a dependency, not a feature flag: the brain refuses to
// start without an AppRole, and so does `brain migrate up`.
//
// The installer window asked for the address and not for the AppRole, so
// choosing OpenBao failed several minutes in — after 70MB of Postgres and a
// cluster — with "validation failed: SECRETS_APPROLE_ID and
// SECRETS_APPROLE_SECRET are required". Refusing it costs a second.
func TestAnExternalSecretStoreWithNoAppRoleIsRefusedBeforeAnythingIsDone(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"openbao", "vault"} {
		w := newWizard(quietOptions(), true, Answers{
			Objects: "filesystem", Secrets: backend,
			Address: "http://127.0.0.1:8200",
		}, "/an/install")

		_, _, err := w.Run(t.Context())
		if err == nil {
			t.Fatalf("Run() with %s and no AppRole = nil, want a refusal", backend)
		}
		for _, want := range []string{"APPROLE_ID", "APPROLE_SECRET"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Run() = %q, want it to name %s", err, want)
			}
		}
	}
}

// Half an AppRole is as unusable as none, and says which half is missing.
func TestHalfAnAppRoleSaysWhichHalfIsMissing(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "an-id")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: "http://127.0.0.1:8200",
	}, "/an/install")

	_, _, err := w.Run(t.Context())
	if err == nil {
		t.Fatal("Run() with half an AppRole = nil, want a refusal")
	}
	if strings.Contains(err.Error(), "APPROLE_ID and") {
		t.Errorf("Run() = %q, want it to name only the missing half", err)
	}
	if !strings.Contains(err.Error(), "APPROLE_SECRET") {
		t.Errorf("Run() = %q, want it to name the secret", err)
	}
}

// Given both, it proceeds. The credentials arrive in the environment because
// argv is readable by every process on the machine.
func TestAnAppRoleGivenInTheEnvironmentIsAccepted(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "an-id")
	t.Setenv("OPENARITY_SECRETS_APPROLE_SECRET", "a-secret")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: "http://127.0.0.1:8200",
	}, "/an/install")

	_, creds, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() with a whole AppRole = %v", err)
	}
	if creds["OPENARITY_SECRETS_APPROLE_ID"] != "an-id" {
		t.Error("the AppRole id did not survive")
	}
}

// Keeping secrets in the brain needs no AppRole, and demanding one would make
// the ordinary install fail.
func TestKeepingSecretsInTheBrainNeedsNoAppRole(t *testing.T) {
	t.Parallel()

	w := newWizard(quietOptions(), true, Answers{Objects: "filesystem", Secrets: "static"}, "/an/install")

	if _, _, err := w.Run(t.Context()); err != nil {
		t.Errorf("Run() = %v, want the ordinary install to proceed", err)
	}
}

// Minting is a choice because it costs an admin token, which can do anything
// to that server. The token is used for the six calls that create the role and
// never written down; only the AppRole reaches the credentials file.
func TestMintingAsksTheServerAndKeepsOnlyTheAppRole(t *testing.T) {
	const admin = "an-admin-token-that-must-not-be-stored"
	t.Setenv(adminToken, admin)

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/role-id"):
			_, _ = w.Write([]byte(`{"data":{"role_id":"minted-id"}}`))
		case strings.HasSuffix(r.URL.Path, "/secret-id"):
			_, _ = w.Write([]byte(`{"data":{"secret_id":"minted-secret"}}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao",
		Address: server.URL, KVMount: "secret", SecretsAuth: "mint",
	}, "/an/install")

	_, creds, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if creds["OPENARITY_SECRETS_APPROLE_ID"] != "minted-id" {
		t.Errorf("the AppRole id is %q, want the one the server minted", creds["OPENARITY_SECRETS_APPROLE_ID"])
	}
	for key, value := range creds {
		if value == admin {
			t.Errorf("%s holds the admin token, which must not be stored", key)
		}
	}
	if len(seen) < 6 {
		t.Errorf("the server saw %d calls, want the six that create a role: %v", len(seen), seen)
	}
}

// Pasting is the other half, and must not reach the server at all.
func TestPastingAnAppRoleContactsNothing(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "a-pasted-id")
	t.Setenv("OPENARITY_SECRETS_APPROLE_SECRET", "a-pasted-secret")

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the secret store was contacted for an AppRole that was pasted")
	}))
	t.Cleanup(server.Close)

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: server.URL, SecretsAuth: "paste",
	}, "/an/install")

	_, creds, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if creds["OPENARITY_SECRETS_APPROLE_ID"] != "a-pasted-id" {
		t.Error("the pasted AppRole did not survive")
	}
}

// Keeping secrets in the brain contacts nothing either, whatever was asked
// for, because there is no server to ask.
func TestMintingIsSkippedWithoutASecretStore(t *testing.T) {
	t.Setenv(adminToken, "an-admin-token")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "static", SecretsAuth: "mint",
	}, "/an/install")

	if _, _, err := w.Run(t.Context()); err != nil {
		t.Errorf("Run() = %v, want the ordinary install to proceed", err)
	}
}
