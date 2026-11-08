-- A message from an approved sender starts a hub run, and the run's answer goes
-- back to the conversation it came from.
--
-- runs.message_id is the message that started a run. It is unique (in the next
-- migration, built concurrently), which is what makes a webhook the provider
-- retries start one run and not two. A run started for steers that arrived
-- after a run's last model request answers several messages and has none.
--
-- runs.reply_to_channel marks a run whose answer is sent back. It is separate
-- from message_id for that reason, and because a run someone starts from the
-- dashboard in the same session must not post into the conversation.
--
-- replies is an outbox: the reply is written in the transaction that finishes
-- the run, and sent afterwards, in order per session, with retries. A finished
-- run with no reply row, or a reply sent twice, are the two failures it exists
-- to rule out.

-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE runs
    ADD COLUMN message_id uuid,
    ADD COLUMN reply_to_channel boolean NOT NULL DEFAULT false;

-- NOT VALID, then VALIDATE: the check of existing rows runs under a lock that
-- lets writes through, rather than the one ADD FOREIGN KEY takes on both
-- tables. Every existing row is null, so there is nothing to find.
ALTER TABLE runs
    ADD CONSTRAINT runs_message_fkey
        FOREIGN KEY (message_id) REFERENCES messages (id) ON DELETE SET NULL NOT VALID;
ALTER TABLE runs VALIDATE CONSTRAINT runs_message_fkey;

-- Only a root run answers a conversation; a delegate's answer goes to its
-- parent, never to a person.
ALTER TABLE runs
    ADD CONSTRAINT runs_reply_from_a_root CHECK (NOT reply_to_channel OR id = root_run_id);

-- Where replies go, for a provider that posts them to a URL (custom). Null is a
-- channel that does not reply. The scheme is checked here as a backstop; the
-- API is what refuses a private address.
ALTER TABLE channels
    ADD COLUMN reply_url text,
    ADD CONSTRAINT channels_reply_url_http CHECK (
        reply_url IS NULL OR (reply_url ~ '^https?://' AND length(reply_url) <= 2048));

CREATE TABLE replies (
    id              bigserial PRIMARY KEY,
    team_id         uuid NOT NULL,
    run_id          uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    session_id      uuid NOT NULL,
    channel_id      uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    in_reply_to     uuid REFERENCES messages (id) ON DELETE SET NULL,
    kind            text NOT NULL,
    text            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending',
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,

    CONSTRAINT replies_one_per_run UNIQUE (run_id),
    CONSTRAINT replies_session_in_team
        FOREIGN KEY (session_id, team_id) REFERENCES sessions (id, team_id) ON DELETE CASCADE,
    CONSTRAINT replies_kind_known CHECK (kind IN ('answer', 'apology')),
    CONSTRAINT replies_status_known CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT replies_text_present CHECK (text <> ''),
    CONSTRAINT replies_attempts_nonneg CHECK (attempts >= 0),
    CONSTRAINT replies_sent_when_sent CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
    CONSTRAINT replies_error_when_failed CHECK (status <> 'failed' OR last_error IS NOT NULL)
);

-- The sender's next pick: the oldest pending reply of each session that is due.
CREATE INDEX replies_due ON replies (next_attempt_at) WHERE status = 'pending';
CREATE INDEX replies_session_order ON replies (session_id, id) WHERE status = 'pending';

-- +goose Down
SET lock_timeout = '3s';

DROP TABLE replies;

ALTER TABLE channels
    DROP CONSTRAINT channels_reply_url_http,
    DROP COLUMN reply_url;

ALTER TABLE runs
    DROP CONSTRAINT runs_reply_from_a_root,
    DROP CONSTRAINT runs_message_fkey,
    DROP COLUMN reply_to_channel,
    DROP COLUMN message_id;
