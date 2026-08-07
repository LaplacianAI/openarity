package config

import (
	"fmt"
	"net/url"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	/* Application Configuration */
	Environment Environment `env:"ENVIRONMENT" envDefault:"development"`
	APIBind     string      `env:"API_BIND" envDefault:"127.0.0.1:21120"`
	WebhookBind string      `env:"WEBHOOK_BIND" envDefault:"127.0.0.1:21121"`

	/* Logging Configuration */
	LogLevel LogLevel `env:"LOG_LEVEL" envDefault:"info"`

	/* Datastore Configuration */
	PostgresDSN string `env:"POSTGRES_DSN" envDefault:"postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable"`
	FalkorDBURL string `env:"FALKOR_DB_URL" envDefault:"redis://127.0.0.1:6380"`
	RedisURL    string `env:"REDIS_URL" envDefault:"redis://127.0.0.1:6379"`

	/* Vault Configuration */
	VaultAddr string `env:"VAULT_ADDR" envDefault:"http://localhost:8200"`

	/* Model Router Configuration */
	OmniRouteURL string `env:"OMNI_ROUTE_URL" envDefault:"http://localhost:20128/v1"`
}

func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{Environment:%s LogLevel:%s APIBind:%s WebhookBind:%s "+
			"PostgresDSN:%s FalkorDBURL:%s RedisURL:%s VaultAddr:%s OmniRouteURL:%s}",
		c.Environment,
		c.LogLevel,
		c.APIBind,
		c.WebhookBind,
		redactURL(c.PostgresDSN),
		redactURL(c.FalkorDBURL),
		redactURL(c.RedisURL),
		redactURL(c.VaultAddr),
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
