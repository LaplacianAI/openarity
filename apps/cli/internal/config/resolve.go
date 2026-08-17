package config

import (
	"fmt"
	"strings"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
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

type Input struct {
	ServerFlag string
	TokenFlag  string
	OutputFlag string

	Env   Env
	Saved Config
	Path  string

	Credential         credential.Credential
	CredentialLocation string
}

func pick(name, fallback, fallbackSource string, candidates ...candidate) Setting {
	for _, c := range candidates {
		if value := strings.TrimSpace(c.value); value != "" {
			return Setting{Name: name, Value: value, Source: c.source}
		}
	}
	return Setting{Name: name, Value: fallback, Source: fallbackSource}
}

func token(input Input) Setting {
	found := pick("token", "", "",
		candidate{input.TokenFlag, "--token"},
		candidate{input.Env("OPENARITY_TOKEN"), "OPENARITY_TOKEN"},
		candidate{input.Credential.Token, input.CredentialLocation},
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

func Resolve(input Input) Settings {
	active := input.Saved.Active()

	return Settings{
		Context: pick("context", "none", "default",
			candidate{input.Saved.ActiveName(), input.Path},
		),
		Server: pick("server", DefaultServer, "default",
			candidate{input.ServerFlag, "--server"},
			candidate{input.Env("OPENARITY_SERVER"), "OPENARITY_SERVER"},
			candidate{active.Server, input.Path},
		),
		Theme: pick("theme", string(DefaultTheme), "default",
			candidate{input.Env("OPENARITY_THEME"), "OPENARITY_THEME"},
			candidate{input.Saved.Theme, input.Path},
		),
		Output: pick("output", string(DefaultOutput), "default",
			candidate{input.OutputFlag, "--output"},
			candidate{input.Env("OPENARITY_OUTPUT"), "OPENARITY_OUTPUT"},
			candidate{input.Saved.Output, input.Path},
		),
		Token: token(input),
	}
}
