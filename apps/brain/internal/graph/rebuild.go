package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const lockRetry = time.Second

func (p *Projector) Rebuild(ctx context.Context, team uuid.UUID) (int, error) {
	for {
		var projected int
		held, err := p.store.WithAdvisoryLock(ctx, p.lockID, func(ctx context.Context) error {
			n, err := p.rebuild(ctx, team)
			projected = n
			return err
		})
		if err != nil || held {
			return projected, err
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(lockRetry):
		}
	}
}

func (p *Projector) rebuild(ctx context.Context, team uuid.UUID) (int, error) {
	if _, err := p.store.GetTeam(ctx, team); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("graph: there is no team %s", team)
		}
		return 0, err
	}

	rows, err := p.store.ListGraphEntities(ctx, team)
	if err != nil {
		return 0, err
	}
	slices.SortStableFunc(rows, func(a, b db.ListGraphEntitiesRow) int {
		return slices.Index(projectionOrder, entity(a.Entity)) - slices.Index(projectionOrder, entity(b.Entity))
	})

	if err := p.graph.DropTeam(ctx, team); err != nil {
		return 0, err
	}
	if err := p.graph.EnsureIndexes(ctx, team); err != nil {
		return 0, err
	}
	if err := p.graph.PutNode(ctx, team, Node{Ref: Ref{Label: LabelTeam, ID: team}}); err != nil {
		return 0, err
	}

	projected := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return projected, err
		}
		if err := p.project(ctx, team, entity(row.Entity), row.ID); err != nil {
			p.logger.ErrorContext(ctx, "graph: a row was not rebuilt",
				"team_id", team, "entity", row.Entity, "entity_id", row.ID, "error", err)
			continue
		}
		projected++
	}

	if projected != len(rows) {
		return projected, fmt.Errorf("graph: %d of the %d things team %s holds were not rebuilt",
			len(rows)-projected, len(rows), team)
	}
	return projected, nil
}
