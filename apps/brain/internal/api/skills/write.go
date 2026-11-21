package skills

import (
	"context"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var uploaded = harness.Origin{Source: "upload"}

type writeStore interface {
	harness.Reserver
	InTx(ctx context.Context, fn func(harness.SkillQueries) error) error
}

type writer struct {
	store   writeStore
	objects harness.SkillObjects
}

func (w writer) create(ctx context.Context, teamID uuid.UUID, dir skill.Directory, o harness.Origin) (db.Skill, error) {
	keys, err := harness.Reserve(ctx, w.store, w.objects, teamID, dir.Files)
	if err != nil {
		return db.Skill{}, err
	}

	var row db.Skill
	err = w.store.InTx(ctx, func(q harness.SkillQueries) error {
		var err error
		row, err = harness.Create(ctx, q, teamID, dir, o, keys)
		return err
	})
	return row, err
}

func (w writer) replace(ctx context.Context, skillID, teamID uuid.UUID, dir skill.Directory, o harness.Origin) (db.Skill, error) {
	keys, err := harness.Reserve(ctx, w.store, w.objects, teamID, dir.Files)
	if err != nil {
		return db.Skill{}, err
	}

	var row db.Skill
	err = w.store.InTx(ctx, func(q harness.SkillQueries) error {
		var err error
		row, err = harness.Replace(ctx, q, skillID, dir, o, keys)
		return err
	})
	return row, err
}
