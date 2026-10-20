package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var (
	httpSchemes     = []string{"http", "https"}
	redisSchemes    = []string{"redis", "rediss"}
	postgresSchemes = []string{"postgres", "postgresql"}
)

const maxEmbeddingDimensions = 4096

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
		return fmt.Errorf("%s cannot be parsed as a URL — a password holding / ? # @ : or %% has to be "+
			"percent-encoded; the value is not repeated here, in case it carries one", field)
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

// checkWebhookPublicURL allows the setting to be empty, which means the brain
// has no public address and hook URLs are left out. When set, it is handed to
// every client that lists a channel, so it may carry nothing private, and it
// is a base: the hook's path is appended to it.
func checkWebhookPublicURL(v string, env Environment) error {
	if v == "" {
		return nil
	}
	if err := checkURL("WEBHOOK_PUBLIC_URL", v, httpSchemes...); err != nil {
		return err
	}
	u, _ := url.Parse(v) // checkURL has just parsed it
	switch {
	case u.User != nil:
		return errors.New("WEBHOOK_PUBLIC_URL holds a user or password, and every client is given it — take them out")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return errors.New("WEBHOOK_PUBLIC_URL is a base address, so it cannot have a query or a fragment")
	case u.Scheme != "https" && env != EnvironmentDevelopment:
		return fmt.Errorf("WEBHOOK_PUBLIC_URL must be https outside development, got ENVIRONMENT=%s", env)
	}
	return nil
}

var hostPattern = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func checkHosts(field string, hosts []string) error {
	for i, h := range hosts {
		switch {
		case strings.Contains(h, "@"):
			return fmt.Errorf("%s entry %d holds an @, so it may carry a credential and is not repeated here — "+
				"write a host name alone, such as hub.example.com", field, i)
		case !hostPattern.MatchString(h):
			return fmt.Errorf("%s entry %d is %q — write a host name alone, such as hub.example.com, with no scheme, port or path", field, i, h)
		}
	}
	return nil
}

func checkAPIKey(field, v string) error {
	if strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("%s holds a space, a line break or a control character, which no key has — "+
			"check for a trailing newline or quotes around a blank; the value is not repeated here", field)
	}
	return nil
}

var linkLocal = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

func checkPrivateNetworks(field string, nets []netip.Prefix) error {
	for i, p := range nets {
		switch {
		case !p.IsValid():
			return fmt.Errorf("%s entry %d is empty — check for a trailing comma", field, i)
		case p.Addr().Is4In6():
			return fmt.Errorf("%s entry %s is an IPv4-mapped prefix — write it as IPv4", field, p)
		case p != p.Masked():
			return fmt.Errorf("%s entry %s has host bits set — did you mean %s?", field, p, p.Masked())
		}
		for _, ll := range linkLocal {
			if p.Overlaps(ll) {
				return fmt.Errorf("%s entry %s overlaps %s, where cloud metadata services answer, which is never dialled", field, p, ll)
			}
		}
	}
	return nil
}

func checkEmbedding(baseURL, apiKey, model string, dims int) error {
	switch {
	case model != strings.TrimSpace(model):
		return fmt.Errorf("EMBEDDING_MODEL %q has a space at one end", model)
	case (model == "") != (dims == 0):
		return errors.New("EMBEDDING_MODEL and EMBEDDING_DIMENSIONS are set together or not at all: " +
			"the index is built for one model's dimensions")
	case dims < 0 || dims > maxEmbeddingDimensions:
		return fmt.Errorf("EMBEDDING_DIMENSIONS is %d, and must be between 1 and %d", dims, maxEmbeddingDimensions)
	case model != "" && baseURL == "":
		return errors.New("EMBEDDING_MODEL is set but EMBEDDING_BASE_URL is not, so there is nowhere to ask for a vector")
	case model == "" && baseURL != "":
		return errors.New("EMBEDDING_BASE_URL is set but EMBEDDING_MODEL is not, so nothing would use it")
	case model == "" && apiKey != "":
		return errors.New("EMBEDDING_API_KEY is set but EMBEDDING_MODEL is not, so nothing would use it")
	case model == "":
		return nil
	}
	if err := checkURL("EMBEDDING_BASE_URL", baseURL, httpSchemes...); err != nil {
		return err
	}
	return checkAPIKey("EMBEDDING_API_KEY", apiKey)
}

