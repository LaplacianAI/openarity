package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var (
	httpSchemes     = []string{"http", "https"}
	redisSchemes    = []string{"redis", "rediss"}
	postgresSchemes = []string{"postgres", "postgresql"}
)

func checkHostPort(field, v string) error {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", field, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s %q: bad port", field, v)
	}
	return nil
}

func checkURL(field, v string, schemes ...string) error {
	url, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", field, err)
	}
	if url.Scheme == "" {
		return fmt.Errorf("%s must have a scheme", field)
	}
	if url.Host == "" {
		return fmt.Errorf("%s must have a host", field)
	}
	if !slices.Contains(schemes, url.Scheme) {
		return fmt.Errorf("%s must have one of the following schemes: %v", field, schemes)
	}
	return nil
}

func (c *Config) Validate() error {
	var errs []error

	if err := checkHostPort("API_BIND", c.APIBind); err != nil {
		errs = append(errs, err)
	}

	if err := checkHostPort("WEBHOOK_BIND", c.WebhookBind); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("POSTGRES_DSN", c.PostgresDSN, postgresSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("REDIS_URL", c.RedisURL, redisSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("FALKOR_DB_URL", c.FalkorDBURL, redisSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("SECRETS_ADDR", c.SecretsAddr, httpSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("OMNI_ROUTE_URL", c.OmniRouteURL, httpSchemes...); err != nil {
		errs = append(errs, err)
	}

	if c.FalkorDBURL == c.RedisURL {
		errs = append(errs, fmt.Errorf("FALKOR_DB_URL and REDIS_URL must differ"))
	}

	if c.OIDCEnabled {
		if err := checkURL("OIDC_ISSUER", c.OIDCIssuer, httpSchemes...); err != nil {
			errs = append(errs, err)
		}
		if c.OIDCAudience == "" {
			errs = append(errs, fmt.Errorf("OIDC_AUDIENCE must be set when OIDC_ENABLED is true"))
		}
	}

	for i, sub := range c.SuperAdmins {
		if sub == "" || sub != strings.TrimSpace(sub) {
			errs = append(errs, fmt.Errorf(
				"SUPER_ADMINS entry %d is %q — entries must not be empty or padded with whitespace", i, sub))
		}
	}

	if c.DevToken != "" && c.Environment != EnvironmentDevelopment {
		errs = append(errs, fmt.Errorf("DEV_TOKEN must not be set outside development, got ENVIRONMENT=%s", c.Environment))
	}

	if c.Environment != EnvironmentDevelopment && c.SecretsAppRoleID == "" {
		errs = append(errs, fmt.Errorf(
			"SECRETS_APPROLE_ID and SECRETS_APPROLE_SECRET are required outside "+
				"development, got ENVIRONMENT=%s: the secret store is a dependency, "+
				"not a feature flag", c.Environment))
	}

	if (c.SecretsAppRoleID == "") != (c.SecretsAppRoleSecret == "") {
		missing := "SECRETS_APPROLE_ID"
		if c.SecretsAppRoleID != "" {
			missing = "SECRETS_APPROLE_SECRET"
		}
		errs = append(errs, fmt.Errorf(
			"%s is required when the other half of the AppRole credential is set",
			missing))
	}

	if c.OIDCEnabled && slices.Contains(c.SuperAdmins, "dev") {
		errs = append(errs, fmt.Errorf(
			"SUPER_ADMINS must not contain \"dev\" when OIDC_ENABLED is true: "+
				"it is the development token's subject, and an identity-provider "+
				"account of the same name would match it"))
	}

	if c.Environment != EnvironmentDevelopment && c.ObjectsEndpoint == "" {
		errs = append(errs, fmt.Errorf(
			"OBJECTS_ENDPOINT is required outside development, got ENVIRONMENT=%s: "+
				"message attachments have nowhere to go without it", c.Environment))
	}

	if (c.ObjectsAccessKey == "") != (c.ObjectsSecretKey == "") {
		missing := "OBJECTS_ACCESS_KEY"
		if c.ObjectsAccessKey != "" {
			missing = "OBJECTS_SECRET_KEY"
		}
		errs = append(errs, fmt.Errorf(
			"%s is required when the other half of the object store credential is set",
			missing))
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %v", errs)
	}

	return nil
}
