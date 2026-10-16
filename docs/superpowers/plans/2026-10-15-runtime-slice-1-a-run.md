# Runtime slice 1, a run: implementation plan

> **For agentic workers:** this plan is executed under the working agreement,
> not subagent-driven development. **The user writes every file that is not a
> test**: Go, migrations, SQL, `rbac.json`, `openapi.yaml`. That is what
> `apps/brain/CLAUDE.md` requires. Claude hands each file over complete, one per
> reply, then reviews what was pasted and writes the tests that attack it.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A person starts a run through the API. A brain worker executes it with
the agent's granted MCP tools and skills, the events stream as they happen, and
the run is recorded. It survives a worker dying and can be cancelled.

**Architecture:** `POST /teams/{id}/runs` writes a `queued` row, commits, then
enqueues a DBOS workflow whose id is the run id. A sweep once a minute catches
any enqueue that was lost. The workflow is a single step that claims the row,
builds an `agent.Spec` from Postgres, and calls the SDK's `Runner.Run` with a
session stored in the SDK's tables under the run id. It records events and
finishes the row. Each worker has its own DBOS executor id. A run whose worker
stops heartbeating is taken over by exactly one other worker and resumed from
its transcript. Time is always compared on the database's clock.

**Tech Stack:**
- Go 1.26.9
- pgx/v5, sqlc, goose
- DBOS `dbos-transact-golang` v1.2.0
- `github.com/LaplacianAI/openarity/sdk/agent`, through a `replace`: `patterns`, `models/openaicompat`, `tools/mcp`, `stores/postgres`, `stores/storetest`
- `github.com/modelcontextprotocol/go-sdk`, in tests only

**Spec:** [`docs/superpowers/specs/2026-10-14-agent-runtime-design.md`](../specs/2026-10-14-agent-runtime-design.md).
Read these sections of it: *What is stored*, *Execution*, *Limits*, *Failure*, and *Probed at the start of slice 1*.

## How this plan differs from the template

As in the agent-registry plan, tests are specified by **name and the behaviour
they assert**, not as full code. Claude writes them against each production
file after it is pasted. The **schema, the queries the design depends on, and
every interface** are exact, because neighbouring tasks rely on them.

The four DBOS probes the spec asked for are already done. Their results are in
the spec and are built into Tasks 3, 7, 8 and 10:

| Probe | Result | Where it lands |
| --- | --- | --- |
| A repeated workflow id | the same workflow; enqueuing it again is a no-op | Tasks 8 and 9: the run id is the workflow id, so the sweep can enqueue freely |
| Waking the worker | the API enqueues through `dbos.Client` after its commit; about 1 s to start; no schedule row per tick | Tasks 8 and 10 |
| Two workers | a shared executor id re-queues a live worker's run; `ResumeWorkflow` from another worker continues a dead one's | Task 3 gives each worker its own id; Task 8 does the takeover |
| Sharing the pool | works, but `dbos.Shutdown` closes the pool it was given | the worker keeps DBOS on its own pool; nothing changes |

## Not in this plan

- **The default entry agent.** A run must name its agent. The flag arrives with
  channels starting runs (spec Open item 4), which is when something first
  starts a run without naming one.
- **`oa run`.** The CLI gets its own small plan once `openapi.yaml` carries
  these routes; its client is generated from that file.
- **Steering a run, and listing a session's runs.**
- **Command MCP servers, built-ins and custom tools.** These belong to slice 2,
  the workspace. A granted command server is recorded as unavailable, not bound.
- **Delegation settings, the `graph` tool and compaction** belong to slices 3–5.
- **Loops a team writes and uploads.** Task 5 makes the loops an agent may
  choose the ones the brain's `Runner` registers, so a deployment adds its own
  in Go. A team writing one without rebuilding the brain, as a script in the
  workspace or as data, is a later design, built on slice 2.

## Global Constraints

- **The user writes production code**; Claude hands it over one file per reply.
  Anchored edits after a file is created; re-read a file before writing an
  anchor, because the user strips comments when pasting.
- **No secret is ever stored in Postgres or the graph.** An MCP server's token
  is read from the secret store at run start through `auth_secret_ref`. No
  event payload holds a header, a token or a secret reference.
- **Never compare a Go time with a Postgres time.** Heartbeats, staleness,
  "queued for longer than", and the wall-clock deadline are all computed in
  SQL with `now()`. Docker Desktop's clock was measured jumping by up to 44
  minutes (see the `docker-clock-skew` memory).
- **RBAC is data.** `run:write`, its grants and the route rows go in
  `internal/store/rbac.json`.
- **A route with no `rbac.json` row panics at startup**, and
  `cmd/brain/spec_test.go` fails when a route and `api/openapi.yaml` disagree.
  The routes, their rows and the spec land in one commit.
- **Never hand-write the Go that runs SQL**: queries go in
  `internal/store/queries/*.sql`, then `make generate`.
- **Migrations:** named `YYYYMMDDHHMMSS_<verb>_<thing>.sql`, beginning
  `SET lock_timeout = '3s';`, with a `-- +goose Down`. Statement-level triggers
  use transition tables.
- **DBOS names are stored state:** every workflow uses `WithWorkflowName`,
  cron expressions have six fields, schedules go through `ApplySchedules`.
  These are the `add-a-background-job` skill's rules.
- **HTTP:** 404 for another team's row or a session the caller cannot see; 400
  for a non-uuid path id; 409 on 23505 and for cancelling a finished run.
  Unexpected errors go through `api.Fail`, and wire structs live in `schema.go`.
- **Tests:** names are sentences. Each new guard is broken once to see a test
  fail (`openarity-practices:verifying-what-you-build`), with a compile gate
  and a no-op gate.
- **Gate before every commit:**
  `cd apps/brain && GOTOOLCHAIN=go1.26.9 make check db=openarity_test port=15432 user=postgres:postgres falkor=redis://127.0.0.1:16380`.
  If a package hits the 10-minute limit under machine load, rerun it with
  `-timeout 45m` and say so; never skip it.
- **Commits:** a Conventional Commit subject written from the reader's side,
  and a body giving the why. **No `Co-Authored-By` trailer and no agent
  attribution.** Claude commits; the user pushes.

## Review Focus

Five ways this can hurt someone that the spec implies but no happy-path test
reaches. Each line names the test that pins it and the task that owns it.

1. **Someone else's direct session.** Starting a run in a direct session the
   caller cannot see, or reading or cancelling a run that lives in one, returns
   404 and writes nothing. `TestARunCannotBeStartedInSomeoneElsesDirectSession`
   and `TestAnotherPersonsDirectRunIsNotFound` (Task 9).
2. **A worker that lost its run keeps going.** After another worker takes over
   a stalled run, the first worker must stop at its next heartbeat and write no
   further event and no finish. `TestAWorkerThatLostItsRunStopsAndWritesNothing`
   (Task 7).
