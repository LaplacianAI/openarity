package config

import (
	"strings"
	"testing"
)

// prodEnv is a non-development environment that is otherwise legal: OIDC
// configured and no dev token, since a dev token outside development is
// refused on its own.
func prodEnv(environment Environment, extra map[string]string) map[string]string {
	// The object store endpoint is part of a valid non-development config now,
	// the same way OIDC is. A test that wants it absent deletes it — see
	// TestAnEndpointIsRequiredOutsideDevelopment — rather than every other
	// test having to remember to supply it.
	return mergeEnv(map[string]string{
		"OPENARITY_ENVIRONMENT":      string(environment),
		"OPENARITY_OIDC_ENABLED":     "true",
		"OPENARITY_OIDC_ISSUER":      "https://auth.example.com/application/o/openarity/",
		"OPENARITY_SECRETS_BACKEND":  "openbao",
		"OPENARITY_OBJECTS_BACKEND":  "s3",
		"OPENARITY_OBJECTS_ENDPOINT": "http://minio:9000",
	}, extra)
}

// Half a credential is a deployment someone stopped configuring. Left alone
// it fails on the first secret read, in production, at request time —
// whereas refusing at boot fails once, loudly, where it is being changed.
func TestAppRoleCredentialsAreBothOrNeither(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"id alone": {
			env:  map[string]string{"OPENARITY_SECRETS_APPROLE_ID": "role-abc"},
			want: "SECRETS_APPROLE_SECRET",
		},
		"secret alone": {
			env:  map[string]string{"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret"},
			want: "SECRETS_APPROLE_ID",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(devEnv(tc.env))
			if err == nil {
				t.Fatalf("load succeeded, want an error naming %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name the missing half %s: %v", tc.want, err)
			}
		})
	}
}

// The secret store is a dependency, not a feature flag. Without this a
// staging brain starts on an in-memory store that holds nothing, looks
// healthy, and fails every channel on its first webhook.
//
// Every non-development environment is covered, the same blast radius as
// the DEV_TOKEN rule this follows.
func TestAppRoleIsRequiredOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for _, environment := range []Environment{
		EnvironmentProduction, EnvironmentStaging, EnvironmentTest,
	} {
		t.Run(string(environment), func(t *testing.T) {
			t.Parallel()

			_, err := load(prodEnv(environment, nil))
			if err == nil {
				t.Fatal("load succeeded with no AppRole credentials")
			}
			if !strings.Contains(err.Error(), "SECRETS_APPROLE_ID") {
				t.Errorf("error does not name SECRETS_APPROLE_ID: %v", err)
			}
		})
	}
}

// The positive case matters as much as the negative one: without it, a guard
// that rejected every non-development config would pass the test above.
func TestAppRoleSatisfiesTheRequirementOutsideDevelopment(t *testing.T) {
	t.Parallel()

	cfg, err := load(prodEnv(EnvironmentProduction, map[string]string{
		"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
		"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SecretsAppRoleID != "role-abc" {
		t.Errorf("SecretsAppRoleID = %q, want role-abc", cfg.SecretsAppRoleID)
	}
}

// Development must stay runnable with nothing but Go and Postgres — the same
// bargain DEV_TOKEN strikes for authentication. A contributor who needs a
// secret store running to build is a contributor who stops.
func TestAppRoleIsOptionalInDevelopment(t *testing.T) {
	t.Parallel()

	if _, err := load(devEnv(nil)); err != nil {
		t.Errorf("development load without AppRole credentials: %v", err)
	}
}
