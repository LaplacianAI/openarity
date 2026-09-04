package main

import (
	"context"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

type superAdminChecker interface {
	AnySuperAdmin(ctx context.Context) (bool, error)
}

// firstUserBootstrap decides whether Resolve may hand this install to whoever
// logs in next.
//
// Both halves matter. The operator has to ask for it, and SUPER_ADMINS has to
// be empty — a deployment that names its admins has already answered the
// question, and letting the flag override that would mean an environment
// variable set for a container image could quietly grant the next visitor
// everything. Composed here rather than in the store because the store cannot
// see configuration and should not learn to.
func firstUserBootstrap(cfg *config.Config) bool {
	return cfg.BootstrapFirstUser && len(cfg.SuperAdmins) == 0
}

// warnIfInstallIsUnowned says so while the window is open.
//
// The window closes on its own, which is the point of the design and also the
// reason it is easy to forget it was ever open. A line at startup is what makes
// "the next person to log in becomes an administrator" a thing somebody
// noticed rather than a thing somebody discovers.
func warnIfInstallIsUnowned(
	ctx context.Context, cfg *config.Config, logger *slog.Logger, s superAdminChecker,
) {
	if !firstUserBootstrap(cfg) {
		return
	}

	owned, err := s.AnySuperAdmin(ctx)
	if err != nil {
		logger.Warn("could not check whether this install has a super admin", "error", err)
		return
	}
	if owned {
		return
	}

	logger.Warn("BOOTSTRAP_FIRST_USER is set and this install has no super admin: "+
		"the next successful login will be granted one, and the grant is permanent",
		"api_bind", cfg.APIBind,
	)
}