3. **Cancel against every state.** A queued run is cancelled at once, with no
   worker involved, and its later dequeue does nothing. A finished run answers
   409. Cancelling twice is not an error.
   `TestCancellingAQueuedRunNeedsNoWorker` and
   `TestCancellingAFinishedRunIsAConflict` (Task 9);
   `TestADequeuedRunThatWasCancelledDoesNothing` (Task 7).
4. **The agent changes between POST and start.** If the agent was deleted, the
   run fails with an error naming it, and `agent_name` keeps the record
   readable. If a grant was removed, the run binds what is granted at start.
   `TestARunWhoseAgentWasDeletedFailsNamingIt` (Task 7);
   `TestToolsAreThoseGrantedWhenTheRunStarts` (Task 4).
5. **Long streams and large results.** An event stream held open past the
   server's 30 s `WriteTimeout` keeps delivering. A tool result over 64 KiB is
   cut and marked `truncated`.
   `TestAnEventStreamOutlivesTheServersWriteTimeout` (Task 9);
   `TestALargeToolResultIsCutAndSaysSo` (Task 6).

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/store/migrations/<ts>_add_runs.sql` | `runs`, `run_events`, the SDK's transcript tables, and the trigger that clears a deleted run's transcript |
| `internal/store/queries/runs.sql` | create, read-if-visible, claim, heartbeat, usage, finish, cancel, and the sweep's two queries |
| `internal/store/queries/run_events.sql` | append, guarded by who holds the run; list after a sequence number |
| `internal/store/queries/{agents,sessions}.sql` | `GetAgentByName`, `ListGrantedMCPServers`, `ListGrantedSkills`; `CreateDirectSession`, `GetSessionVisible` |
| `internal/store/rbac.json` | `run:write`, granted to admin and member; five route rows |
| `internal/worker/worker.go` | an executor id per process; `AppName` exported |
| `internal/discovery/discovery.go` | `Client`: the same dial guard and token for a run's MCP connections |
| `internal/runtime/build.go` | an agent row and its grants become an `agent.Spec`, plus what could not be bound; `Patterns` and `Loops`, the one list of loops |
| `internal/store/migrations/<ts>_name_loops_by_the_runner.sql` | the hard-coded pattern list becomes a name-format check |
| `internal/api/agents/`, `internal/api/loops/` | a pattern is checked against the runner's loops; `GET /teams/{id}/loops` |
| `internal/runtime/events.go` | SDK events become `run_events` rows; usage is counted against the budget |
| `internal/runtime/execute.go` | one run: claim, heartbeat, build, run, finish |
| `internal/runtime/job.go` | the workflow, the queue, the sweep schedule, and `Enqueuer` for the API |
| `internal/api/runs/` | start, read, cancel, and stream events |
| `cmd/brain/{serve,worker,routers}.go`, `go.mod` | wiring, the SDK dependency |
| `api/openapi.yaml` | the four run routes and the loops route, with their schemas; the corrected `pattern` description |
| `apps/brain/CLAUDE.md`, `.claude/skills/add-a-background-job` | the layout line for `internal/runtime`, and the executor-id rule |

There are eleven tasks. **Tasks 1–2 unblock everything.** Task 3 is small, but it
must land before any run executes, because a shared executor id can run a run
twice. Nothing executes until Task 7. Nothing is reachable through the API until
Task 10.

---

### Task 1: The schema

**Files:**
- Create: `internal/store/migrations/<ts>_add_runs.sql` (`make migration name=add_runs`)
- Modify: `internal/store/rbac.json`. Add the `run:write` permission, and add it to `admin` and `member`. Route rows come in Task 9.
- Test: `internal/store/runs_schema_test.go`

**Produces:** this migration.

```sql
-- +goose Up
SET lock_timeout = '3s';

