-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A schedule: a time rule, an objective, and a conversation of its own to
-- answer in. A watcher: a source checked over time, and a run only when it
-- changed. Both are made by a person, or proposed by an agent and confirmed
-- by one, and run as the person who made them. Neither is ever deleted: a
-- delete marks it so, and its conversation and runs stay for review.
CREATE TABLE schedules (
    id            uuid PRIMARY KEY,
    team_id       uuid NOT NULL REFERENCES teams ON DELETE CASCADE,
    -- Null once the maker's user is gone: the schedule pauses, and an admin
    -- may take it over.
    created_by    uuid REFERENCES users ON DELETE SET NULL,
    -- Null is the team's hub.
    agent_id      uuid REFERENCES agents ON DELETE SET NULL,
    name          text NOT NULL,
    objective     text NOT NULL,
    kind          text NOT NULL,
    -- at: an RFC 3339 instant; every: whole seconds; cron: five fields.
    rule          text NOT NULL,
    -- every: the first fire, which the others count from; otherwise when it was made.
    anchor        timestamptz NOT NULL,
    timezone      text NOT NULL,
    status        text NOT NULL,
    paused_reason text,
    deleted_at    timestamptz,
    next_fire_at  timestamptz,
    last_fired_at timestamptz,
    -- The local wall time of the last fire, 'YYYY-MM-DDTHH:MM': the hour the
    -- clocks go back happens twice, and fires once.
    last_local    text,
    consecutive_failures int NOT NULL DEFAULT 0,
    -- What an agent proposed it in: the run, and the conversation, whose next
    -- message from the maker may confirm it.
    proposed_run  uuid,
    proposed_in   uuid REFERENCES sessions ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, team_id),
    CONSTRAINT schedules_name_bounded CHECK (length(name) BETWEEN 1 AND 80),
    CONSTRAINT schedules_objective_bounded CHECK (length(objective) BETWEEN 1 AND 4000),
    CONSTRAINT schedules_kind_known CHECK (kind IN ('at', 'every', 'cron')),
    CONSTRAINT schedules_rule_bounded CHECK (length(rule) BETWEEN 1 AND 200),
    CONSTRAINT schedules_timezone_bounded CHECK (length(timezone) BETWEEN 1 AND 64),
    CONSTRAINT schedules_status_known CHECK (status IN ('proposed', 'active', 'paused', 'done', 'deleted')),
    CONSTRAINT schedules_paused_reason_known CHECK (paused_reason IN
        ('by_person', 'failing', 'maker_cannot_run', 'maker_gone', 'target_gone')),
    CONSTRAINT schedules_paused_says_why CHECK ((status = 'paused') = (paused_reason IS NOT NULL)),
    CONSTRAINT schedules_active_has_next CHECK ((status = 'active') = (next_fire_at IS NOT NULL)),
    CONSTRAINT schedules_deleted_at CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
    CONSTRAINT schedules_proposed_names_where CHECK (status <> 'proposed' OR proposed_in IS NOT NULL)
);
-- What the tick reads: only what is due, so its cost follows that rather
-- than how many schedules there are.
CREATE INDEX schedules_due ON schedules (next_fire_at) WHERE status = 'active';
CREATE INDEX schedules_proposed ON schedules (created_at) WHERE status = 'proposed';
CREATE INDEX schedules_by_maker ON schedules (team_id, created_by) WHERE status <> 'deleted';
-- A person picks one of theirs by name, so a name is theirs once; a deleted
-- one frees it.
CREATE UNIQUE INDEX schedules_name_key ON schedules (team_id, created_by, lower(name))
    WHERE status <> 'deleted';

-- Each time a schedule was due, once: the key is what stops a second worker,
-- a retry or a restart firing the same time twice.
CREATE TABLE schedule_fires (
    schedule_id   uuid NOT NULL REFERENCES schedules ON DELETE CASCADE,
    team_id       uuid NOT NULL REFERENCES teams ON DELETE CASCADE,
    scheduled_for timestamptz NOT NULL,
    fired_at      timestamptz NOT NULL DEFAULT now(),
    outcome       text NOT NULL,
    run_id        uuid UNIQUE REFERENCES runs ON DELETE SET NULL,
    -- When the maker opened what it said.
    seen_at       timestamptz,
    PRIMARY KEY (schedule_id, scheduled_for),
    CONSTRAINT schedule_fires_outcome_known CHECK (outcome IN (
        'started', 'late', 'catch_up', 'missed', 'skipped_busy', 'skipped_cap',
        'skipped_maker', 'silent', 'answered', 'failed'))
);
-- Fires that started a run, by team and time: the hourly cap counts these.
CREATE INDEX schedule_fires_team_hour ON schedule_fires (team_id, fired_at) WHERE run_id IS NOT NULL;
CREATE INDEX schedule_fires_unseen ON schedule_fires (schedule_id) WHERE seen_at IS NULL;

