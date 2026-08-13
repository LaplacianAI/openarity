package config

import (
	"fmt"
	"strings"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
)

const (
	DefaultTheme  = theme.Auto
	DefaultOutput = output.Default
)

type Env func(string) string

type Setting struct {
	Name   string
	Value  string
	Source string
}

type Settings struct {
	Context Setting
	Server  Setting
	Theme   Setting
	Token   Setting
	Output  Setting
}

type candidate struct {
	value  string
	source string
}

func pick(name, fallback, fallbackSource string, candidates ...candidate) Setting {
	for _, c := range candidates {
		if value := strings.TrimSpace(c.value); value != "" {
			return Setting{Name: name, Value: value, Source: c.source}
		}
	}
	return Setting{Name: name, Value: fallback, Source: fallbackSource}
}

func token(flag string, env Env, active Context, path string) Setting {
	found := pick("token", "", "",
		candidate{flag, "--token"},
		candidate{env("OPENARITY_TOKEN"), "OPENARITY_TOKEN"},
		candidate{active.Token, path},
	)
	if found.Value == "" {
		return Setting{Name: "token", Value: "not set", Source: ""}
	}
	return Setting{
		Name:   "token",
		Value:  fmt.Sprintf("set (%d characters)", len(found.Value)),
		Source: found.Source,
	}
}

func Resolve(serverFlag, tokenFlag, outputFlag string, env Env, saved Config, path string) Settings {
	active := saved.Active()

	return Settings{
		Context: pick("context", "none", "default",
			candidate{saved.ActiveName(), path},
		),
		Server: pick("server", DefaultServer, "default",
			candidate{serverFlag, "--server"},
			candidate{env("OPENARITY_SERVER"), "OPENARITY_SERVER"},
			candidate{active.Server, path},
		),
		Theme: pick("theme", string(DefaultTheme), "default",
			candidate{env("OPENARITY_THEME"), "OPENARITY_THEME"},
			candidate{saved.Theme, path},
		),
		Output: pick("output", string(DefaultOutput), "default",
			candidate{outputFlag, "--output"},
			candidate{env("OPENARITY_OUTPUT"), "OPENARITY_OUTPUT"},
			candidate{saved.Output, path},
		),
		Token: token(tokenFlag, env, active, path),
	}
}