CREATE TABLE runs (
    id                  uuid PRIMARY KEY,
    team_id             uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    session_id          uuid NOT NULL,
    parent_run_id       uuid REFERENCES runs(id) ON DELETE CASCADE,
    root_run_id         uuid NOT NULL,
    parent_tool_call    text,
    depth               integer NOT NULL DEFAULT 0,
    agent_id            uuid REFERENCES agents(id) ON DELETE SET NULL,
    agent_name          text NOT NULL,
    user_id             uuid REFERENCES users(id) ON DELETE SET NULL,
    status              text NOT NULL DEFAULT 'queued',
    input               text NOT NULL,
    output              text,
    error               text,
    input_tokens        bigint NOT NULL DEFAULT 0,
    output_tokens       bigint NOT NULL DEFAULT 0,
    worker              text,
    heartbeat_at        timestamptz,
    cancel_requested_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,

    CONSTRAINT runs_session_in_team
        FOREIGN KEY (session_id, team_id) REFERENCES sessions (id, team_id) ON DELETE CASCADE,
    CONSTRAINT runs_status_known CHECK (
        status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT runs_input_present CHECK (input <> ''),
    CONSTRAINT runs_agent_name_present CHECK (agent_name <> ''),
    -- A root run is its own root, at depth 0, created by no tool call; a
    -- delegate is none of those. One shape per kind of run.
    CONSTRAINT runs_root_shape CHECK (
        (parent_run_id IS NULL) = (root_run_id = id)
        AND (parent_run_id IS NULL) = (depth = 0)
        AND (parent_run_id IS NULL) = (parent_tool_call IS NULL)),
    CONSTRAINT runs_depth_nonneg CHECK (depth >= 0),
    CONSTRAINT runs_finished_when_terminal CHECK (
        (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CONSTRAINT runs_error_when_failed CHECK ((status = 'failed') = (error IS NOT NULL)),
    CONSTRAINT runs_held_while_running CHECK (
        status <> 'running' OR (worker IS NOT NULL AND heartbeat_at IS NOT NULL)),
    CONSTRAINT runs_tokens_nonneg CHECK (input_tokens >= 0 AND output_tokens >= 0)
);

-- A parent that resumes and repeats a delegate call finds the child it made.
CREATE UNIQUE INDEX runs_parent_tool_call_key
    ON runs (parent_run_id, parent_tool_call) WHERE parent_run_id IS NOT NULL;
CREATE INDEX runs_session_recent_idx ON runs (session_id, created_at DESC);
-- The sweep reads only unfinished runs.
CREATE INDEX runs_unfinished_idx ON runs (status, heartbeat_at)
    WHERE status IN ('queued', 'running');
-- The SET NULLs on agent and user delete would otherwise scan.
CREATE INDEX runs_agent_idx ON runs (agent_id);
CREATE INDEX runs_user_idx ON runs (user_id);

CREATE TABLE run_events (
    run_id     uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq        integer NOT NULL,
    kind       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (run_id, seq),
    CONSTRAINT run_events_seq_positive CHECK (seq > 0),
    CONSTRAINT run_events_kind_known CHECK (kind IN (
        'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
        'tool_unavailable', 'finished')),
    CONSTRAINT run_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

-- The SDK's session tables, as sdk/agent/stores/postgres.Schema declares them.
-- The session is the run id as text.
CREATE TABLE agent_messages (
    session  text NOT NULL,
    position int  NOT NULL,
    body     json NOT NULL,
    PRIMARY KEY (session, position)
);

CREATE TABLE agent_steers (
    id      bigserial PRIMARY KEY,
    session text NOT NULL,
    text    text NOT NULL
);

CREATE INDEX agent_steers_session ON agent_steers (session, id);

-- A transcript has no foreign key: the SDK keys it by a string. Deleting a
-- run, including through a team's cascade, must not leave a conversation behind.
-- +goose StatementBegin
CREATE FUNCTION runs_forget_transcripts() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM agent_messages WHERE session IN (SELECT id::text FROM gone);
    DELETE FROM agent_steers   WHERE session IN (SELECT id::text FROM gone);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER runs_forget_transcripts
    AFTER DELETE ON runs
    REFERENCING OLD TABLE AS gone
    FOR EACH STATEMENT
    EXECUTE FUNCTION runs_forget_transcripts();

-- +goose Down
DROP TRIGGER runs_forget_transcripts ON runs;
DROP FUNCTION runs_forget_transcripts();
DROP TABLE agent_steers;
DROP TABLE agent_messages;
DROP TABLE run_events;
DROP TABLE runs;
```

- [ ] **Step 1:** Claude hands over the migration; the user pastes it. Then the `rbac.json` permission edit.
- [ ] **Step 2:** Claude writes `runs_schema_test.go`, against real Postgres:
  - `TestARootRunIsItsOwnRootAtDepthZero`: `root_run_id ≠ id` with no parent gives 23514 `runs_root_shape`; so does depth 1 with no parent.
  - `TestARunCannotNameASessionInAnotherTeam`: 23503 `runs_session_in_team`.
  - `TestARunningRunNeedsAWorkerAndAHeartbeat`: 23514 `runs_held_while_running`.
  - `TestOnlyAFinishedRunHasAFinishTime`, plus `TestOnlyAFailedRunHasAnError`.
  - `TestDeletingTheAgentKeepsTheRunAndItsName`: `agent_id` becomes null and `agent_name` stays.
  - `TestDeletingARunForgetsItsTranscript`: rows in `agent_messages` and `agent_steers` for that run are gone, and those of another run are kept.
  - `TestDeletingATeamForgetsEveryTranscriptInIt`: the cascade path, with no trigger error.
  - `TestTheTranscriptTablesPassTheSDKsOwnStoreSuite`: `storetest.Run` with `postgres.New(pool)` against the migrated schema. This proves the tables are the SDK's, not a lookalike.
  - `TestAParentCannotMakeTwoChildrenFromOneToolCall`: 23505.
  - `TestRunWriteIsGrantedToAdminAndMember`: through the existing rbac load test helpers.
- [ ] **Step 3:** `brain migrate up`, then `down` and `up` again; paste the output.
- [ ] **Step 4:** Break `runs_root_shape`, the trigger's second `DELETE`, and `ON DELETE SET NULL` in turn; each test named above fails. Then run the gate and commit `feat(brain): the tables a run is recorded in, and its transcript with it`.

### Task 2: Queries

**Files:**
- Create: `internal/store/queries/runs.sql`, `internal/store/queries/run_events.sql`
- Modify: `internal/store/queries/agents.sql`, `internal/store/queries/sessions.sql`
- Test: `internal/store/runs_test.go`, `internal/store/run_events_test.go`

**Produces** (the sqlc names every later task uses). The queries the design
turns on are written out in full. The rest are one-liners described in a comment.

```sql
-- runs.sql

-- The agent's team is checked by the SELECT: an agent of another team inserts
-- nothing, and the caller gets pgx.ErrNoRows.
-- name: CreateRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input)
SELECT sqlc.arg('id')::uuid, a.team_id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       a.id, a.name, sqlc.narg('user_id')::uuid, sqlc.arg('input')::text
FROM agents a
WHERE a.id = sqlc.arg('agent_id') AND a.team_id = sqlc.arg('team_id')
RETURNING *;

-- The session rule from ListSessionsByTeam, applied to the run's session.
-- name: GetRunVisible :one
SELECT r.* FROM runs r
JOIN sessions s ON s.id = r.session_id
WHERE r.id = sqlc.arg('id') AND r.team_id = sqlc.arg('team_id')
  AND (s.kind <> 'direct' OR sqlc.arg('moderator')::bool OR s.user_id = sqlc.arg('viewer')::uuid);

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- Queued: take it. Running and already ours (after a takeover): carry on.
-- Anything else (cancelled while queued, finished, someone else's): no row.
-- remaining_seconds is the wall-clock budget left, measured on this clock.
-- name: ClaimRun :one
UPDATE runs SET
    status       = 'running',
    worker       = sqlc.arg('worker'),
    heartbeat_at = now(),
    started_at   = coalesce(started_at, now())
WHERE id = sqlc.arg('id')
  AND (status = 'queued' OR (status = 'running' AND worker = sqlc.arg('worker')))
RETURNING *,
    extract(epoch FROM coalesce(started_at, now())
        + make_interval(secs => sqlc.arg('wall_clock_seconds')::float8) - now())::float8
        AS remaining_seconds;

-- No row means this worker no longer holds the run.
-- name: HeartbeatRun :one
UPDATE runs SET heartbeat_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker') AND status = 'running'
RETURNING cancel_requested_at;

-- Counted as it is spent, so a resumed run continues from the same total.
-- name: AddRunUsage :one
UPDATE runs SET
    input_tokens  = input_tokens  + sqlc.arg('input_tokens'),
    output_tokens = output_tokens + sqlc.arg('output_tokens')
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker') AND status = 'running'
RETURNING input_tokens + output_tokens AS spent;

-- name: FinishRun :execrows
UPDATE runs SET
    status      = sqlc.arg('status'),
    output      = sqlc.narg('output'),
    error       = sqlc.narg('error'),
    finished_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker') AND status = 'running';

-- Queued: cancelled now, since no worker holds it. Running: flagged for the
-- worker's next heartbeat. Finished: no row, which the handler answers 409.
-- name: RequestRunCancel :one
UPDATE runs SET
    cancel_requested_at = coalesce(cancel_requested_at, now()),
    status      = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
    finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END
WHERE id = sqlc.arg('id') AND status IN ('queued', 'running')
RETURNING *;

-- name: ListUnstartedRuns :many
SELECT id FROM runs
WHERE status = 'queued'
  AND created_at < now() - make_interval(secs => sqlc.arg('older_than_seconds')::float8)
ORDER BY created_at
LIMIT 100;

-- One winner per stale run: the row is locked while it is taken, and the old
-- worker's next heartbeat finds worker changed and stops.
-- name: TakeOverStaleRuns :many
UPDATE runs SET worker = sqlc.arg('worker'), heartbeat_at = now()
WHERE id IN (
    SELECT id FROM runs
    WHERE status = 'running'
      AND heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8)
    ORDER BY heartbeat_at
    LIMIT 20
    FOR UPDATE SKIP LOCKED)
RETURNING id;
```

```sql
-- run_events.sql

-- Written only by the worker holding the run: HAVING drops the row when it is
-- not, so a worker that lost its run cannot interleave with the one that has it.
-- name: AppendRunEvent :one
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT sqlc.arg('run_id')::uuid, coalesce(max(e.seq), 0) + 1,
       sqlc.arg('kind')::text, sqlc.arg('payload')::jsonb
FROM run_events e
WHERE e.run_id = sqlc.arg('run_id')
HAVING EXISTS (SELECT 1 FROM runs
               WHERE id = sqlc.arg('run_id') AND worker = sqlc.arg('worker'))
RETURNING seq;

-- name: ListRunEvents :many
SELECT * FROM run_events
WHERE run_id = sqlc.arg('run_id') AND seq > sqlc.arg('after')
ORDER BY seq
LIMIT sqlc.arg('page_size');
```

```text
agents.sql    GetAgentByName :one         team_id, lower(name) = lower($2), using agents_team_name_key
              ListGrantedMCPServers :many sqlc.embed(mcp_servers), agent_mcp_servers.allow; by agent_id, ordered by name
              ListGrantedSkills :many     skills.* (with body) joined through agent_skills; by agent_id, ordered by name
sessions.sql  CreateDirectSession :one    INSERT (team_id, kind 'direct', user_id) RETURNING *
              GetSessionVisible :one      id, team_id, and the ListSessionsByTeam predicate (viewer, moderator)
```

- [ ] **Step 1:** The user pastes each query file; then `make generate`.
- [ ] **Step 2:** Claude writes the query tests, against real Postgres:
  - `TestCreateRunRefusesAnAgentFromAnotherTeam`: no row.
  - `TestClaimRunTakesAQueuedRunOnce`: two workers claim concurrently; one gets the row.
  - `TestClaimRunCarriesOnARunThisWorkerAlreadyHolds` and `TestClaimRunIgnoresACancelledRun`.
  - `TestTheWallClockLeftIsMeasuredFromTheFirstStart`: claim, then claim again after a takeover. `remaining_seconds` shrinks and does not reset.
  - `TestAHeartbeatFromAWorkerThatLostTheRunReturnsNothing`.
  - `TestTakeOverStaleRunsLeavesALiveRunAlone`: heartbeat set to `now()`, stale threshold 90 s, so no row.
  - `TestTakeOverStaleRunsHasOneWinner`: two takers concurrently get disjoint ids.
  - `TestCancelOfAQueuedRunFinishesIt`, `TestCancelOfARunningRunOnlyFlagsIt`, `TestCancelOfAFinishedRunReturnsNothing`.
  - `TestEventsAreNumberedFromOneWithoutGaps`.
  - `TestAWorkerThatLostTheRunCannotAppendAnEvent`.
  - `TestGetRunVisibleHidesSomeoneElsesDirectRun`: moderator sees it; another member does not.
  - `TestListGrantedMCPServersCarriesTheAllowList`.
- [ ] **Step 3:** Break the `worker =` guard in `AppendRunEvent`, `FOR UPDATE SKIP LOCKED`, and `coalesce(started_at, …)` in turn; each named test fails. Then run the gate and commit `feat(brain): queries that claim, hold, finish and cancel a run`.

### Task 3: Each worker has its own executor id

**Files:**
- Modify: `internal/worker/worker.go`
- Test: `internal/worker/worker_test.go`

**Produces:**

```go
const AppName = "openarity-brain" // the API's DBOS client acts for the same application

// executorID is unique per process: DBOS re-queues every pending workflow
// carrying its own id at launch, so two workers sharing one run each other's
// runs. The hostname is there for a person reading dbos.workflow_status.
func executorID() string // "<hostname>-<8 random hex>"
```

`Run` sets `dbos.Config{AppName: AppName, ExecutorID: executorID(), …}`.
DBOS lets `DBOS__VMID` override it. The skill change in Task 11 says that
replicas must never be given the same value.

- [ ] **Step 1:** Claude hands over the edit; the user pastes it.
- [ ] **Step 2:** Tests:
  - `TestEveryWorkerProcessHasItsOwnExecutorID`: two calls differ, and both start with the hostname.
  - `TestTheAPIAndTheWorkerActForOneApplication`: `AppName` equals the value the old literal held, so workflows already in the database are not orphaned.
- [ ] **Step 3:** Make `executorID` return a constant; the first test fails. Then run the gate and commit `fix(brain): each worker has its own DBOS executor id, so two never run one run`.

### Task 4: Building the agent

**Files:**
- Modify: `internal/discovery/discovery.go` to add `Client`
- Create: `internal/runtime/build.go`
- Test: `internal/discovery/discovery_test.go`, `internal/runtime/build_test.go`

**Produces:**

```go
// discovery
// Client is the guarded transport discovery uses, with the server's token,
// and no deadline of its own: the run's context bounds it.
func (d *Discoverer) Client(ctx context.Context, s db.McpServer) (*http.Client, error) // ErrCommandServer for s.Url == nil

// runtime
type BuildStore interface {
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	ListGrantedMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.ListGrantedMCPServersRow, error)
	ListGrantedSkills(ctx context.Context, agentID uuid.UUID) ([]db.Skill, error)
	ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error)
}

type Objects interface {
	Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error)
}

