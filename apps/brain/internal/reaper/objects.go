package reaper

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type ObjectStore interface {
	Delete(ctx context.Context, teamID uuid.UUID, key string) error
}

type ObjectRows interface {
	ClaimDeletedObjects(ctx context.Context, arg db.ClaimDeletedObjectsParams) ([]db.DeletedObject, error)
	CountAttachmentsByObjectKey(ctx context.Context, objectKey string) (int64, error)
	ForgetDeletedObject(ctx context.Context, objectKey string) error
	DeletedObjectBacklog(ctx context.Context) ([]db.DeletedObjectBacklogRow, error)
}

type objectEffect struct {
	rows    ObjectRows
	objects ObjectStore
}

func Objects(rows ObjectRows, store ObjectStore) Effect {
	return objectEffect{rows: rows, objects: store}
}

func (objectEffect) Name() string { return "objects" }

func (e objectEffect) Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	rows, err := e.rows.ClaimDeletedObjects(ctx, db.ClaimDeletedObjectsParams{
		RetryBefore: &retryBefore,
		BatchSize:   batch,
	})
	if err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{
			Ref: row.ObjectKey, TeamID: row.TeamID, Attempts: row.Attempts,
		})
	}
	return items, nil
}

func (e objectEffect) Do(ctx context.Context, item Item) (Outcome, error) {
	count, err := e.rows.CountAttachmentsByObjectKey(ctx, item.Ref)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		return Superseded, nil
	}

	if err := e.objects.Delete(ctx, item.TeamID, item.Ref); err != nil {
		return 0, err
	}
	return Applied, nil
}

func (e objectEffect) Forget(ctx context.Context, item Item) error {
	return e.rows.ForgetDeletedObject(ctx, item.Ref)
}

func (e objectEffect) Backlog(ctx context.Context) (int64, time.Time, error) {
	rows, err := e.rows.DeletedObjectBacklog(ctx)
	if err != nil || len(rows) == 0 {
		return 0, time.Time{}, err
	}
	return rows[0].Outstanding, rows[0].Oldest, nil
}
