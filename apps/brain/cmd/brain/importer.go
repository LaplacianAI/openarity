package main

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

func newImporter(cfg *config.Config) hub.Hub {
	h := hub.Hub{
		GitHub: hub.GitHub{
			Client: hub.NewClient(hub.Policy{Hosts: hub.GitHubHosts, Allow: hub.Public}),
			API:    hub.GitHubAPI,
		},
		Zips: hub.Zips{
			Client: hub.NewClient(hub.Policy{Hosts: cfg.SkillImportHosts, Allow: hub.Public}),
		},
	}
	if cfg.SkillImportGitHubToken != "" {
		h.Token = func(context.Context) (string, error) { return cfg.SkillImportGitHubToken, nil }
	}
	return h
}
