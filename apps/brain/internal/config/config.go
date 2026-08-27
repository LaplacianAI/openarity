package config

import (
	"fmt"
	"log/slog"
	"net/url"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	/* Application Configuration */
	Environment Environment `env:"ENVIRONMENT" envDefault:"development"`
	APIBind     string      `env:"API_BIND" envDefault:"127.0.0.1:21120"`
	WebhookBind string      `env:"WEBHOOK_BIND" envDefault:"127.0.0.1:21121"`

	/* Logging Configuration */
	LogLevel slog.Level `env:"LOG_LEVEL" envDefault:"info"`

	/* Datastore Configuration */
	PostgresDSN string `env:"POSTGRES_DSN" envDefault:"postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable"`
	FalkorDBURL string `env:"FALKOR_DB_URL" envDefault:"redis://127.0.0.1:6380"`
	RedisURL    string `env:"REDIS_URL" envDefault:"redis://127.0.0.1:6379"`

	/* Secret Store Configuration */
	SecretsAddr          string `env:"SECRETS_ADDR" envDefault:"http://localhost:8200"`
	SecretsAppRoleID     string `env:"SECRETS_APPROLE_ID" envDefault:""`
	SecretsAppRoleSecret string `env:"SECRETS_APPROLE_SECRET" envDefault:""`
	SecretsKVMount       string `env:"SECRETS_KV_MOUNT" envDefault:"secret"`

	/* Model Router Configuration */
	OmniRouteURL string `env:"OMNI_ROUTE_URL" envDefault:"http://localhost:20128/v1"`

	/* Authentication Configuration */
	OIDCEnabled  bool     `env:"OIDC_ENABLED" envDefault:"false"`
	OIDCIssuer   string   `env:"OIDC_ISSUER" envDefault:""`
	OIDCAudience string   `env:"OIDC_AUDIENCE" envDefault:"openarity"`
	DevToken     string   `env:"DEV_TOKEN" envDefault:""`
	SuperAdmins  []string `env:"SUPER_ADMINS" envDefault:""`

	/* Object Storage Configuration */
	ObjectsEndpoint  string `env:"OBJECTS_ENDPOINT" envDefault:""`
	ObjectsRegion    string `env:"OBJECTS_REGION" envDefault:"us-east-1"`
	ObjectsBucket    string `env:"OBJECTS_BUCKET" envDefault:"openarity"`
	ObjectsAccessKey string `env:"OBJECTS_ACCESS_KEY" envDefault:""`
	ObjectsSecretKey string `env:"OBJECTS_SECRET_KEY" envDefault:""`
}

func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{Environment:%s LogLevel:%s APIBind:%s WebhookBind:%s "+
			"PostgresDSN:%s FalkorDBURL:%s RedisURL:%s SecretsAddr:%s "+
			"ObjectsEndpoint:%s ObjectsBucket:%s OmniRouteURL:%s}",
		c.Environment,
		c.LogLevel,
		c.APIBind,
		c.WebhookBind,
		redactURL(c.PostgresDSN),
		redactURL(c.FalkorDBURL),
		redactURL(c.RedisURL),
		redactURL(c.SecretsAddr),
		redactURL(c.ObjectsEndpoint),
		c.ObjectsBucket,
		redactURL(c.OmniRouteURL),
	)
}

func load(environ map[string]string) (*Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](env.Options{
		Prefix:          "OPENARITY_",
		RequiredIfNoDef: true,
		Environment:     environ,
	})
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Load() (*Config, error) {
	return load(nil)
}

func redactURL(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return "<invalid-url>"
	}
	return u.Redacted()
}
