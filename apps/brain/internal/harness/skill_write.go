package harness

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

// ReservationHold is how long a skill write holds its object keys before the reaper may take them.
const ReservationHold = time.Hour

type Origin struct {
	Source string
	Ref    *string
	SHA    *string
}

type SkillQueries interface {
	CreateSkill(ctx context.Context, arg db.CreateSkillParams) (db.Skill, error)
	UpdateSkill(ctx context.Context, arg db.UpdateSkillParams) (db.Skill, error)
	ClearSkillFiles(ctx context.Context, skillID uuid.UUID) error
	InsertSkillFiles(ctx context.Context, arg []db.InsertSkillFilesParams) (int64, error)
}

type SkillObjects interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
}

// Create writes a skill whose files Reserve has already stored under keys.
func Create(ctx context.Context, q SkillQueries, teamID uuid.UUID, dir skill.Directory, o Origin, keys []string) (db.Skill, error) {
	m := dir.Manifest
	row, err := q.CreateSkill(ctx, db.CreateSkillParams{
		TeamID: teamID, Name: m.Name, Description: m.Description,
		License: m.License, Compatibility: m.Compatibility, Metadata: Metadata(m),
		AllowedTools: m.AllowedTools, Body: m.Body,
		Source: o.Source, SourceRef: o.Ref, SourceSha: o.SHA,
	})
	if err != nil {
		return db.Skill{}, err
	}
	return row, insertFiles(ctx, q, row, dir.Files, keys)
}

// Replace rewrites a skill and its files, whose bytes Reserve has stored.
func Replace(ctx context.Context, q SkillQueries, skillID uuid.UUID, dir skill.Directory, o Origin, keys []string) (db.Skill, error) {
	m := dir.Manifest
	row, err := q.UpdateSkill(ctx, db.UpdateSkillParams{
		ID: skillID, Name: m.Name, Description: m.Description,
		License: m.License, Compatibility: m.Compatibility, Metadata: Metadata(m),
		AllowedTools: m.AllowedTools, Body: m.Body,
		Source: o.Source, SourceRef: o.Ref, SourceSha: o.SHA,
	})
	if err != nil {
		return db.Skill{}, err
	}
	if err := q.ClearSkillFiles(ctx, skillID); err != nil {
		return db.Skill{}, err
	}
	return row, insertFiles(ctx, q, row, dir.Files, keys)
}

type Reserver interface {
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
}

// Reserve stores files under new keys held for an hour first, so bytes whose
// rows never commit are the reaper's to remove.
func Reserve(ctx context.Context, s Reserver, o SkillObjects, teamID uuid.UUID, files []skill.File) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}

	keys := make([]string, len(files))
	for i := range files {
		keys[i] = objects.TeamPrefix(teamID) + "objects/" + uuid.New().String()
	}

	if err := s.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: keys, TeamID: teamID, HoldSeconds: ReservationHold.Seconds(),
	}); err != nil {
		return nil, fmt.Errorf("reserve objects: %w", err)
	}

	for i, f := range files {
		if err := o.Put(ctx, teamID, keys[i], f.Data); err != nil {
			return nil, fmt.Errorf("store %s: %w", f.Path, err)
		}
	}
	return keys, nil
}

func insertFiles(ctx context.Context, q SkillQueries, row db.Skill, files []skill.File, keys []string) error {
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

func Metadata(m skill.Manifest) []byte {
	b, _ := json.Marshal(m.Metadata)
	return b
}
