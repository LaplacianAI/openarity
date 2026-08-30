package reaper

import (
	"context"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type SecretStore interface {
	Delete(ctx context.Context, path string) error
}

type SecretRows interface {
	ClaimDeletedSecrets(ctx context.Context, arg db.ClaimDeletedSecretsParams) ([]db.DeletedSecret, error)
	ForgetDeletedSecret(ctx context.Context, path string) error
	DeletedSecretBacklog(ctx context.Context) ([]db.DeletedSecretBacklogRow, error)
}

type secretEffect struct {
	rows    SecretRows
	secrets SecretStore
}

func Secrets(rows SecretRows, store SecretStore) Effect {
	return secretEffect{rows: rows, secrets: store}
}

func (secretEffect) Name() string { return "secrets" }

func (e secretEffect) Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	rows, err := e.rows.ClaimDeletedSecrets(ctx, db.ClaimDeletedSecretsParams{
		RetryBefore: &retryBefore,
		BatchSize:   batch,
	})
	if err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{
			Ref: row.Path, TeamID: row.TeamID, Attempts: row.Attempts,
		})
	}
	return items, nil
}

func (e secretEffect) Do(ctx context.Context, item Item) (Outcome, error) {
	if err := e.secrets.Delete(ctx, item.Ref); err != nil {
		return 0, err
	}
	return Applied, nil
}

func (e secretEffect) Forget(ctx context.Context, item Item) error {
	return e.rows.ForgetDeletedSecret(ctx, item.Ref)
}

func (e secretEffect) Backlog(ctx context.Context) (int64, time.Time, error) {
	rows, err := e.rows.DeletedSecretBacklog(ctx)
	if err != nil || len(rows) == 0 {
		return 0, time.Time{}, err
	}
	return rows[0].Outstanding, rows[0].Oldest, nil
}
