-- +goose Up
SET lock_timeout = '3s';

CREATE TABLE agents (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id             uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name                text NOT NULL,
    description         text NOT NULL DEFAULT '',
    kind                text NOT NULL DEFAULT 'custom',
    instructions        text NOT NULL DEFAULT '',
    model_name          text NOT NULL,
    max_tokens          integer NOT NULL,
    temperature         double precision,
    pattern             text NOT NULL,
    max_steps           integer NOT NULL,
    steer_continuations integer NOT NULL DEFAULT 0,
    output_schema       jsonb,
    parser              boolean NOT NULL DEFAULT false,
    parser_model_name   text,
    parser_max_tokens   integer,
    parser_temperature  double precision,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT agents_name_present CHECK (name <> ''),
    CONSTRAINT agents_model_present CHECK (model_name <> ''),
    CONSTRAINT agents_kind_known CHECK (kind IN
        ('custom', 'orchestrator', 'probing', 'memory', 'chat')),
    CONSTRAINT agents_pattern_known CHECK (pattern IN
        ('react', 'plan', 'rewoo', 'reflection', 'code', 'custom')),
    CONSTRAINT agents_max_tokens_positive CHECK (max_tokens > 0),
    CONSTRAINT agents_max_steps_positive CHECK (max_steps > 0),
    CONSTRAINT agents_steer_continuations_nonneg CHECK (steer_continuations >= 0),
    -- The SDK refuses a parser with no schema at run time; refusing it here
    -- means the agent that cannot run is never saved.
    CONSTRAINT agents_parser_needs_schema CHECK (NOT parser OR output_schema IS NOT NULL),
    CONSTRAINT agents_output_schema_object CHECK (
        output_schema IS NULL OR jsonb_typeof(output_schema) = 'object'),
    CONSTRAINT agents_parser_settings_need_a_model CHECK (
        parser_model_name IS NOT NULL
        OR (parser_max_tokens IS NULL AND parser_temperature IS NULL)),
    CONSTRAINT agents_parser_max_tokens_positive CHECK (
        parser_max_tokens IS NULL OR parser_max_tokens > 0),

    -- For the composite keys the grant tables point at.
    CONSTRAINT agents_id_team_key UNIQUE (id, team_id)
);

CREATE UNIQUE INDEX agents_team_name_key ON agents (team_id, lower(name));

-- The list query filters by team and walks (created_at, id) backwards, so one
-- index answers both rather than a team index and a sort.
CREATE INDEX agents_team_page_idx ON agents (team_id, created_at, id);

CREATE TABLE mcp_servers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name            text NOT NULL,
    url             text,
    command         text[],
    env             jsonb NOT NULL DEFAULT '{}',
    auth_secret_ref text,
    bare            boolean NOT NULL DEFAULT false,
    discovered_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- The SDK prefixes every tool as name__tool and rejects anything outside
    -- this set, so a name that cannot prefix is a server whose tools never load.
    CONSTRAINT mcp_servers_name_is_prefix CHECK (name ~ '^[a-zA-Z0-9_-]{1,64}$'),
    CONSTRAINT mcp_servers_one_transport CHECK ((url IS NULL) <> (command IS NULL)),
    CONSTRAINT mcp_servers_url_present CHECK (url IS NULL OR url <> ''),
    CONSTRAINT mcp_servers_command_present CHECK (
        command IS NULL OR cardinality(command) > 0),
    CONSTRAINT mcp_servers_env_object CHECK (jsonb_typeof(env) = 'object'),
    CONSTRAINT mcp_servers_auth_ref_present CHECK (
        auth_secret_ref IS NULL OR auth_secret_ref <> ''),

    CONSTRAINT mcp_servers_id_team_key UNIQUE (id, team_id)
);

CREATE UNIQUE INDEX mcp_servers_team_name_key ON mcp_servers (team_id, lower(name));
CREATE INDEX mcp_servers_team_page_idx ON mcp_servers (team_id, created_at, id);

