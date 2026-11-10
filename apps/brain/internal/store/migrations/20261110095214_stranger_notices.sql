-- A sender nobody has let in is told so, once per wait, and told again when
-- they are let in. A notice is not a reply: a stranger has no run, no session
-- and no stored message, so it has its own outbox, addressed by the provider's
-- own ids. Where to answer is kept on the pending row as those ids only, never
-- what the stranger wrote.

-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE channels ADD COLUMN notify_strangers boolean NOT NULL DEFAULT true;

ALTER TABLE pending_senders
    ADD COLUMN notified_at         timestamptz,
    ADD COLUMN notice_session_ref  text,
    ADD COLUMN notice_session_kind text,
    ADD COLUMN notice_message_ref  text,
    ADD CONSTRAINT pending_senders_notice_addressed
        CHECK (notified_at IS NULL OR (notice_session_ref <> '' AND notice_session_kind <> ''));

CREATE TABLE notices (
    id              bigserial PRIMARY KEY,
    team_id         uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    channel_id      uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    sender_ref      text NOT NULL,
    sender_name     text NOT NULL DEFAULT '',
    session_ref     text NOT NULL,
    session_kind    text NOT NULL,
    -- The provider's id for the message it answers; empty quotes nothing.
    in_reply_to     text NOT NULL DEFAULT '',
    kind            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending',
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,

    CONSTRAINT notices_kind_known CHECK (kind IN ('waiting', 'admitted')),
    CONSTRAINT notices_status_known CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT notices_addressed CHECK (sender_ref <> '' AND session_ref <> '' AND session_kind <> ''),
    CONSTRAINT notices_attempts_nonneg CHECK (attempts >= 0),
    CONSTRAINT notices_sent_when_sent CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
    CONSTRAINT notices_error_when_failed CHECK (status <> 'failed' OR last_error IS NOT NULL)
);

-- The sender's next pick, and the order inside one conversation: "you're in"
-- never overtakes the "waiting" it follows.
CREATE INDEX notices_due ON notices (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notices_conversation_order ON notices (channel_id, session_ref, id) WHERE status = 'pending';

-- +goose Down
SET lock_timeout = '3s';

DROP TABLE notices;

ALTER TABLE pending_senders
    DROP CONSTRAINT pending_senders_notice_addressed,
    DROP COLUMN notice_message_ref,
    DROP COLUMN notice_session_kind,
    DROP COLUMN notice_session_ref,
    DROP COLUMN notified_at;

ALTER TABLE channels DROP COLUMN notify_strangers;
