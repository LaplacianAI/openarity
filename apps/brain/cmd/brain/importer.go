package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

func newImporter(ctx context.Context, cfg *config.Config, secretStore secrets.Store) (hub.Hub, error) {
	h := hub.Hub{
		GitHub: hub.GitHub{
			Client: hub.NewClient(hub.Policy{Hosts: hub.GitHubHosts, Allow: hub.Public}),
			API:    hub.GitHubAPI,
		},
		Zips: hub.Zips{
			Client: hub.NewClient(hub.Policy{Hosts: cfg.SkillImportHosts, Allow: hub.Public}),
		},
	}
	if cfg.SkillImportGitHubTokenRef == "" {
		return h, nil
	}

	path, key, _ := strings.Cut(cfg.SkillImportGitHubTokenRef, "#")
	h.Token = func(ctx context.Context) (string, error) {
		return secretStore.Get(ctx, path, key)
	}

	ctx, cancel := context.WithTimeout(ctx, secretStoreTimeout)
	defer cancel()
	if _, err := h.Token(ctx); err != nil {
		return hub.Hub{}, fmt.Errorf("SKILL_IMPORT_GITHUB_TOKEN_REF names %s, which cannot be read: %w",
			cfg.SkillImportGitHubTokenRef, err)
	}
	return h, nil
}
