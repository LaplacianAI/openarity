-- +goose Up
SET lock_timeout = '3s';

-- What a message arrived with, and where the bytes went. The bytes themselves
-- are in the object store, encrypted under the team's key before they left the
-- process, so this table holds nothing that is worth stealing on its own.
--
-- object_key is a key, never a URL. Changing endpoint, bucket or provider is a
-- configuration change rather than an UPDATE over every row, and a URL would
-- also imply the object is reachable directly — it is not, and Task 6's read
-- path exists so it never becomes so.
--
-- Deleting a row does not delete the object. Postgres cascades do not reach a
-- bucket, so a deleted message leaves its bytes behind until the reaper in
-- Task 7 exists. That is a known leak rather than an oversight.
CREATE TABLE attachments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,

    object_key text NOT NULL,

    -- Which key opened it. One value today, and the column exists anyway:
    -- rotation is impossible without knowing which key a given object was
    -- sealed under, and adding this now costs nothing while adding it later
    -- means a migration over every attachment ever stored.
    key_version integer NOT NULL DEFAULT 1,

    -- What the bytes are, decided by sniffing them rather than by believing
    -- the provider. Stored so the read path can serve exactly this and never
    -- re-guess from the filename: an SVG carrying a script sniffs as
    -- text/plain, and is inert only while it is served as text/plain.
    media_type text NOT NULL,

    -- The plaintext length, which is what a person is shown. What the object
    -- store holds is 28 bytes longer — a 12-byte nonce and a 16-byte tag.
    size_bytes bigint NOT NULL,

    -- The provider's name for the file, and therefore attacker-controlled. It
    -- is a label to display, never a path and never a source of a media type.
    filename text NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT attachments_object_key_present CHECK (object_key <> ''),
    CONSTRAINT attachments_media_type_present CHECK (media_type <> ''),

    -- The upper bound is not MaxAttachment. Mirroring a Go constant here would
    -- give two numbers that drift, and the one that must hold is the one the
    -- upload path enforces before anything is encrypted. This says only that
    -- the column means what it claims.
    CONSTRAINT attachments_size_not_negative CHECK (size_bytes >= 0),

    CONSTRAINT attachments_key_version_positive CHECK (key_version >= 1),

    -- The same shape as sessions_ref_bounded, for the same reason: this
    -- arrives from an unauthenticated webhook and is stored exactly as sent.
    -- A provider that sends a megabyte of filename should be refused by the
    -- database, not merely by whoever remembered to check.
    CONSTRAINT attachments_filename_bounded CHECK (char_length(filename) <= 512),
    CONSTRAINT attachments_media_type_bounded CHECK (char_length(media_type) <= 255)
);

-- Listing what a message arrived with, which is the only read the API does.
CREATE INDEX attachments_message_id_idx ON attachments (message_id);

-- Deletion asks whether any other row still points at an object before
-- removing it. Without this that question is a sequential scan, and it is
-- asked once per attachment deleted.
--
-- It also anticipates content-addressed keys, where one file forwarded twice
-- is one object and two rows: two messages, two attachments, one object_key.
-- Not unique for exactly that reason.
CREATE INDEX attachments_object_key_idx ON attachments (object_key);

-- +goose Down
DROP TABLE attachments;
