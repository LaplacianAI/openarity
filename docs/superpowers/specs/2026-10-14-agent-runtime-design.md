# The agent runtime: runs, the graph tool and delegation

**Status:** overall design, not built. Each slice below gets its own spec and
plan; this document is what keeps them from painting each other into a corner.
**Modules:** `apps/brain`; `apps/sandbox` from slice 2; `sdk/agent` for two slices

## The problem

The brain stores what an agent is made of and projects it into a graph. Nothing
runs. No code calls a model, no table records a run, and the graph has a write
side and no read side.

This design covers running an agent, and what makes Openarity's runtime its
own: an agent finds other agents, tools and skills by searching the team's
graph, and hands work to another agent the way Claude Code hands work to a
subagent.

## Decisions

Each was put to the user as a choice. The rejected column is what was offered
and not taken.

| Question | Decided | Not taken |
| --- | --- | --- |
| How much delegation | nested, with background delegates and resume by id, as Claude Code has | one level in turn; one level in parallel |
| Who decides what runs next | the agent holding the run, turn by turn | a planner writing a fixed plan up front |
| Which agents an agent may reach | its whole team by default; an allow-list narrows it; it can be set to none | explicit grants only; whole team with no narrowing |
| Where a run starts | the caller names an agent, or the team's default entry agent is used | always the one orchestrator; always named |
| Where delegation lives | two tools the brain adds to an agent, with their functions in the brain | inside the SDK behind `AgentResolver`; the planner |
| How selection works | one `graph` tool the agent drives: cosine similarity over stored embeddings, then Cypher hops | three tools, each a fixed pipeline the brain drives |
| Who writes the Cypher | the brain, from the agent's parameters | the agent, as Text2Cypher does |
| Where compaction lives | in the SDK, a step beside the one that saves the transcript | in the brain, rewriting history between turns |
| Who writes the summary | the agent's own model through its own endpoint, no tools, a fixed prompt the agent can extend | a separate cheaper model; a compaction agent in the registry |
| What a tool is | three kinds: MCP, custom (a script the team adds), built-in (`Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`); the runner sees one `agent.Tool` for each | MCP only, with team code wrapped as an MCP server |
| Where code runs | one container per session, in a separate sandbox service the brain calls over HTTP | the brain driving Docker itself; a hosted sandbox |
| Build order | seven slices: a run, the workspace, compaction, the graph tool, delegation, loading, background | delegation first; one spec and one plan; the workspace last |

## What this changes in the HLD

The HLD's companion documents decided several of these the other way for V1.
They are not wrong; this is a change of scope, made knowingly.

- **`Scope-V1.md` defers multi-agent delegation and agentic graph RAG.** Both
  start here. Its reason for deferring delegation — that authorisation,
  cross-agent context and partial failure all arrive together — is why
  delegation is slice 5 and not slice 1, and why each of the three has a
  section below.
- **`Planner.md`'s fixed plan stays deferred, and is not what runs first.** A
  run is one agent deciding turn by turn. Nothing here prevents a planner
  being added later as an agent pattern.
- **`Selection.md`'s `search_tools` becomes the `graph` tool.** Its rules are
  kept and extended to agents and skills: scope is applied before the search
  and never as a filter afterwards; no second model call chooses what the
  model is shown; every result carries a reason and a score; every selection
  is recorded on the run.
- **`Agent-SDK.md`'s `AgentResolver` is not built.** Delegation is the brain's.
  If a second consumer of the SDK ever needs it, a working `delegate` moves
  into the SDK then.
- **`Runtime.md` is unchanged and relied on**: a step never receives the
  previous step's transcript, the run is the unit of audit, and the session is
  the unit of memory.

## What was read first

### How Claude Code maintains agents and delegates

From its published documentation and from the interface it gives a model, not
from its code.

- **An agent is a small record**: a name, a description saying when to use it,
  a system prompt, a tool allow-list and deny-list, a model, a turn limit.
- **The orchestrator is shown every agent's description** and chooses by
  reading them. There is no search, embedding or ranking step. The only limit
  is a warning when the descriptions together pass 15,000 tokens.
- **Delegating is one tool call** carrying an agent type and a prompt. The
  prompt is the only thing the parent passes.