type MCPClients interface {
	Client(ctx context.Context, s db.McpServer) (*http.Client, error)
}

type Builder struct{ /* store, objects, mcp */ }

func NewBuilder(s BuildStore, o Objects, m MCPClients) *Builder

// Unavailable names what is granted but cannot be bound in this slice, each
// with its reason; it becomes tool_unavailable events. Close ends the MCP sessions.
type Built struct {
	Spec        agent.Spec
	Unavailable []Unavailable
	closers     []func() error
}

type Unavailable struct{ Name, Reason string }

func (b Built) Close()

func (b *Builder) Build(ctx context.Context, run db.Run, session agent.Session) (Built, error)

// The loops the brain's Runner registers: the one list of what an agent may
// choose. A deployment adds a loop of its own here, in Go, and nowhere else.
func Patterns() []agent.Pattern // ReAct, Plan, ReWOO, Reflection(1)

// Loops is the name of every pattern in Patterns, sorted.
func Loops() []string
```

How `Build` maps a row:
- `run.AgentID == nil`, or `GetAgent` returns no row: `fmt.Errorf("the agent %q was deleted before the run started", run.AgentName)`.
- `Model{Name, MaxTokens, Temperature}`. `System` is `agent.System(instructions)` when instructions are not empty.
- `MaxSteps` and `SteerContinuations` are copied.
- `OutputSchema` becomes `{Name: "output", JSON: …}` when it is not null. `Parser` and `ParserModel` are copied.
- `Session` is the one passed in.
- `Pattern` is copied as stored. The builder does not judge it: the SDK's `Runner` refuses a name it has not registered, and lists the ones it has.
- **MCP:**
  - A command server is not bound, and goes in `Unavailable` with "a command server runs in the workspace, which slice 2 adds".
  - A URL server is bound with `mcp.Connect(ctx, mcp.Server{Name, URL, HTTPClient, Bare})`. Its tools are kept only where `allow` is null or holds the tool's unprefixed name.
  - An unreachable URL server fails the build with an error naming it.
  - `mcp.Gather` refuses a duplicate tool name.
- **Skills:** `agent.Skill{Name, Description, Body: the stored body, Resources: one per skill_files row, read through Objects.Get}`. Files are read when the model asks for them, not at build time.

- [ ] **Step 1:** Claude hands over `Client` in discovery, then `build.go`; one reply each.
- [ ] **Step 2:** Discovery tests:
  - `TestARunsMCPClientRefusesALinkLocalAddress`: the same guard as discovery.
  - `TestARunsMCPClientSendsTheServersToken`.
  - `TestARunsMCPClientHasNoDeadlineOfItsOwn`: a request after the discovery timeout still succeeds.
- [ ] **Step 3:** Build tests, with fake stores, an in-process MCP server (go-sdk on httptest), and a fake `Objects`:
  - `TestAnAgentRowBecomesTheSpecItDescribes`: every field, table-driven.
  - `TestAnAllowListBindsOnlyTheToolsItNames`.
  - `TestToolsAreThoseGrantedWhenTheRunStarts`: the grant is changed after the run is created; the build sees the new set (Review Focus 4).
  - `TestACommandServerIsReportedNotBound`.
  - `TestAnUnreachableServerFailsTheBuildNamingIt`.
  - `TestASkillsFilesAreReadOnlyWhenAsked`: `Objects.Get` is not called until the resource tool is invoked.
  - `TestLoopsNamesEveryRegisteredPatternOnce`: the names from `Patterns()`, sorted, and no duplicate.
  - `TestARunWhoseAgentIsGoneIsRefusedNamingIt`.
  - `TestBuildClosesWhatItOpenedWhenALaterServerFails`.
- [ ] **Step 4:** Break the allow-list filter, the command-server branch, and the close-on-failure path in turn. Then run the gate and commit `feat(brain): an agent row and its grants become a spec the SDK can run`.

### Task 5: The loops an agent may choose are the ones the brain runs

The SDK's `Runner` is already the list of loops: `agent.New` takes the patterns
and refuses a duplicate name, and `Run` refuses an unregistered one by listing
the names it has. The database keeps a second, hard-coded list of six,
`agents_pattern_known`. That list can disagree with the runner: it accepts
`code` and `custom`, which nothing registers, and it refuses a loop a
deployment adds in Go. The API should check against the runner's list, and the
database should only check that a name is shaped like one.

**Files:**
- Create: `internal/store/migrations/<ts>_name_loops_by_the_runner.sql`
- Modify: `internal/api/agents/{agents.go,validate.go}`
- Create: `internal/api/loops/{loops.go,schema.go}`
- Modify: `internal/store/rbac.json` (one route row) and `api/openapi.yaml` (the `pattern` description, which today wrongly says the brain runs `code` and `custom`; and the new route)
- Test: `internal/store/registry_schema_test.go`, `internal/api/agents/agents_test.go`, `internal/api/loops/loops_test.go`

**Produces:**

```sql
-- +goose Up
SET lock_timeout = '3s';
ALTER TABLE agents DROP CONSTRAINT agents_pattern_known;
-- Which loops exist is the brain's Runner's to say; the API checks it. This
-- only keeps a name a name.
ALTER TABLE agents ADD CONSTRAINT agents_pattern_named
    CHECK (pattern ~ '^[a-z][a-z0-9-]{0,63}$');

