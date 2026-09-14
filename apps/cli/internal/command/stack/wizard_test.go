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

// The wizard writes nowhere and asks nothing in these tests: they answer with
// flags, which is the path the installer window takes and the path a script
// takes.
func quietOptions() *cli.Options {
	return &cli.Options{
		Stdout:         io.Discard,
		Stderr:         io.Discard,
		NonInteractive: true,
	}
}

// One flag is enough. Requiring two meant `--secrets openbao` on its own was
// discarded in silence and the install kept credentials in the brain's own
// process — a place nobody chose, reported nowhere.
func TestOneAnswerIsEnoughToBeTakenAsTheAnswers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a    Answers
		want bool
	}{
		{"nothing", Answers{}, false},
		{"only objects", Answers{Objects: "filesystem"}, true},
		{"only secrets", Answers{Secrets: "openbao"}, true},
		{"only the secret store's address", Answers{Address: "http://127.0.0.1:28200"}, true},
		{"only the model backend", Answers{ModelBackend: "litellm"}, true},
		{"both backends", Answers{Objects: "filesystem", Secrets: "static"}, true},
	} {
		if got := tc.a.any(); got != tc.want {
			t.Errorf("%s: any() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The bug itself, at the level it was found: one flag, and the setting it
// names is the setting that ends up in the install.
func TestASingleAnswerReachesTheSettings(t *testing.T) {
	t.Parallel()

	base := engine.DefaultSettings()
	got := Answers{Secrets: "openbao", Address: "http://127.0.0.1:28200"}.settings(base)

	if got.SecretsBackend != "openbao" {
		t.Errorf("SecretsBackend = %q, want the one the flag named", got.SecretsBackend)
	}
	if got.SecretsAddr != "http://127.0.0.1:28200" {
		t.Errorf("SecretsAddr = %q, want the one the flag named", got.SecretsAddr)
	}
	// And nothing it did not name was blanked on the way through.
	if got.ObjectsBackend != base.ObjectsBackend {
		t.Errorf("ObjectsBackend = %q, want the default %q kept", got.ObjectsBackend, base.ObjectsBackend)
	}
	if got.ModelBackend != base.ModelBackend {
		t.Errorf("ModelBackend = %q, want the default %q kept", got.ModelBackend, base.ModelBackend)
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

// Every answer the window can leave empty and setup insists on, in one place.
// If a third one is added, this is what says so.
func TestEveryRequiredDirectoryIsFilledWhenBlank(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{
		Objects: "filesystem", Secrets: "static",
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
// Nobody is asked which way they want one, so with neither an AppRole nor an
// admin token the refusal has to name both ways out. It used to fail several
// minutes in — after 70MB of Postgres and a cluster — with the brain's own
// "SECRETS_APPROLE_ID and SECRETS_APPROLE_SECRET are required".
func TestNeitherAnAppRoleNorATokenIsRefusedBeforeAnythingIsDone(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"openbao", "vault"} {
		w := newWizard(quietOptions(), true, Answers{
			Objects: "filesystem", Secrets: backend,
			Address: aStoreThatAnswers(t),
		}, "/an/install")

		_, _, err := w.Run(t.Context())
		if err == nil {
			t.Fatalf("Run() with %s, no AppRole and no token = nil, want a refusal", backend)
		}
		for _, want := range []string{"APPROLE_ID", "APPROLE_SECRET", "administer"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Run() = %q, want it to mention %s", err, want)
			}
		}
	}
}

// Half an AppRole is as unusable as none, and with no admin token to fall back
// on the refusal is the same one: it names both ways out rather than guessing
// which half was meant.
func TestHalfAnAppRoleIsTreatedAsNone(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "an-id")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: aStoreThatAnswers(t),
	}, "/an/install")

	_, _, err := w.Run(t.Context())
	if err == nil {
		t.Fatal("Run() with half an AppRole and no token = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "APPROLE_SECRET") {
		t.Errorf("Run() = %q, want it to name what is missing", err)
	}
}

// Given both, it proceeds. The credentials arrive in the environment because
// argv is readable by every process on the machine.
func TestAnAppRoleGivenInTheEnvironmentIsAccepted(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "an-id")
	t.Setenv("OPENARITY_SECRETS_APPROLE_SECRET", "a-secret")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: aStoreThatAnswers(t),
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

// Nobody is asked which way they want this. With no AppRole in hand, one is
// minted — which is the only case where the admin token is wanted.
//
// The token is used for the six calls that create the role and never written
// down; only the AppRole reaches the credentials file.
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
		Address: server.URL, KVMount: "secret",
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

// An AppRole already in hand is used as it is. The store is still contacted —
// reachability is checked however the AppRole arrives — but nothing is created,
// and no admin token is wanted.
func TestAnAppRoleAlreadyInHandCreatesNothing(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "a-pasted-id")
	t.Setenv("OPENARITY_SECRETS_APPROLE_SECRET", "a-pasted-secret")

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: server.URL,
	}, "/an/install")

	_, creds, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if creds["OPENARITY_SECRETS_APPROLE_ID"] != "a-pasted-id" {
		t.Error("the pasted AppRole did not survive")
	}

	for _, path := range seen {
		if path != "/v1/sys/health" {
			t.Errorf("the installer called %s for an AppRole that was pasted — it should create nothing", path)
		}
	}
}

// Keeping secrets in the brain contacts nothing, because there is no server to
// ask.
func TestMintingIsSkippedWithoutASecretStore(t *testing.T) {
	t.Setenv(adminToken, "an-admin-token")

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "static",
	}, "/an/install")

	if _, _, err := w.Run(t.Context()); err != nil {
		t.Errorf("Run() = %v, want the ordinary install to proceed", err)
	}
}

// A pasted AppRole against a store that is not there used to fail at
// `brain migrate up`, four minutes in. Reachability is asked whichever way the
// AppRole arrives.
func TestAnUnreachableSecretStoreIsRefusedEvenWhenTheAppRoleWasPasted(t *testing.T) {
	t.Setenv("OPENARITY_SECRETS_APPROLE_ID", "an-id")
	t.Setenv("OPENARITY_SECRETS_APPROLE_SECRET", "a-secret")

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.URL
	server.Close()

	w := newWizard(quietOptions(), true, Answers{
		Objects: "filesystem", Secrets: "openbao", Address: addr,
	}, "/an/install")

	_, _, err := w.Run(t.Context())
	if err == nil {
		t.Fatal("Run() against a store that is not there = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "nothing answered") {
		t.Errorf("Run() = %q, want it to say the store could not be reached", err)
	}
}

// A secret store that is there, for tests about what happens after that.
func aStoreThatAnswers(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// Through Run, which is where the branch was wrong: one flag and no terminal
// used to mean the defaults, silently. Objects only here, because secrets only
// would go on to mint against an address nothing is listening at.
func TestOneFlagAndNoTerminalDoesNotFallBackToTheDefaults(t *testing.T) {
	t.Parallel()

	opts := quietOptions()
	w := newWizard(opts, true, Answers{Objects: "memory"}, "/an/install")

	settings, _, err := w.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() with one answer = %v", err)
	}
	if settings.ObjectsBackend != "memory" {
		t.Errorf("ObjectsBackend = %q, want the answer that was given rather than the default",
			settings.ObjectsBackend)
	}
	if settings.SecretsBackend != engine.DefaultSettings().SecretsBackend {
		t.Errorf("SecretsBackend = %q, want the default for the question nobody answered",
			settings.SecretsBackend)
	}
}