CREATE TABLE mcp_tools (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mcp_server_id uuid NOT NULL,
    team_id       uuid NOT NULL,
    name          text NOT NULL,
    description   text NOT NULL DEFAULT '',
    input_schema  jsonb NOT NULL DEFAULT '{"type":"object"}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT mcp_tools_name_present CHECK (name <> ''),
    CONSTRAINT mcp_tools_server_in_team
        FOREIGN KEY (mcp_server_id, team_id) REFERENCES mcp_servers (id, team_id)
        ON DELETE CASCADE,
    CONSTRAINT mcp_tools_server_name_key UNIQUE (mcp_server_id, name)
);

-- One parsed SKILL.md. The columns are the Agent Skills specification's
-- frontmatter, so a skill written for Claude Code, Codex or Agno uploads here
-- unchanged, and the rules below are that spec's rather than ours.
CREATE TABLE skills (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name          text NOT NULL,
    description   text NOT NULL,
    license       text,
    compatibility text,
    metadata      jsonb NOT NULL DEFAULT '{}',
    allowed_tools text,
    body          text NOT NULL,
    -- Where the directory came from. An imported skill is third-party
    -- instructions; recording the origin is what lets a team see that, and
    -- what lets sync fetch the same thing again.
    source        text NOT NULL DEFAULT 'upload',
    source_ref    text,
    source_sha    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- 1-64 characters, lower case alphanumerics and hyphens, never leading,
    -- trailing or doubled. The regex says all of that at once.
    CONSTRAINT skills_name_is_spec_shaped CHECK (
        name ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(name) <= 64),
    -- The description is all the model sees when choosing a skill, and the
    -- spec caps it so a listing of many skills stays affordable.
    CONSTRAINT skills_description_present CHECK (description <> '' AND length(description) <= 1024),
    CONSTRAINT skills_compatibility_shaped CHECK (
        compatibility IS NULL OR (compatibility <> '' AND length(compatibility) <= 500)),
    CONSTRAINT skills_license_present CHECK (license IS NULL OR license <> ''),
    CONSTRAINT skills_allowed_tools_present CHECK (allowed_tools IS NULL OR allowed_tools <> ''),
    CONSTRAINT skills_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT skills_source_known CHECK (source IN ('upload', 'github', 'url')),
    -- An upload has no origin to record; an import always has both halves.
    CONSTRAINT skills_source_recorded CHECK (
        (source = 'upload' AND source_ref IS NULL AND source_sha IS NULL)
        OR (source <> 'upload' AND source_ref IS NOT NULL AND source_sha IS NOT NULL)),

    CONSTRAINT skills_id_team_key UNIQUE (id, team_id)
);

-- Names are lower case by constraint, so the index needs no lower().
CREATE UNIQUE INDEX skills_team_name_key ON skills (team_id, name);
CREATE INDEX skills_team_page_idx ON skills (team_id, created_at, id);

-- Every file bundled beside the SKILL.md. The bytes are encrypted under the
-- team's key and live in the object store; this row holds where and what.
CREATE TABLE skill_files (
    skill_id   uuid NOT NULL,
    team_id    uuid NOT NULL,
    path       text NOT NULL,
    size       bigint NOT NULL,
    sha256     bytea NOT NULL,
    media_type text NOT NULL,
    object_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (skill_id, path),
    CONSTRAINT skill_files_skill_in_team
        FOREIGN KEY (skill_id, team_id) REFERENCES skills (id, team_id)
        ON DELETE CASCADE,

    -- A path is relative, inside the skill, and is never joined onto anything
    -- here — but a row that still looks like a traversal invites the first
    -- caller that does. Refused: absolute, a .. segment, an empty segment, a
    -- trailing slash and a backslash. A NUL needs no clause: text cannot hold
    -- one, so Postgres refuses it before any CHECK runs.
    CONSTRAINT skill_files_path_is_inside CHECK (
        path <> ''
        AND left(path, 1) <> '/'
        AND right(path, 1) <> '/'
        AND path !~ '(^|/)\.\.(/|$)'
        AND path !~ '//'
        AND path !~ '\\'),

    -- The SKILL.md is the skills row, not a file beside it.
    CONSTRAINT skill_files_not_the_manifest CHECK (path <> 'SKILL.md'),
    CONSTRAINT skill_files_size_nonneg CHECK (size >= 0),
    CONSTRAINT skill_files_sha256_is_sha256 CHECK (octet_length(sha256) = 32),
    CONSTRAINT skill_files_media_type_present CHECK (media_type <> ''),
    CONSTRAINT skill_files_object_key_present CHECK (object_key <> '')
);