-- +goose Down
SET lock_timeout = '3s';
ALTER TABLE agents DROP CONSTRAINT agents_pattern_named;
ALTER TABLE agents ADD CONSTRAINT agents_pattern_known CHECK (pattern IN
    ('react', 'plan', 'rewoo', 'reflection', 'code', 'custom'));
```

```go
// agents
func New(logger *slog.Logger, s Store, loops []string) *api.Router
// create and update: a pattern outside loops is a 400 naming the loops there are.
// The agents_pattern_known branch of the error mapping becomes agents_pattern_named.

// loops: GET /teams/{id}/loops, scope member
func New(logger *slog.Logger, loops []string) *api.Router
// {"items": [{"name": "plan"}, {"name": "react"}, …]}, an api.Page with no next_cursor
```

The route is under a team, which is where team-written loops will list later.
`cmd/brain` passes `runtime.Loops()` to both routers (Task 10). The worker
registers `runtime.Patterns()`, so the API and the worker read one list from
one binary. During a rolling deploy, a worker one version behind may lack a new
loop; its run fails with the SDK's message, which names the loops it has.

- [ ] **Step 1:** Claude hands over the migration, then the edits to `agents`, then `loops.go` and `schema.go`, then the `rbac.json` row, then `openapi.yaml`. One per reply.
- [ ] **Step 2:** Tests:
  - `TestALoopNameMustBeWellFormed`: `"Plan!"`, `""`, and 65 characters are 23514 `agents_pattern_named`; `"critic-pass"` is inserted.
  - `TestExistingAgentsSurviveTheChange`: rows holding each of the six old names are still there after `up`.
  - `TestALoopTheBrainDoesNotRunIsRefusedListingThoseItDoes`: create and update both return 400, and the body names every loop.
  - `TestEveryLoopTheBrainRunsCanBeChosen`: table-driven over `runtime.Loops()`.
  - `TestTheLoopsRouteListsWhatTheRunnerRegisters`: equal to `runtime.Loops()`, and `items` is `[]`-shaped, never `null`.
  - `TestTheLoopsRouteIsForMembersOnly`: a non-member gets 404.
- [ ] **Step 3:** Delete the API check. `TestALoopTheBrainDoesNotRunIsRefusedListingThoseItDoes` fails: the new CHECK accepts any well-formed name, so the API is the only guard. Then run the gate and commit `feat(brain): an agent chooses from the loops the brain runs, and a deployment's own loop is one of them`.

### Task 6: Recording events

**Files:**
- Create: `internal/runtime/events.go`
- Test: `internal/runtime/events_test.go`

**Produces:**

```go
type EventStore interface {
	AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error)
	AddRunUsage(ctx context.Context, arg db.AddRunUsageParams) (int64, error)
}

const (
	maxEventText = 64 << 10
	maxTokens    = 1_000_000
)

var (
	ErrLostRun = errors.New("another worker holds this run now")
	ErrBudget  = errors.New("the run has spent its token budget")
)

// record drains events until the channel closes. It calls stop with
// ErrBudget past maxTokens, and with ErrLostRun when an append finds the
// run is no longer this worker's.
func record(ctx context.Context, s EventStore, runID uuid.UUID, worker string,
	events <-chan agent.Event, stop context.CancelCauseFunc) <-chan struct{} // closed when drained

func appendEvent(ctx context.Context, s EventStore, runID uuid.UUID, worker, kind string, payload any) error
```