- **The delegate starts from nothing**: its own system prompt and tools, none
  of the parent's history.
- **Only the final report returns.** The parent never sees the delegate's tool
  calls, and the report is treated as untrusted model output: control tags in
  it are neutralised before the parent reads it.
- **Each delegate's transcript is kept separately** and can be continued by
  id. Delegates run in parallel up to a cap of 20 and nest three levels deep.
  A delegate cannot ask the user a question.

What carries over is everything but the second point. Listing every
description works for tens of agents on one person's machine, where nothing
needs authorising. A team here can hold hundreds, and `Selection.md` already
cites selection accuracy falling past 30 to 50 options. The graph replaces the
list.

### How Claude Code compacts

From its documentation (`how-claude-code-works`, `context-window`,
`model-config`, `sub-agents`, `hooks`), not its code.

- **It triggers on tokens, not turns.** It compacts as the context nears the
  model's limit, a window the user can set (`autoCompactWindow`). A turn limit
  is a separate guard: a subagent that hits `maxTurns` is marked partial and
  can be resumed.
- **It clears before it summarises.** Older tool outputs go first; the
  conversation is summarised only if that is not enough.
- **The summary keeps** the requests and intent, key concepts, files and
  important snippets, errors and their fixes, pending tasks and the current
  work. Full tool outputs and intermediate reasoning are gone.
- **Some things are put back afterwards**: the most recently modified files are
  re-read, invoked skills re-injected (up to 5,000 tokens each), and the
  project instructions reloaded. Instructions given only in conversation can be
  lost, which is why it tells users to keep standing rules in a file.
- **It can be steered**: a "Compact Instructions" section in the project file,
  or a focus given with `/compact`.
- **The transcript is kept.** A `compact_boundary` entry, with what triggered it
  and the token count before, marks where the summary takes over.
- **Each subagent compacts in its own transcript.** The parent compacting does
  not touch a subagent's.
- **It refuses to thrash.** If one output refills the context after a few
  attempts, it stops with an error instead of looping.

The Anthropic API also compacts on the server (`compaction: {type:
"summarize"}`, returning a signed block that replaces the messages it
summarises). It is not used here: the SDK reaches models through an
OpenAI-compatible endpoint, and a signed block does not survive a gateway.

### Agentic GraphRAG

The term is not standardised and most descriptions are vendor posts. The
common pattern: an agent is given graph retrieval as tools — search by
meaning, walking to a node's neighbours, sometimes a model-written query — and
decides which to call, in what order, and when to stop.

Two papers apply it to this problem. *Agent-as-a-Graph* puts tools and their
parent agents in one knowledge graph, searches both by vector, re-ranks with a
weight per node type, and walks from matching tools to their agents; it
reports 14.9% better Recall@5 than earlier retrievers on LiveMCPBenchmark.
*Graph RAG-Tool Fusion* searches by vector and then walks tool-to-tool
dependencies. Both sets of numbers are the authors' own evaluations.

## The pieces

| Piece | Job |
| --- | --- |
| `internal/api/runs` | Start a run, read it, stream its events, cancel it. Checks permission and resolves the entry agent. |
| `internal/runtime` | Turns an agent row into an `agent.Spec`, supplies the `graph` and `delegate` tools, and holds the worker job that executes runs. |
| `internal/graph` | Gains its read side: cosine similarity and hops over a team's graph. |
| Postgres | `runs`, a tree; `run_events`; delegation settings on agents. Transcripts in the SDK's session tables. |
| `apps/sandbox` | Slice 2. Holds the container runtime; runs a session's workspace and answers exec, read and write over HTTP. |
| `sdk/agent` | Gains compaction in slice 3, and in slice 6 a tool set that can grow during a run. |

One run, with one delegation:

