package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const reservationHold = time.Hour

type Origin struct {
	Source string
	Ref    *string
	SHA    *string
}

var uploaded = Origin{Source: "upload"}

type Queries interface {
	CreateSkill(ctx context.Context, arg db.CreateSkillParams) (db.Skill, error)
	UpdateSkill(ctx context.Context, arg db.UpdateSkillParams) (db.Skill, error)
	ClearSkillFiles(ctx context.Context, skillID uuid.UUID) error
	InsertSkillFiles(ctx context.Context, arg []db.InsertSkillFilesParams) (int64, error)
}

type Objects interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
}

type writeStore interface {
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
	InTx(ctx context.Context, fn func(Queries) error) error
}

type writer struct {
	store   writeStore
	objects Objects
}

func (w writer) create(ctx context.Context, teamID uuid.UUID, dir skill.Directory, o Origin) (db.Skill, error) {
	keys, err := w.upload(ctx, teamID, dir.Files)
	if err != nil {
		return db.Skill{}, err
	}

	m := dir.Manifest
	var row db.Skill
	err = w.store.InTx(ctx, func(q Queries) error {
		var err error
		row, err = q.CreateSkill(ctx, db.CreateSkillParams{
			TeamID: teamID, Name: m.Name, Description: m.Description,
			License: m.License, Compatibility: m.Compatibility, Metadata: metadata(m),
			AllowedTools: m.AllowedTools, Body: m.Body,
			Source: o.Source, SourceRef: o.Ref, SourceSha: o.SHA,
		})
		if err != nil {
			return err
		}
		return insertFiles(ctx, q, row, dir.Files, keys)
	})

	return row, err
}

func (w writer) replace(ctx context.Context, skillID, teamID uuid.UUID, dir skill.Directory, o Origin) (db.Skill, error) {
	keys, err := w.upload(ctx, teamID, dir.Files)
	if err != nil {
		return db.Skill{}, err
	}

	m := dir.Manifest
	var row db.Skill
	err = w.store.InTx(ctx, func(q Queries) error {
		var err error
		row, err = q.UpdateSkill(ctx, db.UpdateSkillParams{
			ID: skillID, Name: m.Name, Description: m.Description,
			License: m.License, Compatibility: m.Compatibility, Metadata: metadata(m),
			AllowedTools: m.AllowedTools, Body: m.Body,
			Source: o.Source, SourceRef: o.Ref, SourceSha: o.SHA,
		})
		if err != nil {
			return err
		}
		if err := q.ClearSkillFiles(ctx, skillID); err != nil {
			return err
		}
		return insertFiles(ctx, q, row, dir.Files, keys)
	})

	return row, err
}

func (w writer) upload(ctx context.Context, teamID uuid.UUID, files []skill.File) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}

	keys := make([]string, len(files))
	for i := range files {
		keys[i] = objects.TeamPrefix(teamID) + "objects/" + uuid.New().String()
	}

	if err := w.store.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: keys, TeamID: teamID, ClaimableAfter: time.Now().Add(reservationHold),
	}); err != nil {
		return nil, fmt.Errorf("reserve objects: %w", err)
	}

	for i, f := range files {
		if err := w.objects.Put(ctx, teamID, keys[i], f.Data); err != nil {
			return nil, fmt.Errorf("store %s: %w", f.Path, err)
		}
	}
	return keys, nil
}

func insertFiles(ctx context.Context, q Queries, row db.Skill, files []skill.File, keys []string) error {
	if len(files) == 0 {
		return nil
	}

	rows := make([]db.InsertSkillFilesParams, len(files))
	for i, f := range files {
		rows[i] = db.InsertSkillFilesParams{
			SkillID: row.ID, TeamID: row.TeamID, Path: f.Path,
			Size: int64(len(f.Data)), Sha256: f.SHA256[:], MediaType: f.MediaType,
			ObjectKey: keys[i],
		}
	}
	_, err := q.InsertSkillFiles(ctx, rows)

	return err
}

func metadata(m skill.Manifest) []byte {
	b, _ := json.Marshal(m.Metadata)
	return b
}
