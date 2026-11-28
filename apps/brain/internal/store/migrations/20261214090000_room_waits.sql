-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A run whose workspace call the sandbox refused before anything ran, for
-- want of room (its memory or disk budget spent, its template still being
-- built, its provider not answering), waits for room rather than going on
-- without its workspace. The run parks as it does for a person; the sweep
-- makes the room itself while it waits, and queues the run's root once there
-- is room, or once it has waited long enough. told_at is when the run was
-- told how its wait ended, as an approval's is.
CREATE TABLE room_waits (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id     uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    run_id      uuid NOT NULL,
    root_run_id uuid NOT NULL,
    session_id  uuid NOT NULL,
    -- What the sandbox said, for the model to be told when it gives up.
    reason      text NOT NULL,
    since       timestamptz NOT NULL DEFAULT now(),
    retry_at    timestamptz NOT NULL,
    ended       text,
    ended_at    timestamptz,
    told_at     timestamptz,

    CONSTRAINT room_waits_run_in_team
        FOREIGN KEY (run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT room_waits_root_in_team
        FOREIGN KEY (root_run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT room_waits_ended_known CHECK (
        ended IS NULL OR ended IN ('room', 'gave_up', 'superseded', 'cancelled')),
    CONSTRAINT room_waits_ended_when_ended CHECK ((ended IS NULL) = (ended_at IS NULL)),
    CONSTRAINT room_waits_told_after_ended CHECK (told_at IS NULL OR ended IS NOT NULL)
);
-- What a waiting run waits for, so a parent is told whether its child waits
-- for a person or for room: 'approval' when anything it waits on is a
-- person, else 'sandbox'. Read only while the run waits.
ALTER TABLE runs
    ADD COLUMN waiting_for text,
    ADD CONSTRAINT runs_waiting_for_known CHECK (
        waiting_for IS NULL OR waiting_for IN ('approval', 'sandbox')) NOT VALID;

-- One wait a run at a time: a second refusal in the same turn is the same
-- wait.
CREATE UNIQUE INDEX room_waits_open_key ON room_waits (run_id) WHERE told_at IS NULL;
CREATE INDEX room_waits_due_idx ON room_waits (retry_at) WHERE ended IS NULL;
-- Whole, not partial: a run or team deleted cascades here, ended waits too.
CREATE INDEX room_waits_root_idx ON room_waits (root_run_id);
CREATE INDEX room_waits_run_idx ON room_waits (run_id);
CREATE INDEX room_waits_team_idx ON room_waits (team_id);

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE room_waits;
ALTER TABLE runs DROP COLUMN waiting_for;