-- A URL a watcher polls, fetched once for every watcher of it.
CREATE TABLE watch_targets (
    id             uuid PRIMARY KEY,
    kind           text NOT NULL,
    url            text NOT NULL,
    host           text NOT NULL,
    etag           text,
    last_modified  text,
    -- Also the claim's lease: a check moves it on before it fetches.
    next_check_at  timestamptz NOT NULL,
    consecutive_errors int NOT NULL DEFAULT 0,
    failing_since  timestamptz,
    UNIQUE (kind, url),
    CONSTRAINT watch_targets_kind_known CHECK (kind IN ('web_page', 'feed')),
    CONSTRAINT watch_targets_url_bounded CHECK (length(url) BETWEEN 1 AND 2048),
    CONSTRAINT watch_targets_host_bounded CHECK (length(host) BETWEEN 1 AND 253)
);
CREATE INDEX watch_targets_due ON watch_targets (next_check_at);

-- One host at a time, and a host that asked us to wait, waited for.
CREATE TABLE watch_hosts (
    host          text PRIMARY KEY,
    busy_until    timestamptz NOT NULL DEFAULT '-infinity',
    backoff_until timestamptz NOT NULL DEFAULT '-infinity'
);

CREATE TABLE watchers (
    id            uuid PRIMARY KEY,
    team_id       uuid NOT NULL REFERENCES teams ON DELETE CASCADE,
    created_by    uuid REFERENCES users ON DELETE SET NULL,
    agent_id      uuid REFERENCES agents ON DELETE SET NULL,
    name          text NOT NULL,
    objective     text NOT NULL,
    source        text NOT NULL,
    target_id     uuid REFERENCES watch_targets ON DELETE RESTRICT,
    selector      text,
    -- A webhook's place in its URL: unguessable, and never its secret.
    hook_id       text UNIQUE,
    every_seconds int,
    cooldown_seconds int NOT NULL DEFAULT 3600,
    -- What the last check saw: a hash and text, a feed's seen ids, a held change.
    state         jsonb NOT NULL DEFAULT '{}',
    status        text NOT NULL,
    paused_reason text,
    deleted_at    timestamptz,
    next_check_at timestamptz,
    last_run_at   timestamptz,
    consecutive_failures int NOT NULL DEFAULT 0,
    proposed_run  uuid,
    proposed_in   uuid REFERENCES sessions ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, team_id),
    CONSTRAINT watchers_name_bounded CHECK (length(name) BETWEEN 1 AND 80),
    CONSTRAINT watchers_objective_bounded CHECK (length(objective) BETWEEN 1 AND 4000),
    CONSTRAINT watchers_source_known CHECK (source IN ('web_page', 'feed', 'webhook')),
    CONSTRAINT watchers_selector_bounded CHECK (length(selector) BETWEEN 1 AND 512),
    CONSTRAINT watchers_hook_bounded CHECK (length(hook_id) BETWEEN 16 AND 64),
    CONSTRAINT watchers_every_positive CHECK (every_seconds > 0),
    CONSTRAINT watchers_cooldown_bounded CHECK (cooldown_seconds BETWEEN 0 AND 604800),
    CONSTRAINT watchers_status_known CHECK (status IN ('proposed', 'waiting_secret', 'active', 'paused', 'deleted')),
    CONSTRAINT watchers_paused_reason_known CHECK (paused_reason IN
        ('by_person', 'failing', 'maker_cannot_run', 'maker_gone', 'target_gone')),
    CONSTRAINT watchers_paused_says_why CHECK ((status = 'paused') = (paused_reason IS NOT NULL)),
    CONSTRAINT watchers_deleted_at CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
    CONSTRAINT watchers_proposed_names_where CHECK (status <> 'proposed' OR proposed_in IS NOT NULL),
    -- A webhook has a hook and nothing to poll; a poller has a target, an
    -- interval and no hook; only a page has a selector.
    CONSTRAINT watchers_source_shape CHECK (
        (source = 'webhook') = (hook_id IS NOT NULL) AND
        (source = 'webhook') = (target_id IS NULL) AND
        (source = 'webhook') = (every_seconds IS NULL) AND
        (selector IS NULL OR source = 'web_page'))
);
CREATE INDEX watchers_of_target ON watchers (target_id) WHERE status = 'active';
CREATE INDEX watchers_proposed ON watchers (created_at) WHERE status IN ('proposed', 'waiting_secret');
CREATE INDEX watchers_by_maker ON watchers (team_id, created_by) WHERE status <> 'deleted';
CREATE UNIQUE INDEX watchers_name_key ON watchers (team_id, created_by, lower(name))
    WHERE status <> 'deleted';

