package store

import (
	"context"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Chosen once and never derived, because an advisory lock is only a lock if
// everybody agrees on the number. Distinct from rbacLockID so that loading the
// permission tables and a first login cannot block one another.
const bootstrapLockID int64 = 1902710444

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

		if s.firstUserBootstrap && !row.IsSuperAdmin {
			row, err = claimUnownedInstall(ctx, tx, row)
			if err != nil {
				return err
			}
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
			ID:         row.ID,
			Issuer:     row.Issuer,
			Subject:    row.Subject,
			Email:      row.Email,
			Teams:      teams,
			SuperAdmin: row.IsSuperAdmin,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return user, nil
}

// claimUnownedInstall grants super admin to row when the install has none, and
// returns row unchanged when it already does.
//
// The guard is "no super admin exists" rather than "no users exist". Empty-table
// is the tempting version and it bricks the install: an ordinary member logging
// in before the operator does would close the window forever, leaving a database
// nobody can administer and no way to reopen it short of an UPDATE by hand.
// Asking about admins instead means an early arrival by somebody else costs
// nothing — the next login still finds the install unowned.
//
// The consequence to know about: if the only super admin is later erased, the
// install becomes unowned again and the next login claims it. That is the
// correct behaviour for the single-user machine this flag exists for, and it is
// why it is off unless somebody asked for it.
func claimUnownedInstall(ctx context.Context, tx *db.Queries, row db.User) (db.User, error) {
	// Unlocked, and deliberately: this runs on every authenticated request for
	// the life of the install, and after the first promotion the answer is
	// always true. Paying for a lock on that path to protect a window that
	// closes in the first minute would be the wrong trade.
	owned, err := tx.AnySuperAdmin(ctx)
	if err != nil {
		return row, fmt.Errorf("check for a super admin: %w", err)
	}
	if owned {
		return row, nil
	}

	if err := tx.LockFirstUserBootstrap(ctx, bootstrapLockID); err != nil {
		return row, fmt.Errorf("lock the bootstrap: %w", err)
	}

	// Again, now that nobody else can be between the check and the write. The
	// answer can differ from the one above: InTx begins with pgx's default
	// isolation, READ COMMITTED, so this statement takes a new snapshot and
	// sees the promotion a transaction ahead of us has just committed. Under
	// REPEATABLE READ it would not, and this re-check would be theatre.
	owned, err = tx.AnySuperAdmin(ctx)
	if err != nil {
		return row, fmt.Errorf("re-check for a super admin: %w", err)
	}
	if owned {
		return row, nil
	}

	promoted, err := tx.PromoteToSuperAdmin(ctx, row.ID)
	if err != nil {
		return row, fmt.Errorf("promote %s to super admin: %w", row.Subject, err)
	}
	return promoted, nil
}

func (s *Store) ActionsFor(ctx context.Context, role string) ([]string, error) {
	return s.ListRolePermissions(ctx, role)
}
