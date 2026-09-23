-- One COPY for every file of a skill: up to two hundred rows is one round
-- trip, not two hundred, and all of them land inside the caller's transaction.
-- name: InsertSkillFiles :copyfrom
INSERT INTO skill_files (skill_id, team_id, path, size, sha256, media_type, object_key)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListSkillFiles :many
SELECT * FROM skill_files WHERE skill_id = $1 ORDER BY path;

-- name: GetSkillFile :one
SELECT * FROM skill_files WHERE skill_id = $1 AND path = $2;

-- A replace clears the old directory before writing the new one. The delete
-- trigger tombstones every old object, so the bytes of files a new upload
-- dropped are swept rather than left behind.
-- name: ClearSkillFiles :exec
DELETE FROM skill_files WHERE skill_id = $1;
