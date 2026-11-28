# brain — decisions worth not relitigating

Read on demand from [AGENTS.md](AGENTS.md): before changing a design, a boundary, or anything a convention there protects.

## Decisions worth not relitigating

- **A person's schedules are our rows, fired by one DBOS tick a minute.**
  DBOS's own schedules keep every one in each worker's memory, re-listed every
  30 seconds, and backfill every missed time without a cap; a sleeping
  workflow per schedule holds a goroutine and cannot be woken early. So the
  tick reads `schedules` through an index on what is due, and claims each with
  `SKIP LOCKED`. What follows (design: schedules and watchers, approved
  2026-11-26):
  - **A time fires once.** `schedule_fires` is keyed on the schedule and the
    time it was due, and its row is written before the run: a second worker, a
    retry or a restart finds it and starts nothing. The fire also waits on the
    team's advisory lock and skips while the schedule's last run works, so the
    key is the last of three guards, not the only one.
  - **Missed times collapse.** Within half the period (2 minutes to 2 hours) a
    fire runs late; beyond, or with several due, one catch-up stands for them
    and the rest are recorded missed. A one-shot runs late for up to a day.
  - **A schedule answers in a conversation of its own,** as its maker, with
    their permissions when it fires. Nobody writes in it, through a message or
    a run; a second writer would make its next fire skip.
  - **A fired run asks and waits like any run.** It is not `unattended`: its
    approvals reach Tool approvals, and a wait that expires ends it as a
    failure (*user*, 2026-11-26).
  - **Deleting one is soft.** Its conversation is closed and kept with its
    runs; nothing in the brain deletes a conversation or a run (*user*,
    2026-11-26).
  - **`[silent]` as a whole answer sends nothing** and tells nobody; five
    failures in a row pause a schedule. A run's end is counted once, however
    often it is reported; a cancelled run is neither a failure nor news.
  - **A fire that errors is put off, not retried at once.** Its transaction
    rolls back, so it would still be the oldest due and be claimed first on
    every tick, ahead of every other schedule. `retry_at` holds it back a
    minute, in a transaction of its own, and keeps `next_fire_at`, so the
    retry is for the time it was due and its lateness is judged by the same
    grace. An error of its own counts as a failure, so the fifth in a row
    pauses it as any failing schedule; the database's passing trouble
    (SQLSTATE classes 08, 40, 53 and 57, `55P03`, a lost connection) does
    not, until the schedule has been stuck on it for an hour. That needs no
    column of its own: `next_fire_at` stays on the time it was due while it
    retries, so its age is how long it has been stuck. A pause clears the
    retry with it.
  - **A time the clocks skip does not fire that day.** On the morning they go
    forward, a rule's 02:30 does not exist on the wall, so nothing fires and
    nothing is missed; firing at the next valid minute instead is a product
    choice not made yet.
  - **A wall time fires once at fall-back only for a rule that pins hours,**
    and the test is that the local time moved forward, not that it differs
    from the last: `0,30 1 * * *` reads 01:00, 01:30, 01:00, 01:30 and fires
    two of them.
  - **What a schedule does is its maker's alone.** It runs as them, with their
    permissions, so only they change its name, objective, rule or agent, or
    run it now. `schedule:manage` sees, pauses, resumes, deletes and takes
    over everyone's; whoever makes or takes over one must be able to start
    runs.
  - **The hub is a choice, not the absence of an agent.** `hub` is stored; an
    agent deleted sets `agent_id` null and leaves `hub` false, and the
    schedule pauses as `target_gone` rather than fall to the hub nobody
    chose.
  - **A schedule may answer in a chat, a channel or its maker's direct
    messages** (`schedules.target`). Its own session takes that chat's
    channel and ref, outside `sessions_open_chat_key`, so a person writing
    there is still in their own conversation; the old one-open-session index
    is dropped for that. A place is checked again when the schedule is made,
    and looked up on its platform before the transaction; how it was shown is
    kept as `target_label`, as a channel's name is the platform's. Posting at
    a channel's top level, or in a group's chat, takes `channel:write`, when
    the schedule is made and again at each fire: a maker who lost it pauses
    the schedule as one who may no longer run does (`maker_cannot_run`). A
    bot removed from the channel is not looked up at fire; the platform
    refuses the post (*user*, 2026-11-26). Without `channel:write` the picker
    leaves private channels out: their names are not for one who may not post
    there.
  - **Take-over hands on a channel or group schedule, not a person's own
    chat.** One answering in its maker's direct messages or their own thread
    is refused, as the new owner's runs would land in the old maker's
    conversation; one posting where a group reads it goes to an admin who
    holds `channel:write` (*user*, 2026-11-26). A maker who lost `channel:write` cannot
    resume such a schedule either, and one they paused themselves before
    losing it may be taken over too, or nobody could ever resume it.
  - **Dropping the old open-conversation index is a deploy gate.** A brain
    older than #306 names it in `EnsureSession`, so a rollback past #306 must
    migrate down past `20261211090200` first.
  - **The pause notice in a chat is not built yet.** The `notices` outbox is
    for senders waiting to be let in, and a reply needs a run; a pause has
    neither. The maker hears of a pause through news until one exists.
  - **The rules are written once,** in `internal/triggers`, below the routes,
    for the API, an agent's tools and the jobs. A schedule and a watcher are
    each a `trigger` to the rules they share (whether its maker may still
    post where it answers, whether they are stuck, how a proposal is
    confirmed), so the two cannot drift.
- **A watcher polls a page or a feed on the same minute tick, and runs when
  what it sees changes** (design: schedules and watchers, D12, D13, D17).
  - **Watchers of one address share one fetch.** The address is normalised
    (scheme and host lowercased, the default port and the fragment dropped,
    the query kept) and is one `watch_targets` row; each watcher keeps its own
    selector and what it last saw (`watchers.state`). The tick counts the due
    targets and hands out batches of fifty, at most eight, onto
    `openarity-watch`; a batch claims its targets with `SKIP LOCKED` and moves
    `next_check_at` ten minutes on as a lease, so a check that crashed is
    tried again then.
  - **A fetch is compared by every active watcher of its address,** due or
    not: it moves the address's validators for all of them, so one not yet due
    that skipped a change would get a 304 at its own time and never see it. A
    watcher not due is told only of a change and keeps its own time; nothing is
    written for it otherwise. When one watcher cannot take in what was read,
    the validators are not kept, so its next look reads the address whole; a
    watcher resumed forgets them too, as others' fetches may have moved them
    past a change it never saw.
  - **One host at a time, and a host that asks us to wait is waited for.** A
    batch takes a host in `watch_hosts` for two minutes, renews the hold
    before each fetch and lets it go only if it is still its own, and fetches
    at most ten of its targets one after another (the rest a minute later),
    eight hosts at once. A batch that lost its hold leaves the rest to the
    batch that has it. A redirect to another host is fetched under the first
    host's hold, and a 429 from it backs off the first host. A 429 or 503 leaves the
    host alone for its `Retry-After`, five minutes if it gives none, six
    hours at most. Each fetch is one conditional GET through web fetch's
    public-address-only client, except when a watcher has seen nothing yet:
    a 304 would leave it with no baseline. An answer over 2 MiB is an error,
    never a change judged on part of it. Why an address could not be
    reached is said on the check in a fixed sentence; the dialer's own words,
    which name the address it refused, go to the log only.
  - **The first look is a baseline and starts nothing.** A page's text (of
    the selector's matches, else the body without scripts and styles) is
    compared line by line; a feed's new items by id. Readability is not used:
    its choice of article can change when the content has not. A new
    selector makes the next look a baseline again.
  - **A change is never lost.** Within the cooldown (an hour by default), or
    while the watcher's last run still works, or over the team's hourly cap,
    the change is held in `state.pending` and given to the next run that may
    start, at the next look past it, even one that finds nothing new. A
    schedule's busy fire is skipped instead: its next time is the same
    question again, a page's next look may not be.
  - **The change is text from the web,** wrapped as `web_fetch`'s pages are
    (`webfetch.Wrap`, which both now use), with the same warning.
  - **Errors back off on the target** after 1, 5, 15 and then 60 minutes, each
    recorded on the watcher's checks; a page or feed failing for a day, or
    fetched but unreadable as the watcher reads it for a day, pauses it as
    `failing`. A 304 ends the target's errors, so a day of them and one more
    error pauses nobody. A watcher that cannot be read that way is read
    whole at each look, not told by a 304 that nothing changed, so its day
    runs on. Its runs are counted as a schedule's: five failures in a row
    pause it, `[silent]` tells nobody, a cancel is neither.
  - **A watcher that cannot be checked at all** (what it saw cannot be read
    back, its run cannot be made) records an error and is held for its own
    interval, between five minutes and an hour, so its address is not
    fetched each minute for it. It never pauses for this; a fault that
    lasts writes one error check per hold. Known gap.
  - **The watcher's source, status and pause reason are described in the
    spec, not listed as enums.** Listed, they share values with a
    schedule's, and the client generator (enum names with no prefix, a
    known gap) then renames every schedule constant in the CLI's client.
    The brain checks each value itself, and refuses one it does not know.
  - **A deleted watcher lets go of its target** (`target_id` null, which the
    table now allows once deleted), and the hourly sweep deletes targets
    nobody watches. Without that a soft-deleted watcher would hold its target
    for ever.
  - **Run now, for a watcher, is look now.** A run with no change would have
    nothing to say; a look now finds the change, if there is one.
- **A webhook watcher runs when its URL is called** (design: schedules and
  watchers, D16). The URL is `/hooks/watchers/<hook>` on the webhook listener
  only, `<hook>` 22 random characters of base64url; it carries no token, so
  the signature is all that admits a delivery.
  - **Signed by GitHub or by us.** With `X-Hub-Signature-256` it is GitHub's
    HMAC of the body, and `X-GitHub-Delivery` is required. Otherwise it is
    the custom channel's signature, through `custom.Check`, which the channel
    now uses too: a timestamp within five minutes and its HMAC.
  - **What it answers says no more than it must.** 401 unsigned, 404 an
    unknown hook or one waiting for its secret or deleted, 413 a body over
    256 KiB, 503 the secret store not answering. Only a signed delivery
    learns that a watcher is paused (409). Anything kept is 202, a refusal
    for the team's cap or the maker's rights too.
  - **A delivery sent twice starts nothing twice.** It is known by every key
    it has, each remembered for a day, and one seen under any is a
    duplicate: its delivery id (`X-GitHub-Delivery` or
    `X-Openarity-Delivery`) and what was signed. The id is not signed, so on
    its own a delivery caught on its way could be sent again under a new one.
    What was signed is our timestamp and the body, so the same body signed
    again later is a new delivery; for GitHub, which signs no time, it is the
    body. An id or event that is not printable text is left out, and the
    delivery is known by what was signed.
  - **One that cannot start a run yet is held,** within the cooldown, while
    the last run works or over the hourly cap. The next run is given what
    fits in 256 KiB, oldest first; what does not stays held for the run
    after, and the run is told how many wait. Only what fits is read. A
    watcher holds at most 100, letting go of the oldest beyond them and
    saying so on the check. The minute's tick releases them once the
    cooldown passes, not the hourly sweep the LLD named: a delivery held
    while a run worked would otherwise wait up to an hour. It reads every
    watcher holding deliveries a page at a time, skipping a team once it is
    at its cap, so the ones still held back first do not keep the rest
    waiting. One that pauses the watcher (its maker may no longer run, its
    agent is gone) is not held, as a resume drops what is held. A body's NUL
    and invalid UTF-8, which a run's input cannot hold, are replaced.
  - **A held delivery keeps its event** (`X-GitHub-Event`) in a column the
    LLD's table lacked, so the run that takes it in later still says what it
    was.
  - **Its secret is `oawh_` and 32 random bytes,** at
    `teams/<team>/watchers/<id>`, shown once when it is made. It is written in
    the transaction that saves the watcher, so none is made without its
    secret kept (the LLD deleted the row after a failed write; this is the
    same end with nothing to clean up). Only its maker may make a new one,
    which retires the old at once and makes one an agent proposed, waiting
    for its secret, active. A new one is written before anything is saved,
    so one not kept leaves the old one working. Taken over, it gets a new
    secret too, shown once: the old maker's would otherwise sign what now
    runs as the admin. Deleting it destroys the secret with every version.
    An answer carrying a secret is `Cache-Control: no-store`. The brain's
    OpenBao policy gains the path, with `update` for the new secret.
  - **Its run asks as any run does:** a tool that needs approval waits for
    the maker, as in a schedule's run.
  - **Known gaps.** The hook is not rate limited, as no channel's hook is:
    each request costs one read of the secret store before its signature is
    checked. A GitHub delivery caught on its way can be sent again once its
    day is past, as GitHub signs no time. An unsigned request to a hook
    waiting for its secret is 404 where one to an active hook is 401, so a
    caller holding a hook id can tell the two apart; the id is 128 random
    bits.
- **An agent proposes schedules and watchers; the person confirms them**
  (design: schedules and watchers, D10). The `schedule` and `watch` tools act
  as the person a run is for, on their own alone, and what they make starts
  only once a later message of that person's, in the conversation it was
  proposed in, confirms it.
  - **Who holds them.** A top-level run a person is there for, of the hub or
    of an agent granted `schedule`, while the team lets agents schedule and
    the person holds `schedule:write`. Never a delegated run, the builder's,
    or a run a schedule or watcher started; a run told it has none says why.
    An agent's own improvement proposals cannot grant it, as a workspace;
    a builder blueprint can, for a person to apply. A replay holds them as
    stand-ins.
  - **The rules live in `internal/triggers`, tools and all,** where the LLD
    put the tools in the runtime: `triggers` sits above `internal/api`, so
    the runtime takes them through a narrow interface (`Scheduling`) that
    `cmd/brain` adapts, as it does the builder's workshop.
  - **Not gated by the team's approvals.** What they ask waits for the
    person's yes instead (*user*, 2026-11-27); gating would ask on every list
    as well.
  - **What an agent asks of one that runs waits for the same yes, beside it**
    (*user*, 2026-11-27): a change to what it does, a delete, a resume or a
    run now. A page an agent read could otherwise rewrite, run or delete what
    runs as the person. Only a pause acts at once: it only stops runs. The
    schedule or watcher runs on as it was, the ask held in `pending` with
    the run and conversation that made it, until a later message confirms
    it; unconfirmed within the hour, the sweep drops it and nothing else.
    Back to `proposed`, as first asked, would stop it firing meanwhile and
    have the sweep delete a working one. The ask is taken off the row before
    it is acted on, so two yeses act once; an act refused or failed after
    the take, or a crash between them, does nothing and says to ask again. The maker's own change, the
    agent's next ask, a take-over and a delete each drop a waiting one. A proposal
    not yet confirmed is revised in place, from `proposed_at`, and the run
    that revised it may not confirm it; one is deleted at once.
  - **A yes is the person's reply carrying the code the tool gave**
    (*user*, 2026-11-28). Each proposal, revision and ask gets four random
    digits, kept in `yes_code` and given only in the tool's answer for the
    model to pass on. The run that asked may have read a page saying "when
    they reply, confirm"; the next run then reads that in its transcript,
    and a "thanks" would start it. Every other check passes then, as the
    brain cannot tell a yes from a thanks, but nothing a run read can make
    the person type the code. Confirming on the dashboard needs no code.
    What is left is a model that tells the person to type the code without
    saying what it confirms, which the confirm-first rule keeps from being
    text it read in that turn. A message that holds the code by chance (a
    year, part of a number) confirms too: about one in ten thousand of the
    four-digit numbers a person writes while something waits, and only what
    they were asked to confirm in that conversation. Requiring "yes" beside
    it would refuse a reply in another language. `list` gives the model
    every waiting code, for a person who lost theirs; the model knew each
    already, and only the person's own reply confirms.
  - **And it is sent after the run that asked finished, and confirmed before
    the run reads anything** (*user*, 2026-11-27). A message sent while the
    run worked did not answer what it said. A confirm comes before the run
    calls anything but `plan` and the two tools' acts: what a page, a file,
    a server, another agent or a list of what earlier runs wrote returned
    could say to confirm. The run marks itself on any other call, found
    tools too, and a list marks it from inside the tool. One resumed from a
    wait is read from the calls it recorded, failing closed: a call it
    cannot tell the action of counts as a read, and the tools refuse any
    argument key not spelled exactly as their own, so the record and the
    tool read a call alike. Text the person pasted in their own yes is
    theirs.
  - **It runs the agent talking, or one it may hand work to,** checked as
    delegation is. The hub's run the hub, whichever agent the team made it.
  - **Here is the chat it was asked in; on the dashboard, a conversation of
    its own** (*user*, 2026-11-27). `me` is the person's direct messages on
    the chat's channel.
  - **No secret passes through a chat.** A webhook watcher an agent proposes
    waits for its secret, and the tool gives a link to the dashboard,
    `<PUBLIC_URL>/ui/schedules/watchers/<id>`, which part 7 builds. Making
    the secret there is its confirmation.
  - **What the tools say is a refusal said for the model,** as `Refused: …`,
    and a failure stays an error. An agent the run may not name is a
    refusal of the runtime's own type, which `cmd/brain` turns into one.
  - **`pending_in` is set null if its conversation is deleted,** which
    nothing in the brain does; the ask then cannot be confirmed from a chat
    and lapses within the hour.