Payloads. Names are snake_case; none holds a secret.

```text
step         {"step": 3}
tool_call    {"id", "name", "arguments"}
tool_result  {"id", "name", "output", "truncated", "error", "duration_ms"}
usage        {"model", "input_tokens", "output_tokens", "cached_input_tokens", "spent"}
TextEvent and SteerEvent are not stored. Token-by-token text never is.
```

- [ ] **Step 1:** Claude hands over `events.go`.
- [ ] **Step 2:** Tests, against real Postgres:
  - `TestEachSDKEventBecomesItsRow`.
  - `TestTextIsNotStored`.
  - `TestALargeToolResultIsCutAndSaysSo`: 70 KiB of output. 64 KiB is stored, cut on a rune boundary, with `truncated: true` (Review Focus 5).
  - `TestUsagePastTheBudgetStopsTheRun`: `stop` is called with `ErrBudget`.
  - `TestUsageCarriesOnFromWhatWasSpentBeforeAResume`: the spend comes from the row, not from memory.
  - `TestAnAppendForALostRunStopsWithErrLostRun`.
  - `TestTheRecorderDrainsEverythingBeforeItSaysDone`.
- [ ] **Step 3:** Break the truncation, the `>` budget comparison (to `>=` and to an always-false check), and the lost-run branch in turn. Then run the gate and commit `feat(brain): what a run does is recorded as it happens, within its token budget`.

### Task 7: Executing one run

**Files:**
- Create: `internal/runtime/execute.go`
- Test: `internal/runtime/execute_test.go`

**Produces:**

```go
type Store interface {
	EventStore
	BuildStore
	ClaimRun(ctx context.Context, arg db.ClaimRunParams) (db.ClaimRunRow, error)
	HeartbeatRun(ctx context.Context, arg db.HeartbeatRunParams) (*time.Time, error)
	FinishRun(ctx context.Context, arg db.FinishRunParams) (int64, error)
}

const (
	heartbeatEvery = 15 * time.Second
	maxWallClock   = 30 * time.Minute
)

var ErrCancelled = errors.New("the run was cancelled")

type Executor struct{ /* store, builder, runner, endpoint, sessions agent.Store, logger */ }

func NewExecutor(s Store, b *Builder, r *agent.Runner, endpoint agent.Endpoint,
	sessions agent.Store, logger *slog.Logger) *Executor

// Execute is the workflow's one step. It returns nil whenever the run reached a
// recorded end, or is not this worker's to run. It returns an error only when
// nothing could be recorded, which DBOS then reports.
func (e *Executor) Execute(ctx context.Context, runID uuid.UUID, worker string) error
```

The order inside `Execute`:

1. **Claim.** `ClaimRun(id, worker, maxWallClock)`. No row means the run was
   cancelled, finished or taken, so return nil (Review Focus 3).
2. **Bound it.** Wrap the context with `WithTimeoutCause(remaining_seconds, "wall clock")`,
   then `WithCancelCause`.
3. **Heartbeat.** Every `heartbeatEvery`, a goroutine calls `HeartbeatRun`. No
   row stops the run with `ErrLostRun`. A non-null `cancel_requested_at` stops
   it with `ErrCancelled`.
4. **Open the session.** `agent.Open(id.String(), sessions)`. If it holds
   messages, append a `resumed` event and continue from them. Otherwise start
   from `[user: input]` and append `started`, which lists the agent, model,
   pattern, tool names and skill names.
5. **Build.** On failure, finish `failed` with the build error.
6. **Report what is unbound.** One `tool_unavailable` event per item in `Unavailable`.
7. **Run.** Call `runner.Run(ctx, spec, msgs, endpoint, events)`, close
   `events`, and wait for the recorder.
8. **Decide the end.**

   | Outcome | Status, and what is stored |
   | --- | --- |
   | no error | `succeeded`, with `result.Output` |
   | cause is `ErrCancelled` | `cancelled` |
   | cause is `ErrLostRun` | **write nothing and return nil** |
   | cause is `ErrBudget` or the wall clock | `failed`, naming the limit |
   | the model endpoint failed | `failed`, naming `OPENARITY_LLM_BASE_URL` |
   | any other error | `failed`, with the error |

9. **Finish.** `FinishRun`, then a `finished` event. Use a fresh context
   bounded at 10 s, because the run's context may already be done.