```text
oa run "triage the new bug reports"
  │
  ▼
POST /teams/{id}/runs            api/runs      permission checked, entry agent resolved,
  │                                            runs row written as queued
  ▼
brain worker                     runtime       picks the run up
  │  build the agent                           instructions, model, granted MCP tools, skills,
  │                                            plus graph and delegate if it may delegate
  ▼
sdk Runner.Run                                 the model and tool loop
  │
  ├─ graph(query: "label github issues", kinds: [agents], hops: 1)
  │      cosine similarity, then one hop, inside what this agent may reach
  │      → triage  0.81  triage -HAS_SKILL-> issue-triage
  │
  ├─ delegate(agent: "triage", brief: "Label the 14 open unlabelled issues …")
  │      checks target, reach, cycle, depth, budget
  │      child runs row (parent = this run)
  │      build triage the same way, run it, return its report
  │
  ▼
final answer                     runs row: succeeded, with the output
  │
  ▼
GET /teams/{id}/runs/{run}/events              the CLI streams progress as it happens
```

Three properties follow from the shape:

- **The API never runs a model.** It records the run and returns. No HTTP
  request is held open for a run.
- **A delegate is a run like any other.** Same table, same builder, same
  recording. It differs only in having a parent.
- **The graph proposes and Postgres decides.** The `graph` tool returns
  candidates. `delegate` checks its target against Postgres before anything
  runs, so a stale or wrong graph cannot widen what an agent may use.

## What is stored

### `runs`

One row per run. Delegation makes it a tree.

```text
runs
  id, team_id, session_id        every run belongs to a session
  parent_run_id, root_run_id     null parent = the run a person started
  parent_tool_call               which tool call in the parent created this run
  depth                          0 for that run, +1 per delegation
  agent_id, agent_name           the name is kept so the record outlives the agent
  user_id                        who asked; copied down the tree
  status                         queued · running · succeeded · failed · cancelled
  input                          the request, or for a delegate, its brief
  output, error                  the answer, or for a delegate, its report
  input_tokens, output_tokens    summed over the tree for the budget
  worker, heartbeat_at           which worker holds a running run
  cancel_requested_at            set by the API; the worker holding the run acts on it
  created_at, started_at, finished_at
```

- A run started through the API with no session gets a new direct session
  owned by the caller.
- `(parent_run_id, parent_tool_call)` is unique. A parent that resumes after a
  crash and makes the same `delegate` call again finds the child that already
  exists and gets its report; it does not start a second one.
- `user_id` is on every row, so "who asked for this" never needs a walk to the
  root.

### `run_events`

Append-only: a run, a sequence number, a kind and a JSON payload. It holds
steps, tool calls, tool results, every delegation, and every `graph` call with
what came back — each result's score and the path that led to it — and what
the agent then selected. That last record is the evaluation set
`Selection.md` asks for. Token-by-token text is not stored. The event stream
reads this table.

### Transcripts

In the SDK's own session tables, keyed by run id. A delegate having its own is
what gives it a fresh context and makes it resumable. This is the store the
durable-sessions design ships; the brain wraps it and implements nothing.

### Delegation settings

```text
agents.delegation       'team' (default) · 'listed' · 'none'
agent_delegates         agent → delegate, read when the setting is 'listed'
```

The pair is same-team by composite foreign key, as the MCP grants are, and an
agent cannot list itself. Listed pairs are projected into the graph as a
`DELEGATES_TO` edge through the existing outbox, so the Graph page shows them.

### The default entry agent

A flag on the agent, with a partial unique index allowing at most one per
team. Deleting that agent clears the default with no reference to maintain
from `teams`.

### Permissions

Data in `rbac.json`, as every permission is.

- Starting, steering or cancelling a run needs a new `run:write`.
- Reading a run uses its session's existing visibility check, the way
  attachments do. There is no `run:read`: it could be granted to a role that
  cannot open the conversation the run is in.

## Tools and the workspace

An agent's tools come in three kinds. The runner sees no difference: each is an
`agent.Tool`, a name, a schema and a function.

| Kind | What the team adds | Where it runs |
| --- | --- | --- |
| MCP | a server, as today | on the server; the brain calls it |
| Custom | a name, a description, an input schema, and a script with the files beside it | in the session's workspace: the arguments as JSON on stdin, stdout is the result |
| Built-in | nothing; each is granted to an agent by name | `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`, in the session's workspace |

Skills do not change. A granted skill's files are copied into the workspace, so
a skill whose instructions say "run `scripts/build.py`" is followed with `Bash`,
as Claude Code follows it. An agent writes and runs its own scripts the same
way: `Write`, then `Bash`.