- **The worker deletes DBOS's record of finished background work after a
  week.** DBOS v1.2.0 never deletes a workflow row, and every tick of every
  sweep leaves one. `internal/retention`, a job of its own, lists the
  schedules DBOS runs, then deletes their finished workflows older than seven
  days, and those of the batches other jobs name to it (the fire and check
  batches).
  What DBOS runs on a schedule is periodic by construction, so nothing is
  missed when a job is added. Run workflows are kept: the `runs` table is
  their record.
- **A workspace is a room running the workspace role, so it works on every
  sandbox provider.** The brain never asks a provider for a shell or a
  filesystem API: it starts `sandbox workspace` in a room through the same
  `Rooms` port a command MCP server's shim uses, and speaks HTTP to it. A
  provider needs nothing workspace-specific, and `sandbox/contract` holds
  each to what a workspace needs, through the port alone. What follows:
  - **Public egress is in the port.** `sandbox.Spec.Network` is `listed` or
    `public`, and a provider that cannot keep a public room off private
    addresses refuses it with `ErrRefused` rather than make one that reaches
    them. Self-hosted Daytona is the one that refuses: it can only allow up to
    ten ranges, never deny.
  - **One room per session**, owner kind `session`, shared by every run on it.
    `runs.session_id` is never null, so there is no room per run.
  - **A room found gone is never retried.** An `Edit` or a `Bash` sent twice
    could act twice; the model is told and its next call starts a fresh room.
    A room the keeper replaces before a call reaches it is said the same way,
    as `sandbox.ErrReplaced` (see the workspace stop decision below).
    Later calls only touch the row (`TouchSandboxRoomByOwner`), because
    `Ensure` takes the owner's lock and probes the room on every call.
  - **`Bash` is a write; the other five read.** A command can send anything to
    any public host. The others change only the session's own room. A team
    that trusts its agents' commands adds one rule, `allow Bash`.
  - **A hub holds the tools when it is granted them.** A hub the team chose
    from its agents may act on its own as well as hand work on, so the grant
    works for it as for any agent (it first said never, 2026-11-21, and was
    changed the same day). The built-in hub is no agent and has none; the
    builder never has them.
  - **No improvement can grant it.** `workspace` is named in the proposal
    diff, so a snapshot from before the field compares equal, and is in no
    kind, so a proposal that changes it is refused.
- **A cached token counts as a tenth, in the budget and on the dashboard.**
  The provider bills a token it reads from its cache at about a tenth of a
  fresh one, and on staging 84% of the review team's input was cached:
  counted whole, a delegate rereading 100,000 tokens a step ran out of a 2M
  budget it had paid for a fifth of, and the spend alert fired at five times
  the bill. `runs.cached_input_tokens` sits beside the totals, and the weight
  is written once, in the SQL function `run_spent`: `AddRunUsage` returns it,
  and the dashboard's tokens and the spike alert sum it. `costOf` in
  `internal/runtime` prices the wrap-up's next call the same way and must
  agree. Ignoring cache reads outright would let a run loop on its context
  for nothing, so they count, cheaply. What follows from it:
  - The cached count is taken as part of the input, as the OpenAI-compatible
    APIs report it (LiteLLM included). A gateway passing on Anthropic's own
    counts, which keep cache reads apart, would be held to the input and
    undercount the cache; check a new gateway's usage before trusting it.
  - A cache write counts in full, as any fresh token, though the provider
    bills it at about 1.25 times: a weight of its own would be a second
    constant in `run_spent` and `costOf` to keep in step, for a quarter of a
    token. The SDK counts writes in `input_tokens` even where the gateway
    reports them apart, as OmniRoute does; before it did, a call that missed
    the cache read as about 150 tokens instead of 40,000, compaction fired
    late, and the budget missed every write. The usage event carries
    `cache_write_tokens` for Grafana; `runs` has no column for it.
  - The `spent` a usage event records is the budget's count from this
    change on, and input plus output before it. Nothing reads it but the
    trace, but a chart of it steps down at the change.
  - During a rolling deploy a worker from before writes no cached count, so
    its runs count every token whole, as they did. The backfill ran before
    them and does not come back for them.
- **A run compacts at 60,000 tokens of context by default, not 100,000.** Review
  delegates spent their last ten or twenty steps at 70,000 to 120,000,
  carrying tool output they were done with; a compaction left about 26,000.
  The cost is a compaction about twice as often, each a summary call and one
  request whose context is not cached yet.
- **A tool result over 8 KiB is previewed, not the SDK's 48 KiB.** Every
  run's tools, the hub's and delegates' alike: the model is sent a result's
  first 4 KiB and last 2 KiB and a `read_output` handle; the transcript keeps
  it whole, and a skill or a `read_output` result is never cut. On staging
  (2026-11-19 to 26) review delegates read 155M input tokens: their results
  over 8 KiB were 22% of them and 46% of their bytes, each written to the
  cache whole (1.25x) and read again on every later call. It applies from the
  first view, since a result shortened later keeps the write and saves only
  the cheap reads, and breaks the cache where it changes; a run in flight at
  the deploy has its 8 to 48 KiB results reshaped once, one cache miss.
  - The browser's tools keep 48 KiB (`browserPreview`): the model clicks and
    types by the `[N]` refs in a snapshot, and a preview would hide the refs
    in the middle of the page. `describe_tools`, which the brain cannot set,
    was never over 8 KiB on staging (121 results, 1.9 KB on average).
  - A brain constant, not a team setting, until a team needs another value.
  - Measured on delegates, whose transcripts #311 never touches, a week after
    against the numbers above: input and cost per delegate run, calls and
    `read_output` calls per run. Undone if calls per run rise by more than a
    fifth or cost per run does not fall.
- **A team sets its own token lines, both of them.** `compaction_tokens_min`
  and `compaction_tokens_max` are team settings, 50,000 and 60,000 until
  saved, each from 30,000 to 1,000,000 and the least never above the most:
  the table checks all three, and the handler checks the pair a save leaves,
  each value the request leaves out taken from what is stored, so a client
  from before the fields neither resets them nor saves a pair out of order.
  Below 30,000 a run would compact almost every step. The SDK already takes
  the line as `Compaction.AfterInputTokens`, so nothing in it changed.
- **A budget has two soft lines before its cap, and they measure different
  things.** With a tenth of the budget left the run compacts; when one more
  call would not fit (twice the last call's input plus room for an answer:
  twice the most it has written, at least 16k, at most `max_tokens`) it is
  asked to wrap up, and its tools refuse. One line at a tenth would stay
  crossed, so the wrap-up would follow on the next call and compacting would
  buy nothing. The cap still stops a run, which then keeps its latest summary
  as its output. A summary call's usage counts against the budget and is
  skipped by the wrap-up check, because it read the whole context.
- **Steps wrap up as the budget does.** Two steps before `MaxSteps` the run
  is told which step of how many it is at and to answer, and its tools
  refuse, so a run near its limit ends with an answer rather than failing at
  it. Two, because the recorder hears of a step as it starts and the ask can
  land a step late; a run of fewer than four steps is never asked, as it
  would lose its tools almost at once. One that still calls tools to the end
  fails at `MaxSteps` and keeps what it had, as one stopped at its budget does.
- **Compaction is the SDK's; the brain sets its thresholds and records it.**
  Past `compaction_messages`, or when the next request would read
  `min(most, max(budget/10, least))` tokens, the team's
  `compaction_tokens_max` and `compaction_tokens_min`. The least is there
  because a compaction keeps the latest ~15k tokens, the prompt and the
  tools: a line under that is crossed again at once, and a small run spends
  its budget summarising itself. A small budget is held by its soft lines instead. The summary is a message in the transcript,
  marked `compaction`, as Claude Code and opencode keep theirs: the runner
  splices it back into every save, so the Postgres store's rewrite of the whole
  transcript keeps it, and a resumed run carries on from it with no model call
  and no query of its own. The `compacted` event is the trace's, not state.
- **A session is one conversation with one transcript.** Every top-level run
  in a session continues the same transcript (`runs.transcript`,
  `session:<id>`), so a follow-up sees what the last run read and did, as
  Claude Code and OpenCode carry a conversation; compaction keeps it bounded.
  A delegate keeps its own. A session has one writer, so one top-level run
  works in it at a time: a message to a busy session is a steer into that
  run, from a chat or from `POST /runs` (200 with that run), and one that
  arrives after its last model call starts the next run when it finishes, of
  the same kind. A channel's session takes runs only from its chat, so every
  answer reaches it. A run writes its place, `transcript_from`, before its
  first message, so a crash between the two writes it once. A channel's run
  is told, as text, only what was said since the last run: a group's
  chatter, which reached no run. Further back it reads with `history`
  (`runtime/history.go`): the session's messages and its top-level runs'
  answers, by `session_seq`, bound to the session when the run is built and
  to the top-level run alone. A message keeps its sender's cleaned display
  name (`messages.sender_name`), so a group read back has speakers.

- **A new run clears the earlier runs' tool results from its view.** Before
  its place, a run writes a clearing mark (`agent.ClearingFor`) when an
  earlier run left a tool result over 1 KiB, a skill's excepted: those
  results are sent as handles from its first request, and `read_output`
  reads one back by call id, whole, a range or a match. A new run never
  needs an earlier run's raw output, and without the mark a conversation
  under the compaction line resent every one on every message: on staging
  (2026-11-26, the openarity team's runs and run events), a "hi" in a
  33-run conversation read 62,903 input tokens, about 19K of them nine old
  results. The mark is stored first, so the place still names the run's
  first message after a crash, as replay reads it, and a run begun again
  finds nothing left to clear. A model that reuses call ids across runs
  makes a handle ambiguous: `read_output` reads the latest and says how many
  share the id, rather than pass the latest off as the one asked for.

- **A session is one stream, numbered in commit order.** A session's messages
  and the events of every run in it share one count, `session_seq`, taken
  from `sessions.event_seq` by a trigger on each insert, so `GET
  /sessions/{id}/events` replays a whole conversation and a client resumes
  after the last number it saw (`sdk/stream`). The count is taken under the
  session's row lock and held to commit, so a number is never visible before
  one below it, and a resuming reader never skips one. That makes **the
  session the first lock, always**: every write of a run's events, and every
  ending of a run, takes the session before any run row (`store/session_stream.go`
  shadows `AppendRunEvent`, `RequestRunCancel` and `ReapChildren` to do it, and
  `FinishRunWithEvent` does it for a delegate too). Two writers then wait on
  each other and never deadlock; `TestARunsEndTakesTheSessionBeforeTheRun`
  holds the order. Every way a run ends writes its `finished` event, the ones
  no worker writes included (a queued run cancelled, a delegate cancelled or
  reaped), so no run is left looking as if it works. Rows from before the
  stream are numbered by `brain backfill session-seq`, down to 0, in time
  order, a session at a time under its lock: rerunnable, and safe to stop.

- **The dashboard is a channel.** Every team has one dashboard channel, made
  with the team and never deleted, and what a person types there is a message
  on it, delivered through the same inbox as a webhook's (`Inbox.DeliverOne`,
  from `POST /sessions` and `/sessions/{id}/messages`). Its conversations are
  direct sessions; only the person one is with writes in it, a moderator who
  may read it included. It has no adapter, so nothing posts to it from
  outside, and its runs queue no reply: the answer is the stream. A message
  that asks is pending until a run takes it, which the run records as a
  `consumed` event: the one it started from, at once; a steer, when the run
  reads it, oldest first, since steers are applied in the order sent and only
  messages steer a channel's session; leftovers, by the follow-up.

- **A team is deleted in two steps: marked, then purged.** `teams.deleted_at`
  hides it from every reader but `GetTeam`, frees its name through the partial
  unique index, and is undone by a restore; `internal/purge` erases it once
  `TEAM_PURGE_AFTER` has passed, through the cascade and tombstones that were
  already there. `GetTeam` stays unfiltered because the graph projector reads
  `ErrNoRows` as "drop this team's graph"; handlers read `GetLiveTeam`, and a
  super admin reads `GetTeam`. `purge_at` is computed on read, never stored, so
  changing the window moves every pending purge.
- **A delete locks the team in a statement of its own, then marks it.**
  `StartRun` takes `LockLiveTeam` (`FOR SHARE`) before its first write, and
  that alone was not enough: an UPDATE that waits on a lock re-checks the row
  it waited for, but its `NOT EXISTS` over `runs` reads the snapshot the
  statement began with, and missed the run that had just committed.
  `SoftDeleteTeam` therefore takes `LockTeamForDelete` (`FOR NO KEY UPDATE`)
  first and runs `MarkTeamDeleted` as the next statement, whose snapshot sees
  the run. The race test fails without either lock. The same reasoning puts
  the window on `PurgeTeams`' outer `DELETE` as well as in its pick: a restore
  committing while the purge waits on the row must keep the team.
- **A deleted team is read-only even to a super admin, and the guard says
  so.** `Guard.Wrap` asks `TeamState` before any write a super admin sends to
  a `{id}` route, and answers 409 unless the route is deleting the team again
  or restoring it. Members never pay the query: `ListUserTeams` already leaves
  a deleted team out of their memberships, so the scope checks refuse them
  first.
- **Config is env-only.** No config files, no flags for the server, no Viper.
  Kubernetes injects env natively, and the config surface stays small because
  secrets live in Vault rather than here.
- **Every pluggable subsystem names its backend in env.** `SECRETS_BACKEND` is
  static, openbao or vault; `OBJECTS_BACKEND` is memory, filesystem, s3 or seaweedfs. Never
  inferred from which settings happen to be filled in — that works for two
  options where one is clearly "not configured", and breaks at three, where
  "endpoint set" and "path set" can both be true, both false, or disagree. The
  enum's `UnmarshalText` turns a typo into a boot failure listing the valid
  values, rather than a silent fall back to the store that holds nothing. The
  in-memory fallback of each is refused outside development.
