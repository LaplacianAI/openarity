package store

import (
	"context"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func emailOf(p *auth.Principal) *string {
	if p.Email == "" {
		return nil
	}
	return &p.Email
}

func (s *Store) Resolve(ctx context.Context, p *auth.Principal) (*auth.User, error) {
	var user *auth.User

	err := s.InTx(ctx, func(tx *db.Queries) error {
		row, err := tx.UpsertUser(ctx, db.UpsertUserParams{
			Issuer:  p.Issuer,
			Subject: p.Subject,
			Email:   emailOf(p),
		})
		if err != nil {
			return fmt.Errorf("upsert user: %w", err)
		}

		rows, err := tx.ListUserTeams(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}

		teams := make([]auth.Membership, len(rows))
		for i, r := range rows {
			teams[i] = auth.Membership{TeamID: r.ID, Name: r.Name, Role: r.Role}
		}

		user = &auth.User{
			ID:      row.ID,
			Issuer:  row.Issuer,
			Subject: row.Subject,
			Email:   row.Email,
			Teams:   teams,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Store) ActionsFor(ctx context.Context, role string) ([]string, error) {
	return s.ListRolePermissions(ctx, role)
}