```text
run ──► sdk Runner ──► Bash("python /skills/report/scripts/build.py")
                            │  HTTP
                            ▼
                   apps/sandbox ──► the session's container
                                      /workspace     what the agent writes
                                      /skills/<name> granted skills' files
                                      /tools/<name>  custom tools' scripts
```

- **One workspace per session, not per run.** A later run in the same session
  finds what an earlier one left, and a delegate works in its parent's. This is
  `Runtime.md`'s rule that the session is the unit of memory.
- **A container.** Teams need native Python packages, which rules out WASM.
  gVisor's `runsc` is used where the host has it.
- **In its own service.** `apps/sandbox` alone holds the container runtime; the
  brain reaches it over HTTP. Docker's socket is root on the host, and a
  process that can be talked into running code must not hold it. This answers
  `Agent-SDK.md`'s open question of where the sandbox lives.
- **Closed by default.** No network unless allowed, and a secret a custom tool
  needs is injected for that one call, never written to the workspace.
- **Checked like the rest.** A built-in or custom tool is bound only when it is
  granted to the agent, read from Postgres when the run starts.

Slice 2 gets its own spec, which settles what this section leaves open: how a
built-in is granted, where a custom tool's script is stored, the network
allow-list, and when an idle container stops and what happens to its files.

## The `graph` tool

```text
graph(query, kinds, hops)

  query   the agent's own words; embedded, then matched by cosine similarity
  kinds   what to return: agents, tools, skills
  hops    how far to walk from the matches: 0 to 3
```

Every agent, tool and skill node already carries an embedding, and each label
has a cosine vector index. One call does both halves:

```cypher
// cosine similarity: the skills nearest the query
CALL db.idx.vector.queryNodes('Skill', 'embedding', 5, vecf32($query)) YIELD node AS s, score
// one hop: the agents that hold them, inside what this agent may reach
MATCH (a:Agent)-[:HAS_SKILL]->(s)
WHERE a.id IN $reach
RETURN a.name, s.name, score ORDER BY score LIMIT 5
```

The same shape with two hops finds an agent through a tool:
`(a:Agent)-[:GRANTED]->(:Toolkit)-[:CONTAINS]->(t:Tool)`.

- **The query is matched against all three kinds of node.** `kinds` says which
  to return, so with hops a matching skill or tool can lead to an agent.
- **`hops: 0`** returns what matches the query directly.
- **`hops: 1` and up** also returns what the matches are joined to, each with
  the path that led there.
- **The score is a similarity.** FalkorDB's vector query returns a cosine
  distance, where smaller is nearer; the tool reports one minus that, so a
  higher score is a better match and a result reached by a hop carries the
  score of the match it was reached from.
- **The agent chooses `kinds` and `hops`**, reads the results, and may call
  again with other words. That choice is what makes this agentic; no second
  model decides for it.

What does not vary:

- **The brain writes the Cypher.** The agent's parameters select among a fixed
  set of parameterised queries. A model writing its own query would be
  deciding what it may see, and a mistake in its input should be a refused
  parameter, not a query that fails at run time.
- **Scope is inside the query.** The allowed set is read from Postgres and
  passed in, so the cut to a handful of results is never spent on something
  the agent cannot use. What may be returned, by kind:

  | Kind | May be returned |
  | --- | --- |
  | agents | those the caller's delegation setting reaches |
  | tools | MCP tools on servers granted to the caller, honouring a grant's allow-list; custom and built-in tools granted to the caller |
  | skills | those granted to the caller |

- **A grant's allow-list is honoured on the hop.** A `GRANTED` edge may carry
  `only`; a walk from a tool to the agents holding it must not report an agent
  granted the server without that tool.
- **Whatever is selected is checked again** against Postgres when it is used.

### Selecting

| Kind | Action | Slice |
| --- | --- | --- |
| an agent | `delegate(agent, brief)` | 5 |
| a tool | load it; its definition joins the run's tool set | 6 |
| a skill | load it, as the SDK's skill tool does today | 6 |