- **A port package, one package per adapter.** `gateway` was the first to do
  it and `secrets` and `objects` now match: the interface and its shared
  helpers live in the parent, each implementation in its own subpackage
  exporting only `New()`. The adapter type stays unexported, so there is one
  way to construct it and no way for one adapter to reach into another's
  internals — the compiler enforces what review otherwise has to.
- **A constructor returns the narrowest useful interface.** `openbao.New` and
  `inmemory.New` return the read-only `Store` even though both values also
  implement `Writer`. `serve.go` has to assert for the writer and hand it to
  exactly one router, which makes taking write access a visible line rather
  than an ambient capability.
- **Postgres is truth, the graph is an index.** Nothing is written only to
  FalkorDB; every node has a Postgres row behind it and the graph is
  rebuildable at any time. `TestARebuildEqualsTheIncrementalGraph` is what
  makes that a fact: a team's history is drained as it happens, the team is
  rebuilt from nothing, and the two graphs are compared node by node, vector
  by vector.
- **The graph is caught up through an outbox, and the row says what changed,
  never what it changed to.** A trigger writes `(team, entity, id)` in the
  transaction that made the change; the projector reads the row as it is now
  and makes the graph match. That makes a replayed row harmless, which is what
  lets the row be cleared last — a crash repeats work and never loses it. A
  trigger rather than Go for the tombstones' reason: a cascade runs no Go.
- **One drain at a time, across every worker, because graph writes do not
  commute.** Worker A claims a row and reads version 1; an edit writes a
  second row; worker B claims that, writes version 2 and clears it; A then
  writes version 1 and clears its own. The graph is behind and the outbox is
  empty, so nothing will ever fix it. The tombstones need no such thing —
  deletes commute — and this is the "second mechanism sharing a word" that
  note warned of. `Store.WithAdvisoryLock` is a session lock on its own key;
  a drain that cannot have it skips its turn, a rebuild waits for it.
- **A rebuild runs the same projection a drain does.** One query lists a
  team's things as `(entity, id)`, named as the outbox names them, and each
  goes through the function an outbox row goes through. Two code paths that
  must agree is what the equality test would otherwise be policing forever.
- **The five entity names are in Go as well as in the CHECK, and that is not
  the duplication the registry avoids elsewhere.** Agent patterns are named
  once, by the loops `runtime.Patterns` registers, and the database checks
  only that a name is well formed. Here each
  name runs different code. A name the projector does not know keeps its row
  and logs, and a test reads the names from what the triggers actually write.
- **A vector is asked for only when the embedded text or the model changed.**
  Each node carries the model and a hash of `name + "\n" + description`; a
  match skips the call, so replaying the outbox costs a read and editing an
  agent's instructions costs nothing. When the router fails nothing of the
  node is written: a new description beside a stale vector would be
  searchable and wrong.
- **A deleted team is checked once per batch, before any of its rows.** Its
  cascade leaves a row for everything it held, each meaning "delete this
  node", and FalkorDB creates a graph for any query sent to one that is not
  there. Run one by one they would drop the graph and leave an empty one.
- **A search is scoped before it is scored, always.** `Graph.Search` starts
  every query from `WHERE r.id IN $reach`, the reach read from Postgres on
  each call, and scores with `vec.cosineDistance` inside it. The vector index
  is never asked first: its top-k cut would be spent on nodes the caller
  cannot use, and the caller would get fewer results, or none, for no reason
  it could see. A team's reach is hundreds of nodes, so the scan is cheap; if
  one ever outgrows it, narrow the scope or run the vector query over the
  scoped set, never over the team and filter after. Only vectors from the
  current `EMBEDDING_MODEL` are compared.
- **The graph tool's queries are the brain's.** The model picks kinds and
  hops; those select among fixed walks in `search.go`, and every walk across
  `GRANTED` honours the grant's allow-list. A path may name a node outside the
  reach, such as the skill that led to an agent: that is the team's registry,
  which every member can read already.
- **A delegate's run is born running, in the parent's worker, and is never
  swept.** `StartChildRun` writes it `running` with the parent's worker and a
  fresh heartbeat, and the `delegate` call holds it inside the tool call; it is
  never queued. `TakeOverStaleRuns` takes `parent_run_id IS NULL` only, because
  a child has no work of its own to resume: its parent is waiting on its
  answer inside a model turn, and that turn is what a resume replays.
- **A child runs on its parent's context.** Cancelling the root, the root's
  six-hour clock and its token budget end the child through the same context,
  with no second path to keep in step. A parent that lost its run stops its
  child too, and writes nothing: the run's new owner reaps it on resume. What
  the brain did not cause — DBOS stopping the worker — leaves the child
  `running` and returns no answer, so the parent is resumed elsewhere rather
  than told its child failed. `ours()` is the list of causes that are the
  brain's.
