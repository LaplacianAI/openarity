package config

import (
	"strings"
	"testing"
)

// Every field carries an envDefault, because RequiredIfNoDef makes a field
// without one mandatory — a field added here without a default would refuse to
// start every existing deployment.
func TestObjectStorageDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for name, tc := range map[string]struct{ got, want string }{
		"ObjectsEndpoint":  {cfg.ObjectsEndpoint, ""},
		"ObjectsRegion":    {cfg.ObjectsRegion, "us-east-1"},
		"ObjectsBucket":    {cfg.ObjectsBucket, "openarity"},
		"ObjectsAccessKey": {cfg.ObjectsAccessKey, ""},
		"ObjectsSecretKey": {cfg.ObjectsSecretKey, ""},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", name, tc.got, tc.want)
		}
	}
}

func TestObjectStorageReadsItsEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OBJECTS_ENDPOINT":   "http://minio:9000",
		"OPENARITY_OBJECTS_REGION":     "eu-west-2",
		"OPENARITY_OBJECTS_BUCKET":     "attachments",
		"OPENARITY_OBJECTS_ACCESS_KEY": "AKIAEXAMPLE",
		"OPENARITY_OBJECTS_SECRET_KEY": "objectsecretkey",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for name, tc := range map[string]struct{ got, want string }{
		"ObjectsEndpoint":  {cfg.ObjectsEndpoint, "http://minio:9000"},
		"ObjectsRegion":    {cfg.ObjectsRegion, "eu-west-2"},
		"ObjectsBucket":    {cfg.ObjectsBucket, "attachments"},
		"ObjectsAccessKey": {cfg.ObjectsAccessKey, "AKIAEXAMPLE"},
		"ObjectsSecretKey": {cfg.ObjectsSecretKey, "objectsecretkey"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", name, tc.got, tc.want)
		}
	}
}

// String() is an allowlist: it names every field it prints, so a field added
// to the struct is invisible until someone adds it to the format string too.
// That is the safe direction, and this is what keeps it that way — adding the
// credential would be a one-word change nobody would question in review.
//
// Both halves, not just the secret. An access key is an identifier rather than
// a password, but it is half a credential and the AppRole settings set the
// precedent by printing neither.
func TestStringNeverPrintsTheObjectStoreCredentials(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OBJECTS_ENDPOINT":   "http://minio:9000",
		"OPENARITY_OBJECTS_ACCESS_KEY": "objectaccesskey",
		"OPENARITY_OBJECTS_SECRET_KEY": "objectsecretkey",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()
	for _, secret := range []string{"objectaccesskey", "objectsecretkey"} {
		if strings.Contains(s, secret) {
			t.Errorf("String() printed %q: %s", secret, s)
		}
	}
}

// Redaction must not eat what an operator needs to see. Which store and which
// bucket is the first question when attachments stop arriving.
func TestStringKeepsTheObjectStoreEndpointAndBucket(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OBJECTS_ENDPOINT": "http://minio:9000",
		"OPENARITY_OBJECTS_BUCKET":   "attachments",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()
	for _, want := range []string{"minio:9000", "attachments"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() omits %q, which an operator needs: %s", want, s)
		}
	}
}

// A credential in a URL is redacted the way every other endpoint is. Nobody
// should put one there, and somebody will.
func TestStringRedactsCredentialsInTheObjectStoreEndpoint(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OBJECTS_ENDPOINT": "http://someone:hunter2@minio:9000",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if s := cfg.String(); strings.Contains(s, "hunter2") {
		t.Errorf("String() printed a password from the endpoint: %s", s)
	}
}

// Attachments have nowhere to go without an endpoint, and finding that out
// when the first one arrives is worse than finding it out at startup.
func TestAnEndpointIsRequiredOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for name, environment := range map[string]Environment{
		"production": EnvironmentProduction,
		"staging":    EnvironmentStaging,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			environ := prodEnv(environment, map[string]string{
				"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
				"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
			})
			delete(environ, "OPENARITY_OBJECTS_ENDPOINT")

			_, err := load(environ)
			if err == nil {
				t.Fatal("load accepted no object store outside development")
			}
			if !strings.Contains(err.Error(), "OBJECTS_ENDPOINT") {
				t.Errorf("the error does not name OBJECTS_ENDPOINT: %v", err)
			}
		})
	}
}

// Development runs without one — the volume adapter and the in-process store
// both exist so that a laptop needs no object store at all.
func TestNoEndpointIsFineInDevelopment(t *testing.T) {
	t.Parallel()

	if _, err := load(map[string]string{}); err != nil {
		t.Errorf("load rejected a development brain with no object store: %v", err)
	}
}

// Half a credential is a misconfiguration that would otherwise surface as
// SignatureDoesNotMatch on the first attachment, which names nothing useful.
func TestHalfAnObjectStoreCredentialIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		accessKey, secretKey, wants string
	}{
		"access key only": {"AKIAEXAMPLE", "", "OBJECTS_SECRET_KEY"},
		"secret key only": {"", "s3cret", "OBJECTS_ACCESS_KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(map[string]string{
				"OPENARITY_OBJECTS_ACCESS_KEY": tc.accessKey,
				"OPENARITY_OBJECTS_SECRET_KEY": tc.secretKey,
			})
			if err == nil {
				t.Fatalf("load accepted %s", name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not name %s: %v", tc.wants, err)
			}
		})
	}
}

// Both halves, or neither. Neither is how a store reached over a trusted
// network or with an instance role is configured.
func TestBothOrNeitherObjectStoreCredentialIsAccepted(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ accessKey, secretKey string }{
		"both":    {"AKIAEXAMPLE", "s3cret"},
		"neither": {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := load(map[string]string{
				"OPENARITY_OBJECTS_ACCESS_KEY": tc.accessKey,
				"OPENARITY_OBJECTS_SECRET_KEY": tc.secretKey,
			})
			if err != nil {
				t.Errorf("load rejected %s: %v", name, err)
			}
		})
	}
}

// The positive case matters as much as the negative one: a guard that rejected
// every non-development config would pass the test above.
func TestAnEndpointSatisfiesTheRequirementOutsideDevelopment(t *testing.T) {
	t.Parallel()

	cfg, err := load(prodEnv(EnvironmentProduction, map[string]string{
		"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
		"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
		"OPENARITY_OBJECTS_ENDPOINT":       "http://minio:9000",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ObjectsEndpoint != "http://minio:9000" {
		t.Errorf("ObjectsEndpoint = %q, want %q", cfg.ObjectsEndpoint, "http://minio:9000")
	}
}
