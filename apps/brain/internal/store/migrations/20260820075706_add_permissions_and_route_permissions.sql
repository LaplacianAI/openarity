-- +goose Up
SET lock_timeout = '3s';

-- Permissions become data. An enterprise composing a role in a dashboard
-- picks from these rows, and adding one is a row rather than a deploy.
--
-- description is what the dashboard shows. "membership:write" tells an
-- administrator nothing about what they are about to grant.
CREATE TABLE permissions (
    name        text PRIMARY KEY,
    description text NOT NULL DEFAULT ''
);

-- Backfill from what is already granted, so the foreign key below has
-- something to point at. Descriptions arrive with rbac.json; the column
-- defaults to empty rather than being nullable, so "not filled in yet" and
-- "deliberately blank" are the same state and neither is a null check.
INSERT INTO permissions (name)
SELECT DISTINCT action FROM role_permissions;

-- The hole this closes: action was plain text with nothing behind it, so
-- 'membership:writ' inserted happily, granted nothing, and said nothing.
-- RESTRICT rather than CASCADE — a permission still granted to somebody must
-- not vanish underneath them because a line was deleted from a config file.
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_action_fkey
    FOREIGN KEY (action) REFERENCES permissions(name) ON DELETE RESTRICT;

-- Which permission a route requires, so pointing a route at a permission is
-- also data.
--
-- The safety this needs, and the reason it can have it: the brain serves
-- these routes itself, so at startup it compares its own mux against this
-- table and refuses to start if a protected route has no row. A missing row
-- is a crash that names the route, never an open endpoint.
CREATE TABLE route_permissions (
    method     text NOT NULL,
    path       text NOT NULL,
    -- Null for the scopes that need no permission. Not every protected route
    -- is permission-based: three of the seven today are gated by membership
    -- or by super admin alone.
    permission text REFERENCES permissions(name) ON DELETE RESTRICT,

    -- Which check runs.
    --
    --   authenticated  signed in, nothing more — the handler filters what it
    --                  returns, as GET /teams does by membership
    --   team         hold the permission in the team named by the path
    --   any_team     hold it in any team at all — strictly weaker, since an
    --                admin of one team passes it, so it is for routes with no
    --                team in the path and nothing else
    --   member       belong to the team, no permission needed
    --   super_admin  configured in OPENARITY_SUPER_ADMINS
    --
    -- Every protected route gets a row, including the last two. Without them
    -- the startup check cannot tell "guarded another way" from "somebody
    -- forgot", which is the one distinction it exists to make.
    scope      text NOT NULL CHECK (scope IN
                   ('authenticated', 'member', 'team', 'any_team', 'super_admin')),

    PRIMARY KEY (method, path),

    -- The two halves have to agree: a team-scoped route with no permission
    -- would check nothing, and a super-admin route naming one would imply the
    -- permission matters when it does not.
    CONSTRAINT route_permissions_permission_matches_scope CHECK (
        (scope IN ('team', 'any_team') AND permission IS NOT NULL)
        OR
        (scope IN ('authenticated', 'member', 'super_admin') AND permission IS NULL)
    ),

    -- http.ServeMux patterns are upper case, so a lower-case method here
    -- would map a route the mux never serves.
    CONSTRAINT route_permissions_method_upper CHECK (method = upper(method))
);

-- +goose Down
DROP TABLE route_permissions;

ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_action_fkey;

DROP TABLE permissions;