Until slice 6 a run binds all of its agent's granted tools and skills at the
start, as the SDK does now. The `graph` tool still returns tools and skills
before then, because what an agent holds is how another agent chooses it.

## `delegate`

```text
delegate(agent, brief)
```

### Checks

Cheapest first. A failure is returned to the calling agent as the tool's
result, so it can choose another; it does not fail the run.

| Check | Refused when |
| --- | --- |
| Target | no agent of that name in the team |
| Reach | the target is outside the caller's setting, or is the caller |
| Cycle | the target is already above the caller in this run's chain |
| Depth | the child would be deeper than the cap |
| Budget | the tree has spent its tokens, its wall-clock time, or its count of runs |

An agent set to `none` is not given the tool, and neither is one already at
the depth cap.

### What the delegate starts with

- Its own instructions, model, granted tools and skills.
- The brief, and nothing else from the parent.
- The user of the run that started the tree.
- The `graph` and `delegate` tools, if its own setting and depth allow.

The tool's description says what Claude Code's says: the delegate knows only
what is in the brief.

### What comes back

- **The final answer only**, as the tool's result.
- **As data.** The report is wrapped and marked as another agent's output, and
  anything in it shaped like an instruction to the system is neutralised. A
  delegate that read a hostile page must not be able to steer its parent
  through its report.
- **Capped.** Past the cap it is cut and says so, with the run id where the
  rest is stored.
- **Honest about failure.** A delegate that failed, was cancelled or hit a
  limit says which, with what it produced. The parent carries on.
- **Usage rolls up** into the tree's totals, which the budget check reads.

### What a delegate cannot do

- **Ask the person a question.** Only the run a person started can.
- **Outlive a cancel.** Cancelling a run cancels everything beneath it.

## Execution

**The API records; the worker runs.** `POST /teams/{id}/runs` writes the run
as `queued` and returns. `brain worker` starts a DBOS workflow from that row
with the run id as the workflow id. The run row is the durable fact, which
avoids depending on something `Background-Work.md` found DBOS does not offer:
enqueueing inside a transaction the caller already holds.

**After a crash a run continues.** The SDK saves the transcript before every
model request. A restarted workflow reads it and carries on, so what is lost
is at most the request in flight, and a tool call in flight may repeat — the
cost the durable-sessions design already states.

**Delegates run in one of two ways.**

| | Foreground, slice 5 | Background, slice 7 |
| --- | --- | --- |
| Where it runs | inside the parent's tool call, same worker | its own queued run, any worker |
| Parent | waits for the report | gets the child's run id at once and continues |
| Report arrives | as the tool's result | as a message into the parent's session, seen at its next model request |

Background delivery uses the SDK's steering across processes. If the parent
has already finished, the report stays on the child run.

**Resume.** A finished delegate can be continued by its parent with a new
brief and its transcript intact. Only within the same tree.

**Cancel.** The API sets `cancel_requested_at`. The worker holding the run
stops it and records it as `cancelled`, and foreground delegates stop with it;
background delegates in the tree are marked the same way. `worker` and
`heartbeat_at` are what let a run whose worker died be picked up by another.

**Events.** The worker appends to `run_events`. The API streams from the
table.

## Compaction

A run that outgrows its model's context window would fail on its next request.
Compaction lets it carry on: the oldest part of the transcript is replaced by
a summary, and the run continues from there. This is the SDK's, so every
consumer gets it; the brain turns it on and records it.

### When

Before each model request, the SDK estimates the request's size from the last
response's token usage plus what has been added since. Past the agent's
threshold, it compacts first. The threshold is tokens, below the model's
window, with room left for the reply and for the summary call itself. Turns are
not the trigger: a turn can be ten tokens or a hundred thousand.

### How, cheapest first

1. **Clear old tool results.** A result older than the last few turns is
   replaced by a short stub naming the tool and the `run_events` entry that
   holds it in full. Tool calls are kept, so the history still makes sense to
   the model and to the API. If that brings the request under the threshold,
   nothing is summarised.
2. **Summarise.** One call to the agent's own model, through its own endpoint,
   with no tools. Everything but the last few turns is summarised; those stay
   word for word. The prompt is fixed, and an agent can add to it, as Claude
   Code's Compact Instructions do.