-- The reaper asks whether any row still needs an object before deleting it,
-- and that question is now asked of this table as well as attachments.
CREATE INDEX skill_files_object_key_idx ON skill_files (object_key);

-- The same tombstone attachments get, for the same reason: a cascade never
-- runs our SQL, and deleting a team removes its skills' files without any Go
-- code seeing a row.
-- +goose StatementBegin
CREATE FUNCTION skill_files_record_deleted_object() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_objects (object_key, team_id)
    SELECT object_key, team_id FROM deleted_rows
    ON CONFLICT (object_key) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER skill_files_tombstone_deleted_objects
    AFTER DELETE ON skill_files
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION skill_files_record_deleted_object();

-- team_id is on the link rows because a trigger cannot see above it in a
-- cascade: deleting a team removes the agent before its links' trigger fires.
-- The composite keys then make a grant across teams impossible to write.
--
-- RESTRICT holds during a team delete only because agents is created above
-- mcp_servers and skills: Postgres runs a table's cascades in the order their
-- constraints were created, so the grants are gone before a server is. NO
-- ACTION does not help — each cascade is checked when it finishes, not when
-- the whole delete does. TestDeletingATeamWithGrantsSucceeds pins the order.
CREATE TABLE agent_mcp_servers (
    agent_id      uuid NOT NULL,
    mcp_server_id uuid NOT NULL,
    team_id       uuid NOT NULL,
    allow         text[],

    PRIMARY KEY (agent_id, mcp_server_id),
    CONSTRAINT agent_mcp_servers_agent_in_team
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id)
        ON DELETE CASCADE,
    CONSTRAINT agent_mcp_servers_server_in_team
        FOREIGN KEY (mcp_server_id, team_id) REFERENCES mcp_servers (id, team_id)
        ON DELETE RESTRICT,
    -- An empty list would grant a server and none of its tools.
    CONSTRAINT agent_mcp_servers_allow_not_empty CHECK (
        allow IS NULL OR cardinality(allow) > 0)
);

-- Deleting a server checks the grants against this; without it that is a scan.
CREATE INDEX agent_mcp_servers_server_idx ON agent_mcp_servers (mcp_server_id, team_id);

CREATE TABLE agent_skills (
    agent_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    team_id  uuid NOT NULL,

    PRIMARY KEY (agent_id, skill_id),
    CONSTRAINT agent_skills_agent_in_team
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id)
        ON DELETE CASCADE,
    CONSTRAINT agent_skills_skill_in_team
        FOREIGN KEY (skill_id, team_id) REFERENCES skills (id, team_id)
        ON DELETE RESTRICT
);

CREATE INDEX agent_skills_skill_idx ON agent_skills (skill_id, team_id);

-- +goose Down
DROP TABLE agent_skills;
DROP TABLE agent_mcp_servers;
DROP TRIGGER skill_files_tombstone_deleted_objects ON skill_files;
DROP FUNCTION skill_files_record_deleted_object();
DROP TABLE skill_files;
DROP TABLE skills;
DROP TABLE mcp_tools;
DROP TABLE mcp_servers;
DROP TABLE agents;
