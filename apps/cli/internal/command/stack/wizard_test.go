package stack

import (
	"testing"

	"github.com/spf13/pflag"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

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
	if got.ModelGatewayURL != base.ModelGatewayURL {
		t.Errorf("ModelGatewayURL = %q, want the default %q", got.ModelGatewayURL, base.ModelGatewayURL)
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