- [ ] **Step 1:** Claude hands over `execute.go`.
- [ ] **Step 2:** Tests, against real Postgres and a scripted `agent.ModelClient` that returns queued responses (the shape of `patterns/react_test.go`'s `fakeModel`), injected through the runner's `ClientFactory`:
  - `TestARunThatAnswersSucceedsWithItsOutput`: status, output, a token total equal to the usage scripted, and events in order: `started`, `step`, `usage`, `finished`.
  - `TestARunThatCallsAToolRecordsTheCallAndTheResult`: the in-process MCP server from Task 4.
  - `TestAGrantedSkillCanBeLoaded`: the model calls `Skill`; the body reaches the next request.
  - `TestTheTranscriptIsSavedUnderTheRunID`.
  - `TestAResumedRunContinuesFromItsTranscript`: a transcript is pre-seeded under the run id; the first scripted request sees it, and a `resumed` event is written instead of `started`.
  - `TestACancelRequestStopsTheRunAtTheNextHeartbeat`: the heartbeat period is injectable in the test. Status becomes `cancelled`.
  - `TestADequeuedRunThatWasCancelledDoesNothing`: no event and no model call (Review Focus 3).
  - `TestAWorkerThatLostItsRunStopsAndWritesNothing`: another worker takes the row mid-run. After it, the first worker writes no event and no finish (Review Focus 2).
  - `TestARunWhoseAgentWasDeletedFailsNamingIt`: Review Focus 4.
  - `TestAnAgentWhoseLoopIsNotRegisteredFailsNamingTheLoopsThatAre`: the row is written with a well-formed name that no runner registers.
  - `TestAnUnreachableModelEndpointFailsNamingTheSetting`.
  - `TestTheWallClockIsTheOneLeftNotAFreshThirtyMinutes`: `remaining_seconds` is set near zero; the run fails on the wall clock.
- [ ] **Step 3:** Break each of these in turn and see its test fail:
  - delete the lost-run early return;
  - drop the cancel check from the heartbeat;
  - always start from the input, ignoring the transcript;
  - use `maxWallClock` instead of `remaining_seconds`.
- [ ] **Step 4:** Run the gate and commit `feat(brain): a worker runs an agent to its end, and stops when the run is no longer its own`.

### Task 8: The job, the queue and the sweep

**Files:**
- Create: `internal/runtime/job.go`
- Test: `internal/runtime/job_test.go`

**Produces:**

```go
const (
	RunWorkflow   = "openarity.runtime.run"
	RunQueue      = "openarity-runs"
	runsPerWorker = 4

	sweepWorkflow  = "openarity.runtime.sweep"
	sweepSchedule  = "runtime-sweep"
	sweepCron      = "0 * * * * *"
	unstartedAfter = 30 * time.Second
	staleAfter     = 90 * time.Second // six missed heartbeats
)

type SweepStore interface {
	ListUnstartedRuns(ctx context.Context, olderThanSeconds float64) ([]uuid.UUID, error)
	TakeOverStaleRuns(ctx context.Context, arg db.TakeOverStaleRunsParams) ([]uuid.UUID, error)
}

type Job struct{ /* executor, sweep store, logger, queue */ }

func RunJob(e *Executor, s SweepStore, logger *slog.Logger) *Job

func (j *Job) Name() string                                         // "runtime"
func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) // the queue, both workflows, the one schedule
func (j *Job) schedule() dbos.ScheduleSpec                          // split out so the spec test needs no runtime

// The run workflow takes the run id as text and calls Executor.Execute in one
// RunAsStep with the worker from dbos.GetExecutorID.
//
// The sweep:
//  1. enqueues every ListUnstartedRuns id with WithWorkflowID(id), WithQueue(RunQueue).
//     Already enqueued is a no-op (probe 1).
//  2. for every TakeOverStaleRuns id, calls dbos.ResumeWorkflow(d, id, WithResumeQueue(RunQueue)).
//     That continues the dead worker's workflow on this worker (probe 3).

type Enqueuer struct{ /* dbos.Client */ }

func NewEnqueuer(c dbos.Client) *Enqueuer
func (q *Enqueuer) Enqueue(ctx context.Context, runID uuid.UUID) error
```

- [ ] **Step 1:** Claude hands over `job.go`.
- [ ] **Step 2:** Tests. Real Postgres throughout, with DBOS under a per-test `DatabaseSchema`:
  - `TestTheSweepCronParsesWithTheParserDBOSBuilds`, and `TestAFiveFieldCronIsRefused`.
  - `TestTheSweepSpecIsWhatItClaims`: schedule name, workflow name, `AutomaticBackfill` false (a missed sweep is redone by the next one).
  - `TestAnEnqueuedRunIsExecutedWithinSeconds`: through `Enqueuer` from a separate `dbos.Client`, as the API will call it.
  - `TestEnqueuingARunTwiceRunsItOnce`.
  - `TestTheSweepStartsARunWhoseEnqueueWasLost`: the row is inserted with no enqueue, then one sweep runs.
  - `TestTheSweepResumesARunWhoseWorkerStoppedHeartbeating`. Worker A's context is cancelled mid-run and the heartbeat is aged in SQL. Worker B's sweep resumes it, and the record ends `succeeded` with one `resumed` event.
  - `TestAResumedRunAndAnUninterruptedOneLeaveTheSameRecord`: the same script, once killed and resumed, and once not. Status, output and tool calls are equal (spec, *Testing*).
  - `TestTheWorkerRunsAtMostFourRunsAtOnce`.
- [ ] **Step 3:** Break each of these in turn and see its test fail: drop `WithWorkflowID` from the sweep's enqueue, drop the takeover step, and set `runsPerWorker` to 0.
- [ ] **Step 4:** Run the gate and commit `feat(brain): runs are queued, picked up within a second, and taken over when their worker dies`.

### Task 9: The runs API

**Files:**
- Create: `internal/api/runs/runs.go`, `internal/api/runs/schema.go`, `internal/api/runs/events.go`
- Modify: `internal/store/rbac.json` (route rows), `api/openapi.yaml`
- Test: `internal/api/runs/runs_test.go`, `internal/api/runs/events_test.go`

**Produces:**

```go
type Store interface {
	GetAgentByName(ctx context.Context, arg db.GetAgentByNameParams) (db.Agent, error)
	GetSessionVisible(ctx context.Context, arg db.GetSessionVisibleParams) (db.Session, error)
	GetRunVisible(ctx context.Context, arg db.GetRunVisibleParams) (db.Run, error)
	RequestRunCancel(ctx context.Context, id uuid.UUID) (db.Run, error)
	ListRunEvents(ctx context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error)
	// One transaction: CreateDirectSession when no session was named, then CreateRun.
	StartRun(ctx context.Context, arg StartArgs) (db.Run, error)
}

type Starter interface {
	Enqueue(ctx context.Context, runID uuid.UUID) error
}

func New(logger *slog.Logger, s Store, a api.Authorizer, starter Starter) *api.Router
```

Routes and their `rbac.json` rows:

```text
POST /teams/{id}/runs                       team    run:write
GET  /teams/{id}/runs/{runID}               member
GET  /teams/{id}/runs/{runID}/events        member
POST /teams/{id}/runs/{runID}/cancel        team    run:write
```

The wire contract, in `schema.go` and `openapi.yaml`:

```json
POST body  {"agent": "triage", "input": "…", "session_id": "uuid, optional"}
run        {"id", "session_id", "agent", "status", "input", "output", "error",
            "input_tokens", "output_tokens", "created_at", "started_at", "finished_at"}
```

Handler rules:
- **Start.**
  - An empty `input` is 400, and so is one over 100 KiB.
  - An unknown `agent` gets the status that the `add-route` skill gives a body naming a missing resource.
  - A `session_id` must pass `GetSessionVisible`; otherwise 404.
  - With no session, `StartRun` creates a direct session owned by the caller.
  - After the commit, `starter.Enqueue`. If that fails, log it and still answer
    202 with the run: the sweep starts it within a minute.
- **Read and cancel.** Both go through `GetRunVisible`. `moderator` is
  `session:read_all`, exactly as `sessions` computes it. Cancel answers 202
  with the run. No row from `RequestRunCancel` after a visible read means the
  run already finished, so 409.
- **Events.**
  - The response is `text/event-stream`, resuming after `Last-Event-ID` or `?after=`.
  - Each frame is `id: <seq>`, `event: <kind>`, then `data: <payload>`.
  - Poll `ListRunEvents` every 500 ms, and send `: keep-alive` every 15 s.
  - Before each write, push the deadline forward with
    `http.NewResponseController(w).SetWriteDeadline(now + 30s)`.
  - Once the run is finished and the last event is sent, send `event: end`
    and return. Stop when the client goes away.

- [ ] **Step 1:** Claude hands over `schema.go`, then `runs.go`, then `events.go`, then the `rbac.json` rows, then `openapi.yaml`. One per reply.
- [ ] **Step 2:** The tests owed under the `add-route` skill's five, using fakes:
  - `TestStartingARunReturns202AndEnqueuesIt`.
  - `TestARunWithNoSessionGetsADirectSessionOfItsOwn`.
  - `TestARunCannotBeStartedInSomeoneElsesDirectSession`: 404, no row, no enqueue (Review Focus 1).
  - `TestAnotherPersonsDirectRunIsNotFound`: covers get, cancel and events (Review Focus 1).
  - `TestAModeratorCanReadADirectRun`.
  - `TestAFailedEnqueueStillAnswers202`.
  - `TestEmptyAndOversizedInputAreRefused`.
  - `TestAnUnknownAgentIsRefusedByName`.
  - `TestCancellingAQueuedRunNeedsNoWorker` and `TestCancellingAFinishedRunIsAConflict` (Review Focus 3).
  - `TestANonUUIDRunIDIs400`.
  - `TestTheEventStreamResumesAfterLastEventID`.
  - `TestTheEventStreamEndsAfterTheRunFinishes`.
  - `TestAnEventStreamOutlivesTheServersWriteTimeout`: through `server.New` with the real timeouts, held 35 s (Review Focus 5).
  - `TestEveryRunRouteHasAnRBACRow` and `TestTheRunRoutesMatchTheOpenAPISpec`: the existing `cmd/brain` checks, once the routes are wired.
- [ ] **Step 3:** Break each of these in turn and see its test fail: the session visibility check on start, the moderator term, and the deadline push.
- [ ] **Step 4:** Run the gate and commit `feat(brain): a run is started, read, cancelled and followed through the API`.

### Task 10: Wiring

**Files:**
- Modify: `apps/brain/go.mod`. Add `require github.com/LaplacianAI/openarity/sdk/agent` and `replace … => ../../sdk/agent`, so CI and the image build outside `go.work`.
- Modify: `cmd/brain/worker.go`
  - In `newJobs`, add `runtime.RunJob(…)` with its `Builder` and `Executor`.
  - The runner is `agent.New(openaicompat factory, runtime.Patterns()…)`.
  - `routers.go` passes `runtime.Loops()` to `agents.New` and `loops.New`.
  - The endpoint is `cfg.LLMBaseURL` with `cfg.LLMAPIKey`.
  - The session store is `postgres.New(dbStore.Pool())`.
- Modify: `cmd/brain/serve.go` and `routers.go`
  - Add a `dbos.Client` on `cfg.PostgresDSN` with `worker.AppName`, wrapped in `runtime.NewEnqueuer` and handed to `runs.New`.
  - The client is shut down after the server drains.
- Modify: `internal/store/store.go` to add `func (s *Store) Pool() *pgxpool.Pool`, for the SDK's session store.
- Test: `cmd/brain/wiring_test.go`, `cmd/brain/worker_test.go`

- [ ] **Step 1:** Claude hands over each file in the order above.
- [ ] **Step 2:** Tests:
  - `TestTheWorkerRunsTheRuntimeJob`: `newJobs` includes `runtime`, with or without `EMBEDDING_MODEL`.
  - `TestTheRunsRouterIsMounted`.
  - `TestTheAPIOffersTheLoopsTheWorkerRegisters`: `GET /teams/{id}/loops` through `newRouters` equals the names `newJobs`'s runner was built with.
  - `TestServeStartsOnADatabaseNoWorkerHasTouched`: an empty schema. The DBOS client creates its tables, and `serve` boots before any worker exists.
  - `TestServeAndAWorkerStartingTogetherDoNotFightOverDBOSsTables`: started concurrently on an empty schema; both come up.
- [ ] **Step 3:** Run the gate and commit `feat(brain): the worker runs agents and the API can start them`.

### Task 11: End to end, and the documents

**Files:**
- Test: `cmd/brain/integration_test.go` (a new test)
- Modify: `apps/brain/CLAUDE.md` (layout: `internal/runtime/`, `internal/api/runs/`, `internal/api/loops/`; a line that a deployment adds a loop in `runtime.Patterns`), `.claude/skills/add-a-background-job/SKILL.md` (a new step 7 rule: an executor id per process, never a shared `DBOS__VMID`), and the spec's status line for slice 1.

- [ ] **Step 1:** Claude writes `TestARunStartedThroughTheAPIIsExecutedAndStreamed`. It uses real Postgres, a real `serve` and `worker` in-process, a scripted model endpoint on httptest speaking `/chat/completions`, an MCP server on httptest, and a granted skill.
  - POST answers 202. The event stream shows `started`, a tool call and result, a skill load, then `finished` and `end`.
  - `GET` reads `succeeded` with the output.
  - Deleting the team leaves no row in `runs`, `run_events`, `agent_messages` or `agent_steers`.
- [ ] **Step 2:** Run it once more while killing the worker mid-run and starting a second one. The second takes over and the record matches.
- [ ] **Step 3:** Claude edits the documents and runs the gate. Commit `docs(brain): where runs live, and why every worker has its own executor id`.
- [ ] **Step 4:** Open the PR, filling every section of the template. The checklist claim that each guard was broken once must be true for every task above.

---

## Self-review

- **Spec coverage, slice 1:**

  | Requirement | Task |
  | --- | --- |
  | API start, read, events, cancel | 9 |
  | `runs` as a tree, and `run_events` | 1, 2 |
  | Transcripts in the SDK's tables, keyed by run id | 1, 7 |
  | A session created when none is named | 2, 9 |
  | `run:write`; reading through session visibility | 1, 9 |
  | The worker builds the agent with its granted MCP tools and skills | 4 |
  | The agent's loop is one the brain registers (added after review) | 4, 5, 7 |
  | `Runner.Run` | 7 |
  | Crash, resume and takeover | 7, 8 |
  | Cancel, including queued runs | 2, 7, 9 |
  | Token and wall-clock limits | 6, 7 |
  | Failure rows: endpoint down, limit hit, worker dies | 7, 8 |
  | Testing: a scripted endpoint, a resumed run equals an uninterrupted one, the wired object driven | 7, 8, 11 |

  Deliberately left out and listed above: the default entry agent, steering, `oa run`, and loops a team uploads.
- **Placeholder scan:** the one deferred value is the status code for an
  unknown agent, which the `add-route` skill decides; it is named, not left
  blank. Migration timestamps are `make migration`'s.
- **Type consistency:**
  - `ClaimRunRow.RemainingSeconds` is produced in Task 2 and consumed in Task 7.
  - `Built.Unavailable` is produced in Task 4 and becomes `tool_unavailable` events in Task 7, a kind the CHECK in Task 1 allows.
  - `ErrLostRun` and `ErrBudget` (Task 6) and `ErrCancelled` (Task 7) are decided on in Task 7's table.
  - `RunQueue` and `RunWorkflow` (Task 8) are used by `Enqueuer` (Task 8) and the wiring (Task 10).
  - `worker.AppName` (Task 3) is used in Task 10.
  - `runtime.Patterns` and `runtime.Loops` (Task 4) feed the runner (Task 10), `agents.New` and `loops.New` (Task 5, wired in Task 10).
- **Review Focus:** each of the five lines names its test, and that test sits in the owning task's step list.