The summary comes back in two parts, because `Runtime.md` decided that
references survive and summaries do not:

```text
summary     what was asked, what has been decided, what was done, what failed
            and why, what is still to do, what the agent is doing now
references  every identifier the run has touched: issue numbers, URLs,
            run ids, file paths, record ids
```

### What is kept

- **The system prompt and tool definitions are never summarised.** They are
  sent fresh on every request, as they are today.
- **Skills loaded during the run are loaded again** after the summary, so a
  skill's instructions are not reduced to a paraphrase.
- **Nothing is deleted.** The session store gets a compaction entry, a
  boundary like Claude Code's: loading the session returns the summary, the
  references, and every message after the boundary. The messages before it stay
  in the store, so the run remains a complete record for audit.
- **`run_events` records it**: a `compacted` event with what triggered it, the
  token count before and after, and whether a summary was needed.

### Around it

- **A delegate compacts its own transcript.** A parent never compacts a
  child's, and a child's report reaches its parent already short.
- **Compacting again** summarises the previous summary and what came after it;
  the references are merged, not summarised.
- **Crash safety.** The compaction entry is written in the same save as the
  transcript, so a run that dies mid-compaction resumes either before it or
  after it, never half-way.
- **No thrashing.** If compaction cannot bring the request under the threshold,
  because a single recent result is larger than the room left, the run fails
  and says which result. It does not compact again in a loop.
- **The summary call counts.** Its tokens join the run's and the tree's totals,
  and the budget check reads them.

## Limits

Constants in code to start with, as the server timeouts are, and promoted to
configuration only when a deployment needs a different value.

| Limit | Start |
| --- | --- |
| Delegation depth | 3 |
| Compaction threshold | a fraction of the agent's context window; to be set in slice 3 |
| Turns kept word for word | to be set in slice 3 |
| CPU, memory, disk and time per workspace call | to be set in slice 2 |
| Runs in one tree | to be set in slice 5 |
| Background delegates at once, per tree | to be set in slice 7 |
| Tokens per tree | to be set in slice 1 |
| Wall-clock per tree | to be set in slice 1 |

The budget is counted in tokens and time, not currency. Nothing in the brain
knows prices.

## Failure

| When | Then |
| --- | --- |
| The model endpoint is down | the run fails and names the setting it tried to reach |
| A delegate fails | its report says so; the parent continues |
| The graph is down or behind | the `graph` tool says it is unavailable; delegating by name still works |
| A limit is hit | refused as a tool result, recorded as an event |
| A worker dies | the run resumes from its transcript on another worker |
| The context window fills | the run compacts and continues; if compaction cannot make room, it fails and names the result that is too large |
| The summary call fails | the run carries on uncompacted if there is room, and fails naming the model setting if there is not |

## Testing

- Real Postgres and real FalkorDB, as the graph tests run today.
- A scripted stand-in for the model endpoint. The SDK's examples have one.
- One test per check in `delegate`, and per scope rule in `graph`, each guard
  broken to confirm a test fails without it.
- A resumed run and an uninterrupted one produce the same record.
- The wired object is driven, not only its parts: a `graph` or `delegate` tool
  that is built and never added to a spec passes every unit test.
- Compaction against the scripted endpoint: a run that crosses its threshold
  clears before it summarises, keeps the last turns and every reference word for
  word, and a run killed mid-compaction resumes to the same record as one that
  was not. A result too large to make room for fails the run instead of looping.

## The slices

Each works end to end before anything built on it begins.

```text
1. A run          start one through the API, one agent executes it with its
                  granted MCP tools and skills, events stream, the run is recorded
2. The workspace  a container per session in apps/sandbox; the built-ins, custom
                  script tools, and skill scripts run through Bash
3. Compaction     a run that outgrows its context window carries on; in the SDK
4. The graph tool cosine similarity and hops over agents, tools and skills
5. Delegation     the delegate tool, its checks, the report, nesting
6. Loading        selecting a tool or a skill mid-run; the SDK's growable tool set
7. Background     delegates that do not block, resume by id, cancel across workers
```

Slice 1 is first because nothing in the brain calls a model or records a run,
and every later slice sits on both.