- **A resumed run carries on the children its worker left, and reaps the
  rest.** It replaces decision A, which reaped every running child and had
  the model ask again: a deploy restarted a review's delegates from nothing,
  and #311's first review on staging ran 17 child runs in 57 minutes where
  one runs about 5 (about 11M tokens a week went to children that failed or
  were cancelled). After its `resumed` event and its catch-up, `carryOn`
  lists the running children that have not beaten for `heartbeatLostAfter`
  (60 s; the parent itself is taken over only after `staleAfter`, 90 s, so a
  dead worker's children are past it by a beat, which a test holds), takes up
  each whose call in the last turn closeCalls closed as ended, one child a
  call, together as a pattern that runs a turn's calls together ran them, and
  puts its report, or its waiting report, in that call. `AdoptOrphan` takes
  only that parent's child, so a stray resume cannot take another tree's.
  The reports are saved at once, as a child carried on to its end is never
  listed again. A child resumes from its own transcript like any run, so it
  carries on its own children. One that cannot be carried on costs the run
  only its report: its call stays ended, unless the worker is stopping or the
  store fails to hand it over, which fail the run, as asking again could
  repeat what the child did. Then `ReapChildren` fails what is still
  running, with `reapReason`: a child that beats, which is another worker's,
  or one whose call is not open. The catch-up comes first so a child carried
  on that parks is not taken up again in the same resume.
- **Whatever ends a run ends what it left running.** `finish` reaps the
  run's running subtree once it has recorded the run, so a resume that fails
  before its own reap (its agent deleted, its clock spent) leaves no child
  behind, and neither does a child whose own finish failed. A run cancelled
  while queued never runs again, so `RequestRunCancel` cancels its running
  subtree in the same statement. A finish that finds the run no longer its
  worker's reaps nothing: the run's new owner decides. Children are never
  swept, so these are the only ways a child ends without its parent.
- **A run's events have one writer while it runs.** `AppendRunEvent` takes
  `max(seq) + 1`, so two writers race for the same seq: staging hit
  `run_events_pkey` when `Delegate` wrote `delegated` while the recorder was
  still writing the parent's `tool_call`. The brain's own events go through
  the run's recorder as notes (`Executor.note`), and the agent's channel is
  unbuffered, so a tool starts only once the recorder holds its call: a
  note written from inside the tool always lands after it.
- **A child's budget is its own agent's, and tokens are not shared by the
  task.** The delegate call gives the child its agent's budget, or the
  default, written on the child's row, however much the task has spent. A
  child starts from nothing and its context is its own; the parent pays only
  for reading the report, which comes back as a tool result. A shared token
  cap made a child started late in a task near-useless, and compaction keeps
  each run's calls bounded anyway. What a task still shares is a runaway's
  limits: 50 runs and six hours, besides the depth of 3.
- **A report is data in an envelope only the brain can close.** The child's
  output goes back as `<report agent run status end>` … `</report end>`, where
  `end` is `rand.Text()`, fresh per report and never shown to the child.
  Every `<report` or `</report` in the body is escaped, whatever its case, so
  the body cannot open or close a tag. The body is capped at 32 KiB after
  escaping, cut where a rune starts, with a line naming the child's run for
  the rest. Escaping alone left look-alikes (`＜/report＞`, a zero-width
  space); the unguessable end is what makes them text.
- **The hub is a role a run has, not a kind of agent.** `runs.hub` marks a run
  started for the team rather than for an agent: `POST /runs` with no agent.
  `CreateHubRun` writes the team's chosen agent onto the row in the same
  statement, or none for the built-in orchestrator, so the hub is decided
  once per run: changing the setting mid-run does not change who answers,
  and a hub agent deleted since resumes as the orchestrator. The same agent
  delegated to is only itself — hub prompt, `plan` and the hub's reach come
  from the run's flag, never from the agent's row, and a check keeps the flag
  on roots only.
- **A browser is a grant of its own, and one provider port over CDP.** An
  agent's `browser` is apart from `web`: a browser runs a page's scripts and
  acts on it, where search and fetch only read. The hub has it whenever its
  team has a browser. Every provider speaks the Chrome DevTools Protocol, so
  `internal/browser` only finds the address: built from a URL and a key
  (Browserless, Lightpanda, any `cdp` browser, Bright Data), or handed back
  by a session the provider makes on request and ends when the run lets it
  go (Steel, Browserbase, Hyperbrowser, Anchor, Kernel). A key is kept as the
  search key is, at `teams/<team_id>/browser/<id>`, and sent again when the
  provider or its URL changes. A self-hosted browser may have none: most ask
  for none, which is why each compose overlay puts one on a network shared
  only with the worker. None of the providers documents CDP request
  interception, so it was measured on the four self-hosted images; all four
  pause every request for the brain to allow. The staging browser is
  Lightpanda, about 63 MiB after a page against 300 to 980 MiB for the
  Chrome-based three.
- **The brain dials the browser itself, and a browser of the team's own must
  let its requests be checked.** chromedp dials with a websocket dialer of
  its own that nothing can sit in front of, so `internal/browser` hands it a
  connection made with `coder/websocket` over the guarded client
  `MCP_PRIVATE_NETWORKS` builds, through an allocator shaped as chromedp's
  remote one. Then `Fetch.enable` pauses every request the page makes, and
  only a host whose every address is public goes on. A self-hosted browser
  that refuses to pause is not used, since it sits on the team's network; a
  hosted one is used anyway, since what it reaches is its provider's. The
  page is read by one script, not the accessibility tree, because Lightpanda
  runs scripts but lays nothing out; a control is found by the
  `data-oa-ref` the read stamped on it.
- **What Fetch cannot see, a page may not do, and the guard is proved.**
  Fetch on the tab pauses the requests of the page's own tab and frames, but
  no socket of any kind, no shared worker's and no other tab's; and
  `Network.setBlockedURLs` stops no websocket handshake either, measured. So a
  script run before the page's own, in every frame, takes away every socket
  (`WebSocket`, `WebSocketStream`, WebTransport, WebRTC), every worker (no
  script of ours reaches into one to take its socket away) and
  `window.open`, and sends a link or form aimed at another window into its
  own. That script is best effort: a page's own listener, after ours, can
  still send a form to a popup. So a browser of the team's own is also
  paused browser-wide (`Fetch.enable` on the browser target), which reaches
  popups, shared workers and frames from other sites. On a browser runs
  share, a run's browser-wide guard pauses other runs' requests too and
  judges them by the same rule; what it refuses there is not named in the
  run's read, as it may be another run's. Measured on headless-shell and
  Lightpanda, with an address the browser can reach and the guard refuses
  and a second site on the same host: nothing gets there, and the popup a
  listener sends does without the browser-wide guard. A popup that opens
  all the same has no script of ours, and its socket nothing pauses
  (measured: it reached the refused address), so every page target the tab
  opens, or that opens in its browser context, is closed as it appears; the
  race between its opening and its close is the browser's, and in the test
  the popup's page was never served. The browser-wide guard judges on a
  queue of its own, small, so other runs' requests never hold the tab's
  judges; what neither queue takes is failed by two failers, never let
  through, and only the tab's own are counted in its read. A frame from
  another site in a process of its own gets no script of ours: accepted,
  and left with DNS rebinding and a preconnect's bare connection to the
  browser's network; headless-shell runs such a frame in the page. A
  browser of the team's own is sent to an address that is always refused,
  and asked to fetch another from a page, each by a random path matched
  exactly, before it is used, so one that acknowledges `Fetch.enable` and
  pauses nothing, or pauses navigations only, is found out, whatever other
  runs' canaries on a shared browser are doing; and each run gets a browser
  context of its own. A
  browser that does not answer in time is a failure, never a refusal, so a
  hosted one is not used unguarded for being slow; one that refuses is, and
  every read says so.
- **The browser is approved a page at a time, and only the open asks.** A
  pause for a person ends the run's process and with it the page, so an ask
  on a click would leave nothing to click. `browser_open` asks at the
  strictest verdict of every browser tool the run is offered, and what is
  then done on that page is made without asking, on the site that open was
  for: a link may lead away, but a click or typing there, or a form sent
  there, is refused until `browser_open` opens it, which asks. The check
  runs in the same script as the action, so the page cannot move between
  them. Without it one approval would cover typing into any site an
  injected page led the model to; the text typed is still not shown to the
  approver, a trade made knowingly. Approving the open for the run covers
  every page, each on its own site. `browser_click` and `browser_type` are
  writes (`approval.Acts`) for anyone, submit or not: a click can buy, and a
  page's script can send what is typed. Destructive was weighed and not
  chosen, a maintainer's decision recorded here: in `destructive` mode a run
  clicks and submits unasked, and a scheduled one made for someone who may
  approve their own does so with nobody present, and a
  team that wants otherwise uses `writes` or a rule. Rules per tool: one
  that asks for any browser tool is asked at the open, and an allow on the
  open alone does not stop it; one that denies the open leaves the run no
  browser; one that denies the click or the typing removes only that tool,
  and the request stops saying it covers it, though a denied click does not
  stop `browser_type` with `submit` sending a form. The site kept to is the
  exact host asked for and landed on, checked on the action after a link,
  not on the link. Keeping a page across a pause was weighed
  and refused: a Bright Data session ends with its connection, and a tab left
  without its guard attached runs on with nothing pausing its requests.
- **A browser of the team's own has one way out: the worker's proxy.** The
  guard judges a name and the browser looks it up again, so a name that
  answers public to one and private to the other is past it, and some of
  what a page does (a preconnect, a socket in a worker) is never paused. So
  the overlays put each browser on an `internal` network, which routes
  nowhere, and start it pointed at a forward proxy the worker serves there
  (`internal/browser/proxy.go`). The proxy dials with a dialer that refuses
  any address that is not public, and link-local whatever is allowed, at
  connect time, so the address checked is the one connected to and nothing
  is looked up twice. It binds every interface and is found as `worker`, so a
  second worker serves it too; what else reaches it gets only what the
  internet already gives. Host firewall rules were the other choice: they
  differ by host, and the ones written for this were never run. It dials
  ports 80, 443, 8080 and 8443 only: any port would make it a relay for mail
  or a scan from the host's address, and those four serve the sites a run
  visits, staging ones included. A proxy that stops stops the worker, which
  its restart policy brings back with one: a worker without it looks healthy
  while every self-hosted browser reaches nothing.
- **The hub's graph finds every agent, and only its own tools and skills.**
  Its reach is the team's other agents, to hand work to, and the tools and
  skills the hub agent holds, since those are all it can call or load; the
  built-in orchestrator holds none. A tool another agent holds is still found
  by hops, with that agent as the hit, because a walk filters only its result
  by the reach. Before, the hub was offered every tool in the team, and a
  tool on a server granted to nobody was ranked, picked and failed at the call.
  The builder keeps the whole team: it designs the team and calls none of it. The web tools come from either: a run has
  them when it is the hub's or its agent has `web: true`, so a delegate has
  them only by its own grant.
- **The built-in orchestrator is no agent row.** It is built in `hub.go` with
  `uuid.Nil` for an id, react for 24 steps, on the team's model; with none
  set it is refused at `POST /runs` with a 409 and fails at build with the
  same sentence. `uuid.Nil` is what keeps it holding no grants, excluding
  no agent from the hub's reach, and never matching a real agent in the
  delegate checks. A team's agent may be named `orchestrator` too; the hub
  badge, not the name, is what says which ran.
- **Deleting the hub agent nulls the setting, and the API keeps the hub in
  its team.** `team_settings.hub_agent_id` is `ON DELETE SET NULL`, which returns
  the team to the orchestrator. Postgres 13 has no `SET NULL (column)`, so a
  composite key would null `team_id` too; same-team is the API's to enforce,
  and `CreateHubRun` and `GET /settings` both read a hub outside the team as
  none rather than trust the row.
- **Search results are data in an envelope only the brain can close.** As a
  report is: `<search_results query end>` with `end` from `rand.Text()`, fresh
  per call. Every title, address and snippet is the page's to choose, so each
  is folded to one line and has `<search_results` and `</search_results`
  escaped whatever its case: a hit cannot close the list or start a line that
  reads as the next result. Snippets are cut at 500 bytes and the list at
  16 KiB, each where a rune starts.
- **A self-hosted search URL is checked when the brain dials it.** The search
  client's `Control` refuses any address `reachable(MCP_PRIVATE_NETWORKS)`
  refuses, and link-local always, mapped or zoned. It sets no proxy, since a
  proxy would dial for it and the guard would see only the proxy, follows no
  redirect, and reads at most 2 MiB in 15 seconds. The settings API judges a
  URL with the same predicate, so a team cannot save an address the worker
  then reaches. An error names the provider and the status and never what
  the provider said: a refusal can quote the key back.
- **A page is data in an envelope only the brain can close.** As search
  results are: `<page url title end>`, both attributes HTML-escaped, the
  title folded to one line, and every `<page` or `</page` in the body escaped
  whatever its case. The body is kept whole, up to the 2 MiB the plain fetch
  reads, with a line saying so when it stopped there; it was cut at 32 KiB
  until `read_output` could reach the rest. A page is previewed like any
  result, past 8 KiB as its first 4 KiB and last 2 KiB, so the model always
  sees both ends of the envelope, and the opening tag's url and title are cut
  so the tag fits in that head. Its own 32 KiB preview went with the SDK's
  smaller sizes (#255), when large results resent on every call, read_output's
  most, turned out to drive a run's spend; a page is not special. A line the
  page wrote that starts like the brain's is escaped with a backslash, so
  only the brain's says the page was cut. The url must be http or https, carry no
  user, name a host and fit in 2,048 bytes, and a host a browser would read
  as a number (`2130706433`, `0x7f.1`, `127.1`) is refused unless it is an IP
  address as written: resolvers and scrapers disagree on what it names.
- **A fetch dials public addresses only.** `MCP_PRIVATE_NETWORKS` is for
  addresses an operator chose; a fetch URL is a model's, often repeating what
  a page told it, and honouring the allowance would make any page a way into
  the operator's network. The plain fetch's client is `hub.Public` at dial
  time, on every redirect (five at most, http or https only), with no proxy
  and an Openarity User-Agent. It reads at most 2 MiB and cuts there rather
  than refusing. A charset the header names wins; without one, valid UTF-8 is
  read as UTF-8, because the stock sniffer takes an ASCII first KiB for
  windows-1252, and only other bytes are sniffed. A type the header names and
  it cannot read as text (a PDF, an image) is refused before the body is
  read; a page that names no type is sniffed from its bytes. HTML goes through
  readability first, so the article comes before menus and banners, and falls
  back to the whole page when there is no article.
- **A team's Firecrawl is judged before and after it reads.** Firecrawl
  dials from its own network, which the brain cannot guard, so the host is
  resolved and refused unless every answer is public before Firecrawl is
  asked, and the address it says it ended at (`metadata.url`, never the
  requested `sourceURL`) is judged the same way afterwards: a redirect to a
  private address returns no page. Firecrawl's own lookup can still differ
  from the brain's, which only network rules on a self-hosted Firecrawl
  close. A scrape renders the page, so its client waits 45 s where search
  waits 15, on the same guarded transport, and Firecrawl is told to stop at
  40 s so that a slow page ends as Firecrawl's answer. Its reply is read
  through the search client's helpers, so one over 2 MiB is refused, not cut.
- **The web tools read the team's settings once.** A key that cannot be read
  costs `web_search`, with its event, and `web_fetch` falls back to the plain
  read; so does a Firecrawl that cannot be built. A team with no provider has
  `web_fetch` alone, and nothing is said: that is the team's choice, as it is
  for the hub's search.
- **A missing search key costs the tool, not the run.** A key not in the
  store, a store that is down, or a provider that cannot be built leaves
  `web_search` out with a `tool_unavailable` event saying why; a provider
  that fails mid-run is a tool error the model works around.
- **Narrowing never adds a grant.** `Lineage.Narrow` keeps only the MCP tools
  and skills it names, checked after a grant's own allow-list, so a name
  never granted is ignored. The brain's own tools are added after the grants
  and are never narrowed. `nil` keeps every grant and an empty narrowing
  keeps none; nothing sets it until the outfitter, and a child's own
  delegates are built without it.
- **A server may not take a name the brain's own tool has on that run.**
  `graph` and `delegate` on every run; `plan` on the hub's; `web_search` and
  `web_fetch` on a run with the web (the hub's, or an agent's with `web:
  true`), decided per run by `reserved`. A bare MCP server's tool of a
  reserved name is left out with a `tool_unavailable` event. Reserving them
  for every agent, because any agent may be chosen as the hub or granted the
  web, cost a plain research agent its search server's `web_search` on the
  upgrade; the clash only exists while the brain's own tool is there.
- **A hub with no graph answers alone.** Agents are found through the graph
  and listed nowhere else, so with no embedding model the hub is given no
  `delegate` and is told the team cannot be searched, rather than being
  left to guess names.
- **The drain ticks once a minute, not every ten seconds.** DBOS keeps a row
  per tick and deletes none: measured, 98 ticks left 98 workflow rows and 98
  step rows, about 560 bytes a tick before indexes. The job's durable state is
  the outbox, so a tick buys only freshness. It does not backfill either — a
  missed tick has no work of its own. `retryAfter` is half a tick so a failed
  row is due by the next one whichever of two timestamps came first.
- **`falkordb-go` is used for its connection and its result parsing, and for
  nothing that writes a parameter.** Its own parameter writing quotes with
  `strconv.Quote`, which turns a non-breaking space into six characters, and
  panics on a `[]float32`. `params.go` writes the `CYPHER k=v` header itself
  and passes `nil`. Its constructor is not lazy either: it asks the server
  whether it is a sentinel before returning, on a context of its own, and a
  server that accepts and says nothing holds it for ten seconds. `Dial` takes
  a context for that reason.
- **A missing end is an error, where FalkorDB reports success.** An edge to a
  node that is not there matches nothing and writes nothing, silently. `Link`
  and `SetOutEdges` count what they wrote, so the outbox row stays and is
  tried again once the other node has been projected. Same for a vector of
  the wrong width, which FalkorDB stores and never searches.
- **A secret is a reference when a team made it, and a value when the
  operator did.** An MCP server's credential or a channel's signing secret is
  created through the API while the brain runs, so no environment variable
  can hold it and Postgres must not: the row keeps a path. `EMBEDDING_API_KEY`,
  `LLM_API_KEY` and `SKILL_IMPORT_GITHUB_TOKEN` are set once by whoever
  deploys, exactly like the DSN's password and `OBJECTS_SECRET_KEY`, and are
  environment variables for the same reason. They were references first, and
  that cost more than it bought: a brain on the static store could not be
  given a key at all, and the OpenBao policy needed a read rule nothing else
  used — one that shipped missing, 403 under the real policy with every test
  on a convenient credential passing. None of the three is in `String()`.
- **The LLM and the embedding model are two endpoints with two keys.** They
  are often two services, and the shapes are identical, so the wired test
  sets both and asserts the LLM one received nothing. `EMBEDDING_BASE_URL`
  has no default and does not fall back to `LLM_BASE_URL`: it is set with the
  model or not at all.
- **The test suite's Postgres floor is 18; the brain's is 13.** Both measured,
  not read off a changelog. 13 is where `gen_random_uuid()` became built-in, and
  every migration applies and rolls back on 13 through 18. 18 is the first
  release to raise `SQLSTATE 23001` for an `ON DELETE RESTRICT` refusal, which
  `schema_test.go` asserts; 13 through 17 report it as `23503`. `internal/store`
  has a `TestMain` that refuses below 18 rather than skipping — the bug it
  exists for was a suite running green against a server nobody chose, and a
  skip is that same silence in a different colour.
- **Two listeners, one process.** The API binds loopback; webhooks bind
  publicly. The auth models are opposites — user credentials versus request
  signature — so they never share a listener. Signature verification needs the
  raw request body, so nothing may parse it first. They never share a mux
  either: one handler on both ports exposes every API route publicly the day
  the routes diverge.
- **Listeners share a fate.** If one fails to bind, the other is shut down and
  the process exits non-zero. Half-alive is the worst state — the pod passes
  its probe while silently dropping webhooks, providers retry a few times and
  give up, and nothing is logged. Fail fast and let Kubernetes crash-loop it
  into an alert.
- **`http.ErrServerClosed` is success.** `ListenAndServe` always returns a
  non-nil error; a clean `Shutdown` returns that one. Treat it as a failure and
  every rolling update is recorded as a crash.
- **Server timeouts are constants, not configuration.** Go's zero-value
  `http.Server` has no timeouts at all, which leaves the public webhook port
  open to Slowloris — `gosec` G112 fails the build on a missing
  `ReadHeaderTimeout`. The same values are right in every environment, so they
  stay named constants. Promote one to config only when a real deployment needs
  a different value. `WriteTimeout` bounds handler execution too, so a run's
  event stream pushes its own deadline forward with
  `http.NewResponseController(w).SetWriteDeadline` before every write. That
  reaches the connection only through `Unwrap`: the logging middleware's
  recorder had none, and every stream closed after its first frame while the
  handler's own tests, on a bare mux, passed.
- **Secrets are references.** A row or config field holds a Vault path, never a
  value. Only `internal/secrets/openbao` reaches a secret backend.
- **Attachments are encrypted before they leave the process.** AES-256-GCM
  under a per-team key held at `teams/<team_id>/attachments`, so the object
  store holds `nonce || ciphertext || tag` and never a key, and reading one
  team's files needs OpenBao *and* that team's key. Encryption is a layer over
  whichever backend `OBJECTS_BACKEND` selected, not a fourth backend — it lives
  in `internal/objects/encrypt.go` rather than beside `s3`, because nothing
  sets `OBJECTS_BACKEND=encrypted`.
- **The team is a parameter, never parsed out of the object key.** The key is a
  string on a database row, so deriving the decryption key from it would let a
  tampered row choose which key is used. Taking the team from the authenticated
  route makes `objects.InTeam` a check rather than a derivation, and every
  refusal is asserted to have changed nothing.
- **The object's key is GCM's additional data.** Additional data is bound, not
  encrypted — it costs nothing and stops somebody with write access to the
  bucket copying one attachment's bytes onto another's key, which without it
  decrypts perfectly: same team, same key, valid tag, and a person served a
  file the row did not name. It forecloses renaming an object, which we do not
  do, and which would fail at the first read rather than silently.
- **A key is decided once, and everyone else agrees with the decision.** A
  team's key is generated on first use, so two concurrent first uploads race.
  `Put` cannot settle it — KV v2 overwrites, so both writers succeed and the
  loser's data is sealed under a key nothing holds. `secrets.Creator` is the
  check and the write as one operation: `cas=0` in OpenBao, one lock in the
  static store. The loser reads the winner's key rather than retrying, because
  retrying returns `ErrExists` forever.
- **An attachment's type comes from its bytes, and the list says what is
  permitted rather than what is not.** A provider's declared content type is
  attacker-influenced; a deny list is a list of the attacks already known.
  Both halves are needed and only one lives here: sniffing at write time is a
  claim the read path has to keep, by serving the recorded type with
  `X-Content-Type-Options: nosniff` and never a type derived from a filename.
  An SVG carrying a script sniffs as `text/plain`, and a PNG with markup
  appended still sniffs as `image/png` — both inert only while served as what
  they sniffed to.
- **An attachment's team comes through its session, never through its
  channel.** `sessions.team_id` is the authoritative copy and always present;
  `sessions.channel_id` is nullable, because the dashboard and the API start
  sessions with no channel behind them. A composite foreign key keeps the two
  team ids equal whenever there is a channel, so joining through sessions is
  never wrong and is sometimes the only join that answers. `GetAttachmentWithTeam`
  returns the team in the same round trip as the row, so authorisation never
  needs a second one.
- **`attachments` carries `session_id` as well as `message_id`, and the pair is
  a foreign key.** The copy is redundant and cannot drift: `messages` has a
  unique key on `(id, session_id)`, so an attachment naming a session its
  message is not in is refused by the database rather than by whoever
  remembered. Same mechanism as `sessions.team_id` against `channels.team_id`.

  It is there because "every attachment in this session" is what an agent asks
  to build context — a message can name a file that arrived twenty messages
  ago — and no index can express that through the join. Measured on 200
  sessions, 209k messages and 21k attachments: 13.6 ms warm through `messages`
  against 0.36 ms through the column, because the join reads every message in
  the session *and* every attachment in the database to return a few hundred.
  Denormalising on a guess about load is suspicious; denormalising because the
  schema cannot express the access path is not.
- **A team's search key has a path per key, and stays with its address.**
  `teams/<team>/search/<key id>`, fresh on every save, with the id on the
  `team_settings` row and a trigger tombstoning the one it replaces. One
  fixed path per team would let the tombstone of a cleared key destroy a key
  saved after it but before the sweep ran. A save that leaves the key out
  keeps it only while the provider *and* the URL are unchanged: moving a
  self-hosted provider's URL with the key still attached would send a
  write-only secret to whoever the new URL names. An automated review caught
  that the first version compared the provider alone, which was enough until
  a provider with a URL of its own existed.
- **`attachments.key_version` exists before rotation does.** A read has to know
  which key sealed an object, and adding the column now costs nothing while
  adding it later means a migration over every attachment ever stored. Every
  row is version 1 today.
- **A file a person sends is reserved before it is written**, as a skill's
  files are: its key goes into `deleted_objects` held for an hour, then the
  bytes into the bucket, then the attachment row with the message. A delivery
  refused, repeated or cut short by a crash leaves bytes no row names, and the
  reaper deletes them once the hold passes; one that commits is kept, since
  the reaper counts references first. `gateway.Ingest.Store` does it for the
  webhooks and the dashboard alike, so a webhook delivered twice no longer
  leaves its second copy in the bucket for good.
- **A model reads a file by reference, and the bytes are fetched per
  request.** A run's transcript holds `attachment:<id>` and never the file:
  transcripts are saved in Postgres outside the attachment encryption, and a
  session outlives any one run. The runner's model client looks each reference
  up within the run's session, so another conversation's file is a missing
  row, and decrypts it for that request only; a four-step run reads an image
  four times. Keeping bytes in the worker's memory between requests would
  save the reads and leave every file a run saw lingering in a process that
  runs other teams' work. At most eight files go in one request, the newest,
  text files among them; images are read up to 5 MB and PDFs and text up to
  10 MB, and a text file is sent as its first 100 KiB.
- **A request whose files the provider cannot take goes again without
  them.** A file read stays in the transcript and goes with every later
  request, so one a provider breaks on would fail the conversation for good:
  OmniRoute answers a PDF of about 6 MB with a 500, its translation
  overflowing a regular expression's stack
  ([OmniRoute#21015](https://github.com/cheaper-inference/OmniRoute/issues/21015)).
  A 400, 422 or 5xx to a request carrying files is tried once with each
  image and PDF named as not sent, text files still sent as text. A stream is
  tried at its first read, since openaicompat reports a provider's answer
  there and not when the stream opens, and never after an event, which was
  already shown. After a 400, 422 or 500 the rest of that run sends none, so
  the refusal is paid once a run, not once a step; any other 5xx (a
  gateway's, a 529 overloaded) may pass, so only that request goes without. openai-go's own
  retries come first, so a 5xx is sent three times before it is tried bare.
  The model says the file could not be read rather than the run failing. If
  the try without files fails too, the first answer is the run's error. Not
  tried again: `context_length_exceeded`, which is compaction's, and a 429.
- **A tool's file is a conversation attachment.** Every file an MCP tool
  returns, readable or not, up to 25 MiB (`mcp.MaxKeptBytes`, a person's
  limit too), is kept by the run's keeper (`runtime/keep.go`) as one of its
  session's attachments: its key reserved, its bytes sealed under the team's
  key, and a row whose source is the run's call (`run_id`, `tool`,
  `call_id`) rather than a message, exactly one of the two, checked by the
  database. The transcript holds only `attachment:<id>`; the model is shown
  an image or a PDF it may read by reference, fetched for each request by the
  rule above, and every other file is a line with its id, which
  `read_attachment` and `attachments` reach from the run's start when the
  run has MCP tools; a run without them, such as a delegate with none, binds
  them only when the session already has files, so one built before a
  sibling's first file cannot read it. A keeper
  that fails does not fail the call, since the tool's work is done: the file
  is named as not kept, and the cause logged, never shown. The SDK decides
  what a file is and how its line reads (`mcp.Keeper`), so the brain never
  rewrites a result. A call made twice, as a replay, keeps its file twice:
  two rows, each the bytes it returned. Whoever may read the session may
  fetch a tool's file by id through the bytes route, as a person's; the
  API's list of the session's files is a person's alone (`people_only`),
  each named by its message, until a tool's file reaches a person through
  `send_file` (F3). A pod from before this reads `message_id` as never null,
  so its attachment reads fail on a tool's row while a deploy rolls: the
  window is the rollout's. Runs from before this kept their files' bytes in
  their transcripts, and `Resolving` still sends inline bytes as it sends a
  reference's.
- **A run hands its person a file with `send_file` (F3).** Any file of the
  conversation, a person's or a tool's, by its id, looked up within the
  session so another conversation's is a missing row, and again by the
  database (`replies_file_in_session`). Offered to a top-level run only, as
  only it answers the conversation, and only where the file can arrive: a
  conversation read on its stream (no channel, or the dashboard's) or a
  channel the run answers whose provider is a `gateway.FileReplier` and, for
  `custom`, has a reply URL. Slack's upload is three calls, each built from
  the answer before it, so it is a `gateway.FileUploader` the sender runs a
  step at a time (F3b; the gateway's DECISIONS has why). Behind the approval
  gate as a built-in (`approval.Shares`), judged like the web: a read for a
  person who is there and may approve their own calls, a write for a guest
  or a run nobody is present for. So `writes` asks for a guest and refuses
  the call in a run nobody is present for (`approval.Decide` turns an ask
  into a deny there), `all` asks every time for anyone present, and a
  team's rule on `send_file` asks or denies on every channel at once. Not a
  channel setting: who may send a file is a question about the tool, and
  the answer is the same wherever the file goes. A request names the file
  by its id alone, and approving for the run covers any file the run sends
  after; naming the file a call chose would need a lookup when the request
  is made, left for later. A run event `file_sent` puts a chip under the
  run on the dashboard;
  on a channel the file is also a reply of kind `file`, in the session's
  order, so a file sent while the run works goes out before its answer, one
  per call (`replies_one_per_file_call`) and kicked at once. Ten a run, a
  try at a file found counting whether it went or not, and a caption of
  1024 characters, Telegram's. The name is reserved on every run, so a
  team's MCP tool already named `send_file` is refused from then on. The
  sender loads and decrypts the bytes just before it builds the requests,
  one file at a time; a file gone since is given up, a store that fails is
  retried. Telegram sends a document, never a photo, which it recompresses;
  `custom` sends the bytes inline in the signed reply rather than a link,
  which would be a route serving files to whoever holds it. A replay holds
  `send_file` as a stand-in, as it holds the workspace's tools. Delivery is
  at least once, as every reply's: neither Telegram nor Slack deduplicates,
  so a post (on Slack, the upload's last step) whose answer was lost sends
  the file twice there. A file is tried twice, as a
  step is, so one that cannot go holds its answer back a minute, not hours;
  bytes that cannot be decrypted are given up at once. A file given up is
  not sent, and nobody is told: the model was told it was sent when it was
  queued, and the reply shows as given up only in the database until the
  dashboard shows a reply's delivery. A post held whole has a deadline by
  its size, ten seconds and a second per 512 KiB, a minute at most; one that
  streams a file, and a file reply's lease, grow with the file instead (F4c,
  below). An upload in steps, Slack's, is several posts under one claim,
  with a deadline of its own inside the lease. A claimed batch is sent one
  row at a time, and each row after the first has its lease held again
  before it is sent, so one behind a large file is never sent by a second
  sender too; a row whose lease another sender took is left to it. A worker
  that dies sending a large file holds that conversation's later replies
  back until its lease runs out: two minutes and the file at 256 KiB a
  second, under nine minutes at 100 MiB. A custom channel's sender holds
  its file whole, about three copies (the bytes, base64, the body): some
  100 MB at 25 MiB.
  Ten files a run, the count read when it is built from its file calls,
  each once whether its reply or its event was written, so a resume keeps
  to it. A reply not written and not queued is
  the model told the file did not go; on a channel, a `file_sent` event that
  fails after the reply is queued is logged, not the call's failure, as the
  model would send the file again. Deleting a file deletes its file replies,
  sent ones too: a file goes only with its conversation, its message or its
  run, and the record of its sending goes with it. The risk accepted: for a
  person who is there and may approve their own, the call is a read, so
  under `off`, `writes` and `destructive` an instruction a tool's content
  plants could send them a file of the conversation unasked. It reaches
  only this conversation's people, who
  could already be shown the tool's text in the answer; a team that wants a
  person between the two sets a rule on `send_file`.
- **A file a tool wrote in a room is kept with `keep_file` (F4).** Some
  tools never return a file: `mcp-mail-server`'s `save_attachment` writes
  it into the server's room and answers with its path, and a script in the
  workspace draws a chart into `/workspace`. `keep_file(path, server?)`
  reads it from that room's `/file` route, which the shim and the workspace
  role serve alike (`apps/sandbox/internal/roomfile`), and keeps it through
  F2's keeper, under the tool name `keep_file`. The model names the path:
  nothing finds paths or base64 in a tool's text, as no rule for that holds
  across servers. A command server's room is read as it runs
  (`keeper.Find`), never started to look: a fresh room holds none of the
  old one's files, and the model is told the file is gone. The shim's token
  is one the server can read; the route hands over only that room's files,
  which the server can already read. A room's own refusal carries
  `X-Room-Problem`, so a missing file is told from a missing room, which a
  provider's proxy also answers 404. Behind the gate: class `Room`, a read,
  for the session's own workspace; `Shares`, as `send_file`, for a run that
  holds a command server, whose room is the server's and shared by the
  team's sessions, so a guest or an unattended run would bring in another
  conversation's file unseen. The class is the tool's, so a workspace read in
  such a run is judged that way too. A run holding `keep_file` holds the
  attachment tools, as its tool list is fixed and its first file comes
  mid-run. Twenty tries a run, a failed one counted, counted from the run's
  earlier ones; one file read at a time, read by a client with the dial
  policy and no 8 MiB cap of its own (`discovery.RoomClient`); up to 100 MiB
  since F4b, below. A file an MCP tool returns stays at 25 MiB.
- **Every object is written as v2, sealed in chunks (F4b).** v1 sealed a
  file as one GCM message, which opens only whole, so every read and write
  held the file. v2 is `"OAo2"`, a 32-byte salt, then 64 KiB chunks under a
  key of the object's own (HKDF of the team key, the salt and the object key),
  each with a nonce that is its index and a last-chunk flag: the STREAM
  construction, as age uses it. A key per object makes a counter a safe nonce
  with no birthday bound across a team's files; the index stops chunks being
  reordered or dropped, the flag stops the file being cut at a chunk's end,
  and the object key in the derivation keeps v1's refusal of bytes moved to
  another key. Small objects are v2 too, so there is one writer; v1 stays
  readable forever, told apart by the magic and tried as well when a v1 nonce
  happens to begin with it (2⁻³²), so no column records the format.
  `Encrypted.Open` opens the first chunk before it returns, so a wrong key
  fails before a download has answered; a chunk damaged later cuts the body
  short of its `Content-Length`. S3 writes past 8 MiB as a multipart upload,
  aborted on failure; the filesystem writes a synced temporary file and
  renames it. `keep_file` streams from the room into the store, up to
  100 MiB; a channel takes what its provider's `MaxFile` says, below. Only `io.EOF` ends a file
  (`objects.Strict`): `io.ReadFull` reads a short last chunk as
  `io.ErrUnexpectedEOF`, so a source that says that itself, a connection
  closed early, would otherwise be sealed as a whole file; and a reader's
  first error is its last, as `io.ReadFull` drops one that comes with a full
  buffer. The same on the way out tells a cut connection from a damaged
  chunk, so it is not retried as v1. A download moves its write deadline past
  the server's 30 seconds, by its size at 256 KiB a second at the least.
- **A channel's file is streamed, up to what its platform takes (F4c).**
  Each `FileReplier` declares `MaxFile`: Slack and WhatsApp 100 MiB, what a
  conversation keeps; Telegram 50,000,000 bytes, the Bot API's "50 MB" for a
  bot, decimal so safe either way; custom 25 MiB. `send_file` refuses past it,
  naming it, and the sender refuses a file queued before. A provider builds
  around the file, `Body ‖ file ‖ Stream.Tail`, so `Reply` stays pure, and
  the sender opens the file only as it posts, with an exact `Content-Length`,
  so a store that hands back less fails the request. Custom keeps its file
  whole (`WholeFiles`): its signature covers the body, so streaming it reads
  the file twice and hands a receiver a JSON document of about 140 MB. Telegram's
  boundary is a hash of the bot token and the reply id, no longer of the
  bytes: the same on every attempt, and unknowable to whoever wrote the file.
  A file reply's lease is pushed out once, before it is sent, to the claim's
  two minutes and the file at 256 KiB a second (`ExtendReplyLease`); its post,
  or its upload, is given the file's time the same way. WhatsApp's upload
  goes through whatsmeow's `UploadReader`, which encrypts into a temporary
  file, as the whole must be hashed before it is uploaded.
- **A tool's file is typed by its bytes.** The type stored is what the bytes
  sniff as, never the server's word, and so is the extension a name lacks.
  Any type is kept, unlike a person's: one sniffed as nothing better is
  `application/octet-stream`, served as a download under `nosniff`.
- **An MCP answer is capped at 40 MiB, and a worker reads one large answer
  at a time.** An answer decodes at about nine times its size (measured: a
  100 MiB file peaked a client at 870 MiB to 1 GiB), so files larger than
  25 MiB are F4's: written in the room, read through its file API. An answer
  past 40 MiB, such as two 25 MiB files, fails its call with the cap named.
  A POST answer past 1 MiB waits for the worker's one slot
  (`runtime/capped.go`), within its call's five minutes, and gives it back
  when it is read to its end or closed; the files it decoded live on while
  the keeper stores them, so a worker holds a little more than one large
  answer. The cap counts a GET stream's whole life too, which carries a
  resumed answer; a per-event cap for long streams is a follow-up.
- **What Postgres cannot include in a transaction gets a tombstone, not a
  best-effort call.** A cascade does not reach a bucket or a secret store, and
  the two writes can never both be atomic. What is atomic is recording the
  intent: a trigger writes a row to `deleted_objects` or `deleted_secrets` in
  the same transaction that removes the row it belonged to, and `brain reap`
  converges the other system afterwards. A transactional outbox, which makes an
  erasure something that has been *recorded* rather than something that was
  attempted. `internal/reaper` is the loop; each destination is an `Effect`.
- **The tombstone is written by a trigger, because a cascade never runs our
  SQL.** Deleting a team removes its attachments through channels, sessions and
  messages without any Go code seeing a row — and deleting a team is the
  commonest reason an erasure is owed. An outbox written by our own delete
  statements would catch nothing. The triggers are statement-level with
  transition tables, so deleting a team with ten thousand attachments is one
  insert rather than ten thousand invocations.
- **The effect happens first, the tombstone is cleared second.** A crash
  between them repeats the effect, which every effect tolerates: deleting an
  absent object or an absent secret is a no-op everywhere. Clearing first and
  crashing loses the record while the data survives, which is the original bug
  with extra steps. Destroy the record of work last.
- **A tombstone holds no personal data and has no foreign key.** Two uuids and
  a path. A filename in there would make it a list of the files people asked to
  have deleted, kept because they asked. A reference to `teams` would cascade
  away the record of the work still to do, at the exact moment the work is
  owed.
- **The alarm is the age of the oldest tombstone, never the count.** Nine
  hundred a minute old is a busy delete; one a day old is a sweep failing every
  run. Nothing else about a deleted row looks different depending on whether
  the other system caught up, so this is the only signal there is — and `brain
  reap` exits non-zero on it, because a failed job is seen and a log line is
  not.
- **A secret is destroyed by the sweep, not by the handler that deleted the
  row.** `DELETE /teams/{id}/channels/{channelID}` used to delete the signing
  secret itself and log when it failed, which left a live credential behind
  with a log line in front of it — and left every channel's secret behind when
  a *team* was deleted, because a cascade never reaches a handler.
- **Destroying a team's key is the half of an erasure that does not wait.**
  Secrets sweep before objects: once `teams/<id>/attachments` is gone every
  object of that team is undecryptable, including copies in bucket backups no
  sweep can reach. In OpenBao this is a delete of the *metadata* path — the
  data path only hides the latest version, and a secret somebody can undelete
  is a secret that was not erased.
- **`attachments.team_id` exists because a trigger cannot join for it.**
  Measured: during a team cascade the `sessions` row is already gone when the
  attachments trigger fires, so the tombstone would lose the one field the
  sweep cannot work without. Third copy of that fact and the same mechanism as
  the other two — a composite foreign key onto `sessions (id, team_id)` means
  it cannot drift.
- **Two tombstone tables, not one with a `kind` column.** They carry different
  columns today and would carry different constraints tomorrow, and a shared
  table forces the stricter of two sets of rules onto both. The *loop* is
  shared; the tables are not.
- **The sweep has no ordering and must not grow one.** `FOR UPDATE SKIP LOCKED`
  hands different rows to different sweepers in any order, which is safe only
  because deletes commute. An outbox for something that does not commute —
  outbound replies to one thread — needs a partition key and one worker per
  partition, and would be a second mechanism sharing a word rather than a third
  `Effect`.
- **A job registers in one phase and is scheduled in another, because the
  runtime says so.** DBOS builds its workflow and queue registries in memory
  and reads them at `Launch`, so declaring has to happen before; installing a
  schedule needs a running scheduler, so that has to happen after. `Job` is
  `Name()` plus `Register(dbos.Context) ([]dbos.ScheduleSpec, error)` — it
  declares, and hands back what it wants scheduled. `internal/worker` knows the
  interface and no job; `newJobs` in `cmd/brain/worker.go` is the index, one
  line each, and is a function of its own so "which jobs does this
  configuration run" can be tested without a durable runtime.
  Structural typing means a domain never imports `internal/worker` back.
- **A job lives with its module, not in a jobs package.** `internal/reaper/job.go`
  today, `internal/runtime/job.go` when agent runs land. A separate package
  would have to import every domain and call into it, so each would export its
  guts to one consumer — and the "see every background job in one place"
  argument is already answered by the `worker.New(...)` call. Same answer
  `internal/api/<domain>` and `internal/gateway/<provider>` already got.
- **`ApplySchedules`, never `CreateSchedule`.** The second is not idempotent:
  a worker restarting against its own schedule gets `duplicate key value
  violates unique constraint "workflow_schedules_schedule_name_key"`. Measured,
  and it only appears on the second run — the first deploy looks fine.
- **The worker sweeps once at startup, and an overdue backlog does not stop
  it.** The startup sweep proves the secret store can delete and the object
  store exists before the process calls itself healthy, and covers the interval
  a newly installed schedule has no missed ticks to backfill. But `ErrOverdue`
  must not be fatal there: refusing to start would deadlock the only thing that
  can clear the backlog. Same error, opposite handling from `brain reap`, whose
  exit code *is* its report.
- **A deploy leaves a run's workflow PENDING, on a version the next build
  runs.** Two DBOS defaults stranded every long run on staging. The worker
  made DBOS's context from the signal context, so SIGTERM reached each
  workflow as a cancellation, which DBOS records as CANCELLED for good; it
  skips that only when its own `Shutdown` stops the workflow. The context is
  now `context.WithoutCancel(ctx)`, and `Shutdown` does the stopping. And
  DBOS's default version is a hash of the binary, while a worker dequeues only
  its own version, so a run the sweep resumed after a deploy sat ENQUEUED for
  good. `appVersion` in `internal/worker` is fixed instead: a run's workflow is
  one step that rebuilds the run from its rows, so any build can carry it on.
  - **Each worker makes its version the latest.** The API enqueues with no
    version, and DBOS hands that only to a worker of the latest version, the
    highest `version_timestamp`. That is set when a version is first
    registered and moved to now by `SetLatestApplicationVersion`, which `Run`
    calls with the context's own version (`DBOS__APPVERSION` overrides the
    config). Without it, one launch of any other build would take every new
    run for good.
  - **A rollback to a build from before this** launches under a hash, becomes
    the latest and takes the API's new runs. What waits under
    `openarity-brain-v1` waits untouched, and is taken up when this build is
    deployed again, with no restamp.
  - **Other jobs' workflows are left PENDING by a stop.** A sweep, purge, drain
    or reply workflow stopped mid-step stays PENDING under the dead worker's
    executor id. Only runs are taken over, by the run sweep, and executor ids
    are random per process, so nothing else ever clears those rows: a few
    gather per deploy. They block nothing, as both queues limit concurrency
    per worker only, and the next tick or reply sweep redoes their work.
    Never cancel `openarity-runs` rows to tidy up: a cancelled run is never
    taken up again.
  - **It rests on a DBOS internal,** the cause its shutdown cancels with. The
    Postgres tests in `internal/worker/shutdown_test.go` hold it, with
    `TestTheWorkerKeepsOneVersionAcrossBuilds`; run them with `db=` on every
    DBOS upgrade.
  - **Restamping** is needed after the first deploy of this change, after
    any change to `appVersion`, and after a rollback to an older build and
    forward again (the older build stamps new runs with its hash), because a workflow keeps the version it was
    enqueued with and neither a resume nor a second enqueue restamps it. There
    is no operations page yet, so the steps live here:
    1. Deploy, and make sure no older worker still runs.
    2. Wait three minutes: a run the old build cancelled has no queue until
       the run sweep takes it over (`staleAfter`, 90s) and resumes it (the
       `runtime-sweep` schedule, every minute).
    3. Count what is left on another version, restamp it, and count again
       until nothing is. A row with no version is the API's and is normal:
       the latest version's worker takes it, so neither query touches it. Where `DBOS__APPVERSION` is set, use its value; better,
       do not set it in a deployment.

    ```sql
    SELECT application_version, status, count(*) FROM dbos.workflow_status
     WHERE queue_name = 'openarity-runs' AND status IN ('ENQUEUED', 'PENDING')
       AND application_version <> 'openarity-brain-v1'
     GROUP BY 1, 2 ORDER BY 1, 2;

    UPDATE dbos.workflow_status SET application_version = 'openarity-brain-v1'
     WHERE queue_name = 'openarity-runs' AND status IN ('ENQUEUED', 'PENDING')
       AND application_version <> 'openarity-brain-v1';
    ```

    Leave the other queues: the next tick or sweep redoes their work.
- **`brain migrate up` creates DBOS's tables as well as ours.** DBOS keeps
  eleven tables under `dbos` and migrates them itself, with no lock: `serve`
  and a worker starting together on a fresh database failed with `duplicate
  key value violates unique constraint "pg_type_typname_nsp_index"`. The
  migrate init container runs before a pod's server does, and takes a lock of
  its own around DBOS's step, so both find the tables current, which DBOS
  checks without writing. Both still run that check, so a database
  that skipped `migrate up` works too, with the race back. That is the price
  of a library instead of a cluster, and worth knowing before debugging a
  schema diff.
- **A deployment adds a loop in `runtime.Patterns`, and nowhere else.** The
  runner, `GET /teams/{id}/loops` and the check on an agent's `pattern` all
  read the names from there; the database only checks that a name is well
  formed, so a new loop needs no migration.
- **The cron expression has six fields.** DBOS builds its parser with seconds
  enabled, so `*/15 * * * *` is refused with "expected exactly 6 fields, found
  5". A test parses the constant with the same parser, because the refusal
  would otherwise land at worker start rather than in CI.
- **The tombstones are an outbox, not a queue, and nothing may be built on them
  as if they were.** A queue row means "run this job with these arguments"; a
  tombstone means "Postgres has committed a change another system has not caught
  up to yet." `deleted_objects` holds a key, a team id, a timestamp and an
  attempt count — no payload, no job type, no dead-letter table, no routing, one
  consumer forever. That is exactly what makes replay free and `SKIP LOCKED`
  safe, and exactly why an agent runtime cannot sit on it. When one arrives it
  needs a real queue with two properties: it enqueues inside the transaction
  that writes the message, and a crash mid-run does not re-pay the tokens. No
  Postgres job queue has the second; Temporal cannot have the first, because
  starting a workflow is an RPC rather than a row — it sits behind an outbox
  rather than replacing one. `deployment/SCHEDULING.md` carries the measurements.
- **A sandbox room has one owner, and rooms are kept by one package that knows
  nothing of what runs in them.** `sandbox_rooms` holds one row per
  `(owner_kind, owner_id)`; a command MCP server is the first owner kind, and
  agent code and skill scripts will be others, each added to the CHECK by its
  own migration with a trigger on its own table that deletes its rooms. The
  keeper (`internal/sandbox/keeper`) owns everything that makes a room
  correct: a Postgres advisory lock per owner, so concurrent callers in every
  brain wait for one cold start; a hash of what the room was made from; the
  health wait; the token. `mcproom` is the thin caller that says what a
  command server needs. A caller in one brain waits on a mutex in the process
  and not on the database lock, which holds a pooled connection for as long as
  a cold start takes.
- **A room is deleted with the credentials that made it.** The row keeps the
  provider, its address and the id of the key; deleting a row (a deleted
  owner, a replaced room, an idle one, a team that changed provider, address
  or key) writes a `deleted_rooms` tombstone carrying them, and the reaper's
  `rooms` effect deletes the room with that key. A key's own tombstone is not
  claimed while any live row or owed tombstone names it: otherwise the room
  would be left at a provider nothing can authenticate to. A room's token is
  `teams/<team>/sandbox/rooms/<row id>`: the row id is new for every room, so
  a tombstoned token can never be a live room's.
- **The brain chooses a room's image, a team never names one.** sandboxd and
  Daytona start from `openarity-sandbox:sha-<the brain's commit>` (the image
  build passes it as `REVISION`, since its context has no `.git`), or `:latest`
  when the commit is unknown; `OPENARITY_SANDBOX_IMAGE` overrides it. E2B alone
  starts from a template in the team's account, so the brain builds the same
  image there as `openarity-sandbox-<commit12>` (the keeper's `Templates`),
  and a team names a template only to replace it. A row per (team, sandbox
  key, name): a template lives in the account the key belongs to. A room's
  start waits for no build: while one runs the caller gets
  `sandbox.ErrPreparing` and tries again; the worker reads builds back every
  thirty seconds, fails one past thirty minutes, and a failed one is retried
  after ten minutes or at once on a save. The poll is a job of its own, not a
  reaper effect: the reaper's backlog means erasures owed, and a build is not
  one.
- **A session's workspace is stopped, not deleted, when its last run ends.**
  A room that sat idle for thirty minutes held its memory the whole time, and
  on a small sandboxd that refused the next conversation's room ("sandbox is
  busy"). So `Executor.finish` stops the room when no other run of the session
  is queued or running, the reaper stops one left running past the team's
  idle minutes (or `OPENARITY_SANDBOX_IDLE`), and a stopped room is deleted
  after the team's keep hours (24 by default). The next call resumes it with
  its files: `/workspace` is a volume on sandboxd, E2B pauses, Daytona stops.
  What follows:
  - **A waiting run does not hold the room.** A run waiting for a person may
    wait hours; its room is stopped meanwhile and resumed when it calls again.
    `IdleSessionOf` and `SessionIsIdle` leave `waiting` out on purpose.
  - **A call refused for want of room waits for room** (`room_waits`,
    `runtime/roomwait.go`). On staging, 12 runs in 30 days had a workspace
    call refused this way (29 refusals for a full sandbox, 11 for an
    unreachable sandboxd); each went on without its workspace, and 2 of the
    9 judged were not done or partly. Now a refusal `sandbox.Waitable` names
    (full, preparing, `ErrUnavailable`) makes nothing: the run is told to
    say it waits and stop, and parks with `waiting_for = 'sandbox'`. The
    sweep makes the room itself, a minute after the refusal and then after
    as long as the run has waited, at most two minutes apart (ten noticed a
    sandbox back, such as a laptop woken, up to ten minutes late; a try is one
    `Ensure` and no model turn), and queues the
    root once it is made, or at the first try after the wait is an hour old,
    so a try costs no
    model turn and a delegate's parent is not woken to hear it still
    waits. Only what nothing ran for waits: a command killed inside its room,
    sandboxd's own 503 (an image it cannot pull), a room that started and
    did not answer (`ErrNotReady`, whatever it wraps), a room lost mid-call,
    and a transport failure that is not one of no answer (a refused
    certificate) stay the call's error. A wait opens under the run's session
    lock and only while it runs, so a cancel or a person writing again
    cannot leave one open behind them; the model and the wait carry the
    refusal in a few words, and the provider's own, which can name an
    internal address, goes to the log. The sweep tries rooms after it has
    restarted queued runs, and starts tries for two minutes at most. A person writing again ends the wait, as it gives
    up their requests; a tree that ends cancels it. Command MCP servers in
    the sandbox do not wait yet: none met these in the same 30 days.
  - **A stop re-checks idleness under the owner's locks**, so a run that
    starts between the check and the stop is never left without its room.
  - **A resume refused for room (`ErrFull`) leaves the room stopped** and
    says so. A workspace found gone, refused, over its disk, or not answering
    after a resume is replaced, without its files, and the call that found it
    returns `sandbox.ErrReplaced`, so the model is told and does not work on
    as if the files were there. Failing every call until the keep hours end
    was the other choice; it keeps files nobody can reach and leaves the
    conversation without a workspace for up to 30 days.
  - **A deploy replaces every workspace.** The image is part of a room's
    spec hash and is named for the release's commit, so a new commit gives
    each conversation a new room, without its files, and the model is told.
    Leaving the image out of a workspace's hash would keep files across a
    deploy, at the price of rooms that run an old workspace server for as
    long as they are used; carrying `/workspace` across an upgrade is a
    follow-up, not this.
  - **A running workspace that does not answer is resumed once before it is
    replaced:** the provider may have stopped it without the row hearing (a
    mark that failed, a run's end cut short, sandboxd's disk watcher).
  - **The idle sweep lists rooms in no fixed order and skips busy sessions**,
    so rooms that cannot be stopped now never take every place in a sweep.
  - **Size is a team setting, and part of the room's hash.** A team chooses
    memory, CPUs and disk, all three or none; a change gives its sessions new
    rooms. sandboxd refuses a size past what its operator gives a workspace;
    Daytona takes whole GB, rounded up.
  - **E2B builds a template per size.** E2B fixes a sandbox's CPUs, memory
    and disk in its template, so a team with a size gets
    `openarity-sandbox-<commit12>-<c>c-<m>m-<d>d` built in its account
    beside the default, which its command servers keep using. The row
    carries the size (`e2b_templates.size`), so a room waiting for its
    release's template falls back only to an older one of the same size:
    never another size, which would hand a workspace the wrong memory. Disk
    is `minFreeDiskMb`, the free space E2B grows the filesystem to, best
    effort. Whether a size is allowed is the team's E2B plan's to say; a
    refusal is a failed build with E2B's reason, not a check here. The
    template's name and the row's size are both derived by one function,
    `sizeKey`, and the table checks the size's shape. A team that names
    its own template keeps that template's size: the team's is not used,
    so changing it replaces no room. Two teams that saved the same E2B key
    share an account, so they share a name per release and size; E2B adds
    the second team's build to the template of that name, from the same
    image at the same size, and each team's row follows its own build.
    Nothing deletes an older release's templates yet: each release adds one
    per size to the account, which a follow-up is to prune.
  - **sandboxd's disk is a soft cap.** Docker cannot cap a volume on every
    storage driver, so sandboxd measures volumes every 30 seconds and stops a
    room past its disk, and counts disk against `SANDBOXD_DISK_BUDGET`.
- **The scheduler is a seam because scheduling has no specification.** Auth is a
  port over OIDC, so authentik is swappable for Dex or Okta by changing one
  variable. River, Temporal, DBOS and Restate agree on no model at all, and an
  interface all four satisfy collapses to "run this function" — which discards
  the replay that was the reason to want Temporal. So the adapters are ours, on
  the `internal/gateway` pattern, and the heavy option is never mandatory: a
  2 GB host runs a ticker, a cluster runs what it already operates.
- **`MaxAttachment` is a constant, and not only for politeness.** `gcm.Seal`
  panics rather than erroring above `(2^32-2)*16` bytes, and an attachment is
  held whole in memory twice while it is sealed. A test asserts the constant
  stays under both ceilings, so raising it carelessly fails instead of
  crashing. Promoting it to configuration would need a validated ceiling.
- **The policy grants `update` on the attachment key path, and that is not
  redundant next to `create`.** Without it the loser of that race is refused by
  the policy (403) rather than by check-and-set (400), and cannot tell "somebody
  beat me" from "the policy is wrong". A root token returns 400 either way, so
  no test using a convenient credential can see this — the policy test builds
  its AppRole from the file that ships.
- **An attachment has no permission of its own.** It is reached through the
  session that holds it and uses that session's check — the same `visible` the
  message route uses. An `attachment:read` permission could be granted to a
  role without `session:read`, and then somebody who cannot open a private
  conversation could still read the file sent in it. Deriving the check makes
  that combination unrepresentable rather than merely unconfigured. The rule
  generalises: a permission for a child resource is a bug when the child is
  only reachable through its parent.
- **The read path serves the recorded type and nothing else.** `Content-Type`
  is `attachments.media_type` exactly, with `X-Content-Type-Options: nosniff`,
  never a type derived from the filename. Sniffing at write time is a claim;
  this is what makes it true. An SVG carrying a script sniffs as `text/plain`
  and is inert only while it is served as that.
- **`Content-Disposition: inline` is an allow list, and PDF is not on it.**
  Only `image/png`, `image/jpeg`, `image/gif`, `image/webp` and `text/plain`
  render in place, because each is inert as itself under nosniff. A same-origin
  PDF runs JavaScript in every major viewer, and a zip gains nothing from
  opening. Everything else downloads.
- **The brain proxies the bytes; there is no signed URL.** The object store
  holds ciphertext under a key it does not have, so a URL to it would serve
  something unreadable. Decryption happens in the process that already knows
  which team the caller was allowed to read, and the team comes from the
  session rather than from the object key — a string on a row.
- **A refusal has to do nothing, not merely answer nothing.** `visible` writes
  its 404 and returns; a handler that ignored its second return value would
  still answer 404, because the status is already committed and `WriteHeader`
  only fires once. The assertion that catches it is that the queries never ran.
  Status codes cannot detect work done after a refusal.
- **A write resolves the name it was given; it does not send the caller to a
  directory first.** `POST /teams/{id}/members` takes `user_id` or `subject`,
  because requiring the id would mean requiring `GET /users`, which needs
  `user:read` *somewhere* — a much larger authority than adding one person you
  can already name. The question to ask of any reference in a
  request body is what permission the caller needs to produce it.
- **Permissions are data; scopes are code.** Adding a permission, creating a
  role, or pointing a route at a different permission is rows, not a deploy —
  which is what lets an enterprise compose roles in a dashboard. Adding a
  *scope* is code, because each one is a different check.
  `internal/store/rbac.json` is the product's default catalogue, applied by
  `brain migrate up`: permissions are upserted and never deleted, only the
  roles named in the file are touched, and routes are replaced wholesale.
  `role_permissions.action` and `route_permissions.permission` are foreign keys
  onto `permissions`, so a typo is a rejected write rather than a grant that
  silently means nothing.
- **An adapter names attachments; it never fetches them.** `Parse` is pure and
  offline, so a payload that carries a file carries a `Ref` and the claimed
  filename, type and size — every one of them attacker-chosen, which is why
  they are spelled `Claimed`. Downloading needs a credential and a network
  call, so it happens afterwards through the optional `Fetcher` interface,
  after the handler has already resolved `Keys()`. Optional because a provider
  with no concept of an attachment has nothing to implement, and a stub
  returning `nil, nil` is indistinguishable from an empty file.
- **`FetchAttachment` is given the request, not only the ref.** The obvious
  signature takes a ref and a credential, which is enough for Slack — its refs
  are file ids you hand back to its API. It is not enough for a provider whose
  bytes arrive inside the delivery itself, where the ref means nothing without
  the body it came in. That only surfaced because `custom` ships: three
  adapters that all download from a server would have agreed on the narrower
  signature and been wrong together.
- **A ref is unique within a delivery, and the suite enforces it.**
  `FetchAttachment` is given a ref and nothing else, so two attachments sharing
  one means the second resolves to the first one's bytes and is stored under
  the second one's filename and media type. That ingest returns success and
  nothing downstream can tell. Both halves were individually correct — `Parse`
  produced valid refs, `FetchAttachment` resolved one it was handed — so the
  contract between them had to be written down before anything could check it.
  In `custom` the collision has two routes: two equal ids, and an id equal to
  another attachment's `#<index>` fallback, so the check is on the computed ref
  rather than on the id.
- **Attachments are fetched inline, and the ack window is what sizes the
  budget.** Two seconds for a delivery and one for a file, because Slack allows
  three to answer a webhook — not because that is what a download needs. Inline
  is then safe by construction: it cannot blow an ack. A file that does not fit
  is dropped with a log line, and that line is the trigger for building the
  queue rather than for raising the constant. Ack-first is the better design
  and needs a durable queue; building one to store a photo is the wrong order.
- **A failed fetch loses the file; a failed row loses the request.** A file
  that 404s would 404 on every retry, so the message is stored without it and
  the provider gets a 200. A row that will not write happens *after* the bytes
  reached the bucket, so it is a 500 — a 200 there leaves an object nothing
  names and a message nobody has.
- **`InsertMessage` returns an id and `ErrNoRows` means replay.** Attachments
  hang off a message id, and the obvious way to always get one back —
  `ON CONFLICT DO UPDATE SET external_id = EXCLUDED.external_id` — writes a new
  row version even when nothing changed: 200 replays of one row took the heap
  from 8 kB to 16 kB on 18.6. Replays are the hot case on a webhook. `DO
  NOTHING ... RETURNING id` gives the id when there is work and nothing when
  there is not, which is exactly the question the caller is asking.
- **The object key is a fresh id, never a hash of the content.** Content
  addressing would let identical files share an object, which is what
  `CountObjectReferences` anticipates — but it also tells anyone who can
  list the bucket that two teams hold the same file, and lets them confirm a
  guessed file is present by hashing it. That is the property the encryption
  exists to remove.
- **A filename is cleaned where the sender is not trusted, which is always.**
  Directory components are stripped, control characters become spaces, and the
  result is clipped to the 512 the column allows. The name is only ever echoed
  in a `Content-Disposition`, but a value that still looks like a path invites
  the first caller that joins it onto one.
- **A channel adapter receives values, never capabilities.** It gets a
  `gateway.WebhookRequest` rather than the `*http.Request`, `Credentials`
  rather than `secrets.Store`, and a `ReceivedAt` rather than the clock. So it
  cannot hijack the connection, read another channel's signing secret, or be
  untestable. Adapters are the code most likely to be wrong and least likely to
  be reviewed closely, and they sit on a public unauthenticated URL — what is
  left after taking the authority away is a function from bytes to structs,
  which can be tested exhaustively without being trusted. `Keys()` is the
  mechanism: the adapter declares which secrets it needs and the handler
  fetches them, scoped to the channel in the URL. See
  `internal/gateway/AGENTS.md`.
- **A channel has sessions; a session has messages.** A session is one
  conversation, and the adapter chooses what that means through `Session.Ref` —
  a provider gives you identity, never episode. It belongs to a *team* rather
  than to a channel, because a channel is only one way a session starts and
  because a workspace or a sandbox will hang off it. `sessions` carries
  `status` and `last_message_at`. Every message bumps `last_message_at`, which
  orders the lists and their cursor. `status` is still only `open`, though the
  lists already filter on it: a Slack thread ends on its own, a direct message
  never does, and an idle sweep is the only thing that can tell them apart. The partial unique index is what lets that land as
  an UPDATE rather than a redesign. See `internal/gateway/AGENTS.md`.
- **A direct session belongs to one person; the check is a permission, not a
  role.** `group` and `thread` sessions are the team's, `direct` is its
  participant's, and `session:read_all` is what lets anyone else read one. The
  handler asks `Can` for that permission rather than testing for `"admin"`,
  because which role holds it is data in `rbac.json` — a role name in Go would
  put that mapping back into a deploy.

  The guard authorises reaching the route, and the handler decides which rows
  come back. Several handlers ask `Can(session:read_all)` for that: the
  session lists, a team's run history, a channel's 24-hour session count and
  the team summary. They filter in SQL, and the single-session read filters in
  Go, so both have to agree — which is why the mutation pass breaks each
  independently.
- **The reference adapter ships; it is not a fake.** `internal/gateway/custom`
  is a real generic webhook anyone can integrate against, and it runs the same
  `providertest` suite as every other adapter. A test double is written by the
  same person, at the same time, with the same misunderstandings as the thing
  it validates, so it agrees by construction — a second real implementation is
  what proves an abstraction rather than the code.
- **The guard wraps every route; it is never a per-route decorator.**
  `Router.Register` takes an `api.RouteGuard` and there is no way to mount a
  handler without it. A decorator can be forgotten, and a forgotten one is an
  open endpoint that passes every test written about its body. A protected
  route with no row in `route_permissions` panics at startup naming the route;
  rows for routes nothing serves fail `serve` with the same specificity.
- **`Can` for a team, `CanInAnyTeam` for a route with no team in it.** The
  second is strictly weaker — an admin of one team passes it — so a
  team-scoped route using it would turn one admin role into an admin role
  everywhere. This is now a property of the *scope* rather than of the calling
  package: `any_team` on a route with `{id}` in its path is refused by a test
  in `internal/store`.
- **`member` is not `team` with a permission everyone holds.** Belonging is a
  fact and cannot be revoked or forgotten; a grant is configuration and can be
  both. Collapsing them would make "a member who cannot open their own team"
  reachable by omitting one line from a role in a dashboard. `member` also
  costs no query — it reads memberships already on the request — and denies
  with 404 rather than 403, because a team you do not belong to should not be
  confirmed to exist.
- **A subject is not unique.** `users` is unique on `(issuer, subject)`, so
  anything keyed on subject alone can match more than one person and must
  answer rather than pick. With one provider configured it never fires, which
  is exactly why it has to be handled when it is written rather than when it
  happens.
- **`slog` from the stdlib.** No zap, no zerolog. Text with source locations in
  development because a terminal is the reader; JSON everywhere else because a
  log aggregator is. `AddSource` costs a stack walk per record, so development
  only.
- **`/healthz` and `/readyz` are not logged.** Kubernetes probes them every ten
  seconds on two listeners — roughly 17k lines a day that say nothing and bury
  everything else. The skip lives in the middleware, matches the path exactly,
  and must still let the response through untouched.
- **Liveness never checks a dependency; readiness always does.** `/healthz`
  answers 200 with Postgres on fire — failing it restarts every pod at once,
  which fixes nothing and adds a reconnect storm to the outage. `/readyz` pings
  the database and returns 503, which takes the pod out of the Service and
  restarts nothing. Both routes go on both listeners: `kubelet` probes the pod
  IP, so the loopback API listener is unreachable to it and the webhook copies
  are the ones Kubernetes actually uses.
- **pgx, sqlc and goose. No ORM.** Three tools, one job each, none hiding the
  database. The queue needs `FOR UPDATE SKIP LOCKED`, the runtime tables need
  `jsonb` operators, and graph RAG will need CTEs — all natural in SQL and all
  awkward through an ORM. sqlc's weakness is dynamic queries; write the two or
  three real shapes as named queries rather than reaching for a builder.
- **`pgxpool` is lazy.** `New` dials nothing — a stopped database, a wrong host
  and a wrong password all return a working pool and a nil error. `Ping` is the
  only proof, which is why `run` calls it before anything else starts.
- **Pool settings live in code, not the DSN.** `pool_max_conns` and friends in
  a connection string are silently overridden by `applyPoolDefaults`. One place
  decides, and it is greppable. pgx's own default derives `MaxConns` from
  `NumCPU`, so the same image would open 8 connections on one node and 64 on
  another.
- **Migrations are embedded and applied by the binary.** `brain migrate up`,
  never the `goose` CLI against a real database — the CLI reads files from
  disk while the binary carries its own copy, and the two drift silently. In
  Kubernetes it is an `initContainer` on the brain Deployment's pods
  (`deployment/k8s/deployment.yaml`), never a step inside `run`. Every replica
  runs it, and that is safe only because every step of it serialises on a
  Postgres advisory lock: goose's (`migrationLockID`, a session lock goose
  polls every 5 s for up to 5 minutes, so a second pod waits and then finds
  nothing to apply; a migration longer than that fails the waiting pods'
  init containers, which Kubernetes restarts), the RBAC catalogue
  load's (`rbacLockID`, held for its transaction) and DBOS's
  (`dbosMigrationLockID`, retried every 250 ms). A Job would have run it once,
  but it is a second object to order against the rollout, and an init
  container gives the property that matters for free: a pod cannot start
  serving against a schema it has not applied. The price is that a pod
  can wait on the lock, and that old pods keep serving the previous schema while
  a new one migrates, so a migration must be expand-then-contract. The
  worker (`deployment/k8s/worker.yaml`) has no init container on purpose: the
  schema has one owner.
- **The migration advisory lock is project-specific.** goose's `DefaultLockID`
  is crc32 of the string `"goose"` and is therefore shared by every goose user;
  Postgres advisory locks are scoped per database, not per schema. Ours is
  crc32 of `"openarity"`. Locking is also off unless `WithSessionLocker` is
  passed — the default is no lock at all.
- **The rules a harness is held to live below `internal/api`.** An agent's
  spec and validation, its row and grants, and a skill's write are in
  `internal/harness`; a proposed change to them (its kinds, checks, apply and
  revert) is in `internal/proposals`. The agent, skill, blueprint and
  improvement routes are handlers over them, and the improver, a background
  job, imports them without importing a route (#277). Before, the improver
  reached the rules through three handler packages, so a handler change
  could break a job that serves no request.
- **The judge and the improver read a tool's result by both its ends.** A
  run's evidence shows each result, and each tool error, as its first and
  last 200 characters (`resultChars`), since a long command's error is in
  its last lines. A Bash result reads by how the command ended: `Bash timed
  out` when it begins with workroom's `TimedOutNote` (it also ends with an
  exit line, so this is read first); `Bash returned (output cut)` when the
  stored output lost its tail (`truncated`, past the 64 KiB an event keeps),
  as its exit line went with it; `Bash failed (exit N)` when its whole last
  line is workroom's `ExitLine`. The runtime writes both markers and the
  improver reads them through `workroom`, and a test reads back every exit
  and timeout `execText` writes, so neither side can change them alone. A
  command whose own last line is exactly `exit code N` reads as failed; that
  is accepted. A pipe still hides a failure, as its status is its last
  command's: Bash's description says to use `set -o pipefail` or
  `${PIPESTATUS[0]}` when the status matters. It is not turned on for every
  command, as `grep` finding nothing and `head` closing a pipe early would
  read as failures.
- **A blueprint writes through the domain packages' own code, in one
  transaction.** `internal/api/blueprints` turns each item into the request
  its own API takes and runs `harness.Validate`, `mcpservers.Parse` and
  `skill.Assemble` on it, then writes with `harness.Regrant` and
  `harness.Create`/`Replace`, so a rule changed in one place holds for both.
  Its routes are member-wide and the apply handler checks `tool:write`,
  `skill:write` and `agent:write` for what the plan writes: one permission on
  the route would either let a blueprint write what its holder could not, or
  refuse one that writes only what they can. Apply plans again and refuses
  (409, with the new plan) unless the plan's digest is the one the caller was
  shown, so nobody applies a plan they did not see. Only changed items are
  written, so a second apply leaves no outbox row and no `updated_at` moved.
- **The builder proposes; only a person applies.** A builder run is the
  team's built-in builder (`runs.builder`), never also the hub, and its tools
  are `graph`, `team`, `find_mcp_servers`, `list_mcp_tools`, `propose` and the
  web tools: no write, delegate, MCP tool call or skill, so text it reads can
  change what it proposes but never the team. A person reads every proposal,
  so the web is worth its injection risk here; the card names each new host.
- **The builder is an agent on the dashboard channel.** Every message has
  its channel, so a conversation with the builder is a dashboard conversation
  (`sessions.builder`) and its runs are started by its messages, like any
  other; `POST /runs` refuses `builder`. Who answers is fixed when the
  conversation starts. The builder is the one agent a person talks to
  directly; every other agent is reached through the hub. Builder sessions
  made before had no channel and moved onto their team's dashboard channel;
  the rollback leaves them there, since they may by then hold messages.
- **`list_mcp_tools` sends a secret only where the team already does.** It
  discovers a server that need not be the team's, so a reference is sent only
  when the team has a server at that exact URL with that exact secret.
  Otherwise a page the builder read could name a host and a team secret, and
  the brain would carry the token there itself.
- **A run a person is waiting on streams; a delegated one does not.** The
  builder and every top-level run take a second runner whose loops stream
  (each loop's streaming variant), and the recorder gathers the deltas into
  `text` events, written at 512 bytes, every 250 ms, and before any other
  event, so the text and the call it led to stay in order and a reply is tens
  of rows, not thousands. Postgres is the bus because the worker and the API
  are separate processes; the SSE route already polls it. A delegated run's
  writing is its parent's to report.
- **Signing in to an MCP server is the brain's, never the SDK's.** The agent
  loop authorises nothing, and OAuth is all state it lacks: secrets, a
  callback a browser is sent to, a person, a lock across workers. Discovery
  (`internal/discovery/oauth.go`) follows the MCP authorization spec with the
  go-sdk's `oauthex` and `auth.GetAuthServerMetadata`, and `x/oauth2` for the
  code exchange; the SDK's `AuthorizationCodeHandler` is not used, because it
  blocks on a browser a worker does not have. Measured against Gmail's server
  rather than assumed: it answers `initialize` with 200 before any sign-in, so
  no 401 is not "no sign-in" and the well-known paths are tried regardless;
  its metadata names `https://accounts.google.com/` with a slash, trimmed
  before the lookup; and it lists every Gmail scope, full mailbox access
  included, so scopes set on the server win over the discovered ones. A
  refresh is a form of its own because `x/oauth2` cannot name the `resource`
  on one, and only `invalid_grant` means signing in again: an authority's bad
  minute must not sign a team out of its servers.
- **A sender let in as someone new is a user of their own, not a role.**
  Approving names a member, or makes a channel user: issuer
  `openarity:channel`, subject `<channel>:<ref>`, which no token is ever
  issued for, added to the team with the role given. A role alone would make
  every guest one identity: one schedule cap, one sign-in, nobody removable
  alone, and `runs.user_id` meaning nothing for them. Moving a sender off a
  channel user, or removing it, deletes that user when it was their last
  sender, in the same transaction, since a user nobody can log in as and no
  sender speaks as is a member that can do nothing. It is a second statement,
  not a CTE: deleting the user cascades into the very binding the statement
  rewrites.
- **A role changes in place, and the last admin keeps theirs.** `PATCH
  /teams/{id}/members/{userID}` locks the team's row before it counts the
  admins, so two admins demoting each other at once leave one, not none. The
  transaction lives in `cmd/brain`, as `StartRun` does, with its errors in
  `internal/api/teams`.
- **Every run is told the date and time, in its message.** A model otherwise
  takes its training cutoff for today, and reads a date already past as one
  still to come. A run's first message starts `Now: <weekday, date, hh:mm>
  UTC.`, a delegate's brief too. Never the system prompt: every system block
  is cacheable, and a block that changed with the day would end the cached
  prefix there, so the conversation after it would miss. In the message, each
  turn keeps the time it was asked, which is also the truth. The message is
  pinned (`agent.Message.Pinned`), so a compaction that folds the turn sends
  it again after the summary: otherwise the only date left in view would be
  the conversation's first message, days old. It is the time the turn began;
  an agent that must count minutes needs a tool, not this line. UTC until a
  team has a timezone.
- **Thinking is recorded apart from talking.** What the SDK reports as
  reasoning, the model's own and a loop's plan or critique, is gathered into
  `reasoning` events the same way, and a switch between the two flushes, so
  no row holds both. Each row keeps its `step`, which is what names its
  message on the session stream (`<run_id>:<step>:text`). Splitting on the
  pattern instead would teach the stream every loop; splitting on what the
  provider labels keeps it in the SDK, where each provider's field names
  already are. The reasoning itself stays in the transcript through the SDK's
  store, and goes back to the model that wrote it: DeepSeek refuses a request
  with tools whose history leaves it out.
- **A chat is told the calls, not what the model wrote on the way to one.**
  A channel sending each step sends "Using `<tool>`…" and "Asking
  `<agent>`…", then the answer, never the text a step wrote before its call.
  That text streams before the model decides to call, so nothing can mark it
  as it arrives, and it is as often a note to itself ("Grants are empty; try
  listing root.") as words to the person. It is still the step's `text`
  event, so the trace and the session stream show it. A run that waits for
  approval says what it wants in its turn's last text, with no call after
  it, so that is its output and reaches the chat as the reply.
- **A channel run is told the word its channel answers to.** The trigger
  word decides whether a message is answered, but the message reaches the
  run as written, so a model read "@Fduv can you explain this?" as asking
  someone else. The run's first message, not the system prompt, says "In
  this chat you are called @Fduv", so the system blocks stay cacheable. The
  word stays in the text: stripping it would change what the person wrote,
  and what the session shows. A bot's own handle on Slack or Telegram is not
  told yet; the run does not hold it.
- **An MCP secret is written whole, and never read back.** `PUT
  /teams/{id}/mcp-secrets/{name}` takes every key of the secret and writes
  them with one `PutAll`: an OpenBao write replaces the keys a path held, so
  keys written one at a time would leave only the last. The values reach the
  secret store and nothing else; no route returns or reports them, so the
  dashboard can say a token is set only because a server names one.
- **An apply discovers what it wrote, after it commits.** Each created or
  changed server is asked for its tools through the MCP servers' own
  `Refresher`, four at a time, on a context the caller cannot cancel. A
  failure is recorded on the server and reported in `discovered`; it never
  undoes the apply, which is already the team's. `propose`
  plans without permissions and records the blueprint with that plan; reading
  an open proposal plans it again as the reader, and applying it is the
  blueprint apply as the caller, with the proposal marked applied in the same
  transaction so it is applied once. A newer proposal supersedes the session's
  open one in the same transaction (a partial unique index keeps one open);
  one statement cannot do both, since its insert is checked against the row
  before the update. Proposals follow their session's visibility.

## Skills, full descriptions

- **`add-env-var`** — adding any new environment variable. Covers the struct
  field, the enum type if the value is a fixed set, validation, redaction in
  `String()`, and the three test files. Use it every time; a field wired into
  four of the five places is the normal failure.
- **`add-middleware`** — anything that wraps an `http.Handler`: auth, request
  IDs, panic recovery, rate limits, body size limits. Covers the signature,
  wrapping the response writer, chain order, and wiring into `server.New`. Use
  it every time; a middleware that is built and never applied passes every
  linter and every test except the wiring one.
- **`authorise-a-route`** — anything about who may call what: adding a
  permission, choosing a route's scope, adding a role, or working out why a
  request came back 403 or 404. Covers the five scopes, `rbac.json`, what is
  live versus what needs a restart, and the checks that fail the boot. Read it
  before adding a route, and before touching `internal/authz`.
- **`add-a-backend`** — a new implementation behind one of the storage ports,
  a secret store or an object store. Covers the port, the adapter package, the
  enum value that names it, per-backend validation, the wiring in `cmd/brain`,
  and the tests that catch a switch case falling through. Use it whenever a new
  provider must be selectable by name.
- **`update-api-spec`** — `api/openapi.yaml` whenever an endpoint is added,
  changed or removed. Covers what belongs in the spec, its conventions, the
  keywords that wreck the generated client, how to validate it, the test that
  fails when it drifts from the routes, and when a change is breaking. Use it
  alongside `add-route` for every endpoint.
- **`add-route`** — every API endpoint, and every new domain package under
  `internal/api`. Covers where the route lives, the `Router` and its prefix,
  per-package dependencies, the response contract, which status each failure
  gets, and the five tests a route owes. Use it
  every time; a route registered on the wrong mux passes every test written
  about its body.
- **`add-a-background-job`** — anything that runs on a schedule or off a queue
  rather than on a request. Covers where a `Job` lives, the two phases the
  durable runtime forces on it, names that are stored state, the six-field
  cron, and the tests that catch a job which registers cleanly and never fires.
  Read it before adding work to `brain worker`.
- **`write-migration`** — every schema change. Covers the goose file format,
  `lock_timeout`, expand-contract for column changes, batched backfills, and
  when an index needs `CONCURRENTLY`. Use it every time: the migration that
  freezes a table in production is indistinguishable from a safe one when the
  table is empty.
- **`write-query`** — every query that reads or writes Postgres. Covers the
  sqlc annotations, regenerating and committing the output, type overrides,
  when a write needs `InTx`, and the batch and copy modes. Use it every time;
  never hand-write the Go that runs SQL.
- **`write-tests`** — every test, any package. Covers naming, when
  `t.Parallel()` is allowed, contexts and ports and timing, what a fake owes
  you, and step 7: break the thing and confirm the test fails. Read it before
  writing a concurrency test.
- **`fix-lint`** — any golangci-lint or gofumpt failure, and any change to
  `.golangci.yml`. Lists the linters that actually fire here and the correct
  fix for each. The fix is almost never a `nolint`.
- **`test-with-postgres`** — any test that needs a real database. Covers
  skipping when none is available, one schema per test, why these cannot be
  `t.Parallel()`, and how to check a test would actually fail. Read step 5
  before writing any concurrency test: we shipped one that passed with the
  guard removed.

## Conventions, with their reasons

- **Initialisms keep one case**: `URL`, `DSN`, `API`, `ID`, `DB`. Unexported
  names starting with one go all-lowercase: `redactURL`, but `urlParser`.
  `revive`'s `var-naming` enforces this.
- **File names match content**: singular for one type (`config.go`), plural for
  several related things (`enums.go`). Split by job, not by size — loading,
  validating and the enums are three files.
- **A type, its constants and its methods stay together** in one file.
- **Interfaces only where a second implementation exists.** Methods on structs
  are the right amount of OOP here; an interface with one implementation just
  costs a hop when reading. Accept interfaces, return structs.
- **Do not create a package until it has an occupant.** No `util` package —
  name packages after what they do (`redact`, not `util`). A little copying
  beats a little dependency.
- **Tests mirror source files**: `validate.go` has `validate_test.go`.
- **Tests inject configuration, never mutate process env.** `load(map[...])`
  keeps tests isolated and `t.Parallel()`-safe; `t.Setenv` does not.
- **`os.Exit` appears only in `main`, and `main` holds no defers.** `os.Exit`
  skips deferred functions entirely, so a `defer` beside it is dead code. That
  is the entire reason `run(ctx, out)` exists — everything that needs cleanup
  lives there and returns an error instead. `gocritic`'s `exitAfterDefer`
  catches regressions.
- **Take the context and the writer as parameters.** Anything that builds its
  own context cannot be cancelled by a test, and anything that writes to
  `os.Stdout` directly cannot be observed by one. Both turn a fast test into a
  ten-minute timeout.
- **Tests reserve a real ephemeral port; they never hardcode one and never use
  port 0.** Bind `127.0.0.1:0`, read `l.Addr()`, close it, use the address.
  A hardcoded port collides with a running brain, and `checkHostPort` rejects
  port 0 outright.
- **`noctx` bans the context-free constructors.** Use
  `httptest.NewRequestWithContext`, `(*net.ListenConfig).Listen` and
  `exec.CommandContext` — never `httptest.NewRequest`, `net.Listen`,
  `http.Get` or `exec.Command`. In tests the context is `t.Context()`.
- **Lifecycle tests poll, they do not sleep.** Wait until `/healthz` answers
  200 before asserting anything, with a deadline that fails the test. A
  `time.Sleep` long enough to be reliable is long enough to be slow, and short
  enough to be fast is flaky in CI.
- **Use the stdlib type when it already encodes the constraint.** `LogLevel` was
  a hand-written enum until `slog.Level` turned out to be a `TextUnmarshaler`
  that parses case-insensitively and rejects unknown names with a better
  message. Write your own type when the constraint is yours — `Environment` has
  no stdlib equivalent and stays. Probe the stdlib before writing the enum.
- **The logger is a parameter, never a global.** `slog.Default()` is a hidden
  dependency and cannot be swapped safely under `t.Parallel()`. It reaches
  `internal/server` through `New`, and a nil one is left to panic rather than
  defaulted — see the trust rule below.
- **Log fields, not sentences.** `logger.Info("request", "status", 202)`, never
  `logger.Info(fmt.Sprintf("request returned %d", 202))`. The point is querying
  by field. Never log a whole struct: `"config", cfg` with `%+v` bypasses the
  `String()` that was redacting the password.
- **Log `r.URL.Path`, never `r.URL.String()`.** Credentials travel in query
  strings, and the log shipper takes them off the box.
- **Middleware returns a `Middleware`, it does not take `next` directly.**
  `func LogRequests(logger) Middleware` — dependencies in the outer call, the
  handler in the inner one. The two-argument form nests inside-out and becomes
  unreadable at three middlewares.
- **An exported identifier that nothing calls is invisible.** `unused` assumes
  an exported function has a caller in another package, so a middleware that is
  built and never applied passes every linter. Only a test that drives the wired
  object catches it.
- **Validate what comes from outside; trust what comes from the composition
  root.** `config.Load` validates because the environment is outside the
  program. `server.New` does not check its logger for nil because `run` is
  inside it — a nil there is a programming error, and a panic with a stack
  trace beats a silent fallback.
- **Cheapest failure first.** `run` parses arguments before loading config, and
  loads config before dialling Postgres. A typo in a Kubernetes Job spec should
  fail instantly with `unknown command "migrat"`, not after a connect timeout
  with an error blaming the database.
- **Parsing is separate from execution.** `parse` turns `[]string` into a
  `command` and is pure — no config, no database, no environment. `execute`
  validates nothing because `parse` already did. That is what lets the argument
  tests run with nothing set up.
- **A fixed set of strings is a defined type, not a `string`.** `commandName`
  and `direction` exist so `exhaustive` fails the build when a new role is
  added and a switch forgets it. That guarantee depends on
  `default-signifies-exhaustive: false` — it was `true` here for three steps,
  and adding a `commandName` reported 0 issues. Keep the `default:` arm anyway:
  it is unreachable, it returns a clear error rather than silence, and it is
  tested.
- **Never put a DSN in an error message.** pgx already redacts the password in
  its own errors — `postgres://user:xxxxx@…` — and wrapping with the raw string
  undoes that. Errors reach stderr and the log shipper.
- **Never delete error handling to raise a coverage number.** When a branch is
  untestable, say so in a comment and pin the assumption with a test that fails
  if it stops holding — see `TestTheSessionLockerWeBuildCannotFail`.
