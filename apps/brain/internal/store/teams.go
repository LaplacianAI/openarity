package store

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// CreateTeam makes a team and its settings in one transaction: a new team
// asks before its agents write. Teams from before approvals keep off, the
// column's default, until an admin chooses. A taken name is the insert's
// 23505, as it always was.
func (s *Store) CreateTeam(ctx context.Context, name string) (db.Team, error) {
	var team db.Team
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		if team, err = q.CreateTeam(ctx, name); err != nil {
			return err
		}
		return q.StartTeamSettings(ctx, team.ID)
	})
	return team, err
}
