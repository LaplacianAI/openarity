package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
)

// schemes
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

	if err := checkURL("VAULT_ADDR", c.VaultAddr, httpSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkURL("OMNI_ROUTE_URL", c.OmniRouteURL, httpSchemes...); err != nil {
		errs = append(errs, err)
	}

	if c.FalkorDBURL == c.RedisURL {
		errs = append(errs, fmt.Errorf("FALKOR_DB_URL and REDIS_URL must differ"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %v", errs)
	}

	return nil
}