CREATE TABLE watcher_checks (
    id          bigserial PRIMARY KEY,
    watcher_id  uuid NOT NULL REFERENCES watchers ON DELETE CASCADE,
    team_id     uuid NOT NULL REFERENCES teams ON DELETE CASCADE,
    checked_at  timestamptz NOT NULL DEFAULT now(),
    outcome     text NOT NULL,
    -- An error's reason or a change's size: never a body.
    detail      text,
    run_id      uuid UNIQUE REFERENCES runs ON DELETE SET NULL,
    seen_at     timestamptz,
    CONSTRAINT watcher_checks_outcome_known CHECK (outcome IN (
        'unchanged', 'baseline', 'changed', 'held', 'error', 'delivered', 'duplicate',
        'skipped_cap', 'skipped_maker', 'silent', 'answered', 'failed')),
    CONSTRAINT watcher_checks_detail_bounded CHECK (length(detail) <= 500)
);
CREATE INDEX watcher_checks_recent ON watcher_checks (watcher_id, checked_at DESC);
CREATE INDEX watcher_checks_team_hour ON watcher_checks (team_id, checked_at) WHERE run_id IS NOT NULL;
CREATE INDEX watcher_checks_unseen ON watcher_checks (watcher_id) WHERE seen_at IS NULL;

-- A webhook's deliveries for a day, so one sent twice starts nothing twice;
-- and the bodies of those held for the next allowed run.
CREATE TABLE watcher_deliveries (
    watcher_id  uuid NOT NULL REFERENCES watchers ON DELETE CASCADE,
    key         text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    body        bytea,
    held        boolean NOT NULL DEFAULT false,
    PRIMARY KEY (watcher_id, key),
    CONSTRAINT watcher_deliveries_key_bounded CHECK (length(key) BETWEEN 1 AND 200),
    CONSTRAINT watcher_deliveries_body_bounded CHECK (octet_length(body) <= 262144),
    CONSTRAINT watcher_deliveries_held_has_body CHECK (NOT held OR body IS NOT NULL)
);
CREATE INDEX watcher_deliveries_held ON watcher_deliveries (watcher_id) WHERE held;
CREATE INDEX watcher_deliveries_received ON watcher_deliveries (received_at) WHERE NOT held;

-- A schedule's or a watcher's own conversation. The keys are proven in the
-- next migration, and their unique indexes built without holding writes in
-- the one after.
ALTER TABLE sessions
    ADD COLUMN schedule_id uuid,
    ADD COLUMN watcher_id  uuid,
    ADD CONSTRAINT sessions_schedule_fkey FOREIGN KEY (schedule_id) REFERENCES schedules NOT VALID,
    ADD CONSTRAINT sessions_watcher_fkey FOREIGN KEY (watcher_id) REFERENCES watchers NOT VALID,
    ADD CONSTRAINT sessions_one_trigger CHECK (schedule_id IS NULL OR watcher_id IS NULL) NOT VALID;

-- Where a reply landed, as its channel names it: a Slack post's channel and
-- ts, so a reply in its thread can find what was posted.
ALTER TABLE replies
    ADD COLUMN posted_ref text,
    ADD CONSTRAINT replies_posted_ref_bounded CHECK (length(posted_ref) <= 256) NOT VALID;

-- An agent that may make schedules and watchers from a chat. The hub may
-- whenever its team lets agents.
ALTER TABLE agents ADD COLUMN schedule boolean NOT NULL DEFAULT false;

ALTER TABLE team_settings
    ADD COLUMN agents_may_schedule boolean NOT NULL DEFAULT true,
    ADD COLUMN timezone text NOT NULL DEFAULT 'UTC',
    ADD COLUMN schedule_min_interval_minutes integer NOT NULL DEFAULT 60,
    ADD COLUMN schedules_per_member integer NOT NULL DEFAULT 5,
    ADD COLUMN schedules_per_team integer NOT NULL DEFAULT 20,
    ADD COLUMN fires_per_hour integer NOT NULL DEFAULT 20,
    ADD CONSTRAINT team_settings_timezone_bounded CHECK (length(timezone) BETWEEN 1 AND 64),
    ADD CONSTRAINT team_settings_schedule_min_range CHECK (schedule_min_interval_minutes BETWEEN 5 AND 10080),
    ADD CONSTRAINT team_settings_per_member_range CHECK (schedules_per_member BETWEEN 1 AND 100),
    ADD CONSTRAINT team_settings_per_team_range CHECK (schedules_per_team BETWEEN 1 AND 1000
                                                     AND schedules_per_team >= schedules_per_member),
    ADD CONSTRAINT team_settings_fires_range CHECK (fires_per_hour BETWEEN 1 AND 1000);

-- +goose Down
SET LOCAL lock_timeout = '3s';
ALTER TABLE team_settings
    DROP COLUMN agents_may_schedule,
    DROP COLUMN timezone,
    DROP COLUMN schedule_min_interval_minutes,
    DROP COLUMN schedules_per_member,
    DROP COLUMN schedules_per_team,
    DROP COLUMN fires_per_hour;
ALTER TABLE agents DROP COLUMN schedule;
ALTER TABLE replies DROP COLUMN posted_ref;
ALTER TABLE sessions DROP COLUMN schedule_id, DROP COLUMN watcher_id;
DROP TABLE watcher_deliveries;
DROP TABLE watcher_checks;
DROP TABLE watchers;
DROP TABLE watch_hosts;
DROP TABLE watch_targets;
DROP TABLE schedule_fires;
DROP TABLE schedules;
