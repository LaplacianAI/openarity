-- +goose Up
SET lock_timeout = '3s';

-- The action is a plain text column with no foreign key behind it, so this
-- statement is the whole of that rename. Only admin holds it today.
UPDATE role_permissions SET action = 'membership:write' WHERE action = 'member:write';

-- The role cannot be renamed in place: team_members.role references
-- roles(name) with no ON UPDATE, so the new row has to exist before anything
-- points at it, and the old one cannot go until nothing does.
INSERT INTO roles (name, description) VALUES
    ('member', 'Create and change agents and tools within a team');

INSERT INTO role_permissions (role, action)
SELECT 'member', action FROM role_permissions WHERE role = 'developer';

UPDATE team_members SET role = 'member' WHERE role = 'developer';

-- role_permissions is ON DELETE CASCADE, so developer's rows go with it.
DELETE FROM roles WHERE name = 'developer';

-- +goose Down
SET lock_timeout = '3s';

INSERT INTO roles (name, description) VALUES
    ('developer', 'Create and change agents and tools');

INSERT INTO role_permissions (role, action)
SELECT 'developer', action FROM role_permissions WHERE role = 'member';

UPDATE team_members SET role = 'developer' WHERE role = 'member';

DELETE FROM roles WHERE name = 'member';

UPDATE role_permissions SET action = 'member:write' WHERE action = 'membership:write';