func (c *Config) Validate() error {
	var errs []error

	if err := checkHostPort("API_BIND", c.APIBind); err != nil {
		errs = append(errs, err)
	}

	if err := checkHostPort("WEBHOOK_BIND", c.WebhookBind); err != nil {
		errs = append(errs, err)
	}

	if err := checkWebhookPublicURL(c.WebhookPublicURL, c.Environment); err != nil {
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

	if err := checkURL("LLM_BASE_URL", c.LLMBaseURL, httpSchemes...); err != nil {
		errs = append(errs, err)
	}

	if err := checkAPIKey("LLM_API_KEY", c.LLMAPIKey); err != nil {
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

	if err := checkHosts("SKILL_IMPORT_HOSTS", c.SkillImportHosts); err != nil {
		errs = append(errs, err)
	}

	if err := checkAPIKey("SKILL_IMPORT_GITHUB_TOKEN", c.SkillImportGitHubToken); err != nil {
		errs = append(errs, err)
	}

	if err := checkPrivateNetworks("MCP_PRIVATE_NETWORKS", c.MCPPrivateNetworks); err != nil {
		errs = append(errs, err)
	}

	if err := checkEmbedding(c.EmbeddingBaseURL, c.EmbeddingAPIKey, c.EmbeddingModel, c.EmbeddingDimensions); err != nil {
		errs = append(errs, err)
	}

	if c.DevToken != "" && c.Environment != EnvironmentDevelopment {
		errs = append(errs, fmt.Errorf("DEV_TOKEN must not be set outside development, got ENVIRONMENT=%s", c.Environment))
	}

	if c.Environment != EnvironmentDevelopment {
		if c.SecretsBackend == SecretsBackendStatic {
			errs = append(errs, fmt.Errorf(
				"SECRETS_BACKEND=static is refused outside development, got "+
					"ENVIRONMENT=%s: it holds secrets in the process and loses them "+
					"on restart, so every channel stops verifying after a deploy",
				c.Environment))
		}
		if c.ObjectsBackend == ObjectsBackendMemory {
			errs = append(errs, fmt.Errorf(
				"OBJECTS_BACKEND=memory is refused outside development, got "+
					"ENVIRONMENT=%s: attachments would be lost on restart, silently — "+
					"uploads and downloads both succeed until the process dies",
				c.Environment))
		}
	}

	switch c.SecretsBackend {
	case SecretsBackendOpenBao, SecretsBackendVault:
		if c.SecretsAppRoleID == "" {
			errs = append(errs, fmt.Errorf(
				"SECRETS_APPROLE_ID and SECRETS_APPROLE_SECRET are required when "+
					"SECRETS_BACKEND=%s: the secret store is a dependency, not a "+
					"feature flag", c.SecretsBackend))
		}
	case SecretsBackendStatic:
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

	switch c.ObjectsBackend {
	case ObjectsBackendS3, ObjectsBackendSeaweedFS:
		if c.ObjectsEndpoint == "" {
			errs = append(errs, fmt.Errorf(
				"OBJECTS_ENDPOINT is required when OBJECTS_BACKEND=s3 or OBJECTS_BACKEND=seaweedfs: message "+
					"attachments have nowhere to go without it"))
		}
	case ObjectsBackendFilesystem:
		// Nothing to require. OBJECTS_PATH carries a default, and an env var
		// set to the empty string falls back to it — measured, not assumed —
		// so the path is never empty and a check for it could never fire. An
		// unreachable guard reads as protection and cannot be tested.
	case ObjectsBackendMemory:
		// Nothing to configure, and refused outside development above.
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