Slice 2 is second because a skill that ships scripts, a custom tool and an
agent that writes its own code all need somewhere to run, and every later slice
assumes an agent can do what Claude Code's can.

Slice 3 is SDK work and touches neither the graph nor delegation, so it can be
built alongside slices 1 and 2; the brain's part is a setting and an event. It comes
before delegation because a long run is what an orchestrator is, and an
orchestrator that fails at its context window cannot hand anything on.

### To probe at the start of slice 1

`Background-Work.md` lists the first and last of these as unvalidated.

1. That DBOS treats a repeated workflow id as the same workflow.
2. How the worker is woken promptly for a new run without writing a schedule
   row every second. The graph drain moved to once a minute because DBOS keeps
   a row per tick.
3. How DBOS recovery behaves with two worker processes.
4. That DBOS can share the brain's connection pool.

## Open

1. How `runs.agent_id` behaves when its agent is deleted. The brain's Postgres
   floor is 13, which rules out a column-level `SET NULL` on a composite key.
2. The values of the limits marked "to be set".
3. Whether skills are listed up to a threshold and searched above it, and
   where the threshold sits.
4. How a channel names its entry agent. Channels start runs after slice 1.
5. Where an agent's additions to the summary prompt are kept: a column on
   `agents`, or a section of its instructions that compaction reads.
6. How the size of a request is estimated when an endpoint reports no token
   usage. Most OpenAI-compatible gateways report it; a few do not.
7. **The vector search's cut comes before the scope.** In the Cypher under "The
   `graph` tool", `queryNodes(…, 5, …)` takes the five nearest skills in the
   whole team before `WHERE a.id IN $reach` runs, so the cut can be spent on
   what the agent cannot reach — what "Scope is inside the query" says must
   not happen. Either fetch more than is returned and cut after the scope, or
   filter inside the vector query if FalkorDB allows it.
8. **What `root_run_id` holds on the root run**: its own id, or null. Its own id
   makes "every run in this tree" one equality; null matches `parent_run_id`.

## Not in scope

- **Session memory across runs**: the rolling summary and references a
  *session* carries from one run to the next. Compaction is within one run's
  transcript; the two share the summary-and-references shape, and session
  memory needs a design of its own.

- **The planner**, branches and loops. `Planner.md` stands as written.
- **Learnings.** The registry design gives their shape; they need runs to
  learn from, which this provides, and a design of their own.
- **Large tool results going to the workspace.** `Runtime.md` describes it;
  slice 2 builds the workspace, not that.
- **Channels starting runs**, and replies going back out through a channel.
- **A delegate asking the person a question.**
- **Agent teams and scripted workflows**, the other two ways Claude Code
  orchestrates.

## Sources

- Claude Code documentation on compaction:
  [how Claude Code works](https://code.claude.com/docs/en/how-claude-code-works#when-context-fills-up),
  [the context window](https://code.claude.com/docs/en/context-window),
  [model configuration](https://code.claude.com/docs/en/model-config#set-the-auto-compact-window),
  [hooks](https://code.claude.com/docs/en/hooks)
- [Compaction on demand, Claude API](https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand)

- Claude Code documentation: [subagents](https://code.claude.com/docs/en/sub-agents),
  [Agent SDK subagents](https://code.claude.com/docs/en/agent-sdk/subagents),
  [agent teams](https://code.claude.com/docs/en/agent-teams),
  [workflows](https://code.claude.com/docs/en/workflows),
  [tools reference](https://code.claude.com/docs/en/tools-reference)
- [Agent-as-a-Graph: Knowledge Graph-Based Tool and Agent Retrieval for LLM Multi-Agent Systems](https://arxiv.org/abs/2511.18194)
- [Graph RAG-Tool Fusion](https://arxiv.org/abs/2502.07223)
- [Introducing ToolsRetriever in the Neo4j GraphRAG Python package](https://neo4j.com/blog/developer/introducing-toolsretriever-graphrag-python-package/)
- [GraphRAG and agentic architecture with NeoConverse](https://neo4j.com/blog/developer/graphrag-and-agentic-architecture-with-neoconverse/)
- [Anthropic, Advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use), as cited in `Selection.md`
