package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// WithE2BTemplateLock runs fn holding the lock for one team's template under
// one sandbox key, so a save and a room's first start that both find no build
// ask E2B for one between them. It waits, as the room lock does; asking E2B
// for a build takes seconds.
func (s *Store) WithE2BTemplateLock(
	ctx context.Context, team, key uuid.UUID, name string, fn func(context.Context) error,
) error {
	args := db.LockE2BTemplateParams{TeamID: team.String(), SandboxSecretID: key.String(), Name: name}
	return s.withSessionLock(ctx, "template",
		func(q *db.Queries) error { return q.LockE2BTemplate(ctx, args) },
		func(ctx context.Context, q *db.Queries) (bool, error) {
			return q.UnlockE2BTemplate(ctx, db.UnlockE2BTemplateParams(args))
		}, fn)
}
