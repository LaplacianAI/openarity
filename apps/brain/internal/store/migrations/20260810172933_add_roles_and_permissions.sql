-- +goose Up
SET lock_timeout = '3s';

CREATE TABLE roles (
    name        text PRIMARY KEY,
    description text NOT NULL DEFAULT ''
);

CREATE TABLE role_permissions (
    role   text NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    action text NOT NULL,
    PRIMARY KEY (role, action)
);

INSERT INTO roles (name, description) VALUES
    ('admin',     'Full access within a team, including membership'),
    ('developer', 'Create and change agents and tools');

INSERT INTO role_permissions (role, action) VALUES
    ('admin', 'agent:write'), ('admin', 'tool:write'),
    ('admin', 'channel:write'), ('admin', 'member:write'),
    ('developer', 'agent:write'), ('developer', 'tool:write');

ALTER TABLE team_members
    DROP CONSTRAINT team_members_role_check,
    ADD CONSTRAINT team_members_role_fkey FOREIGN KEY (role) REFERENCES roles(name);

-- +goose Down
ALTER TABLE team_members
    DROP CONSTRAINT team_members_role_fkey,
    ADD CONSTRAINT team_members_role_check CHECK (role IN ('admin','developer'));

DROP TABLE role_permissions;
DROP TABLE roles;
