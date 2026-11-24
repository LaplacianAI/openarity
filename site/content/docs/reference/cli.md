---
title: oa, the CLI
---

`oa` talks to a brain over its HTTP API and holds no state of its own beyond a
config file. Against a development brain it needs no setup — run `oa whoami` and
it finds the shared token already in your shell. Anywhere else, `oa login`.

## Commands

| Command | What it does |
| ------------------------------ | ------------------------------------------------------- |
| `oa login`                     | Log in to the current context's brain                    |
| `oa logout`                    | Discard the current context's credential                 |
| `oa whoami`                    | Show who the current credential authenticates as         |
| `oa context list`              | List the saved contexts                                  |
| `oa context create <name>`     | Add a context and switch to it                           |
| `oa context use <name>`        | Make a context the one every command talks to            |
| `oa context rename <old> <new>`| Rename a context, keeping its address and credential     |
| `oa context delete <name>`     | Forget a context and its credential                      |
| `oa config show`               | Show the effective settings and where each came from     |
| `oa config set <key> <value>`  | Write a setting to the config file                       |
| `oa config unset <key>`        | Remove a setting from the config file                    |
| `oa config path`               | Print the config file location                           |
| `oa teams list`                | List the teams you can see                               |
| `oa teams create <name>`       | Create a team                                            |
| `oa teams delete <team>`       | Delete a team; it can be restored until it is erased     |
| `oa teams restore <team>`      | Bring back a deleted team                                |
| `oa teams list --deleted`      | List deleted teams, and when each is erased              |
| `oa teams members list <team>` | List a team's members                                    |
| `oa teams members add <team> <user>` | Add someone to a team, with `--role` (`member`, `approver` or `admin`) |
| `oa teams members remove <team> <user>` | Take someone out of a team                      |
| `oa teams settings show <team>` | Show a team's model, web search, self-improvement (with trying changes on past runs), compaction and hub |
| `oa teams settings set <team>` | Change only the settings named by flags, such as `--compaction-messages`, `--compaction-tokens-min` and `--compaction-tokens-max` (30,000 to 1,000,000, the least never above the most), `--judge-model` (with `--judge-max-tokens`, `--judge-temperature`) or `--no-judge-model`, `--sample-share` (0 to 100) `--daily-passes` (1 to 200, the most improver passes a day; 20 until set), `--pass-tokens` (100,000 to 2,000,000, the tokens a pass may spend across its tries) and `--pass-minutes` (1 to 25, the minutes for each try); 400,000 and 20 until set |
| `oa teams settings set <team> --daily-runs <n>` | The judged runs an agent needs since its last pass before the daily improver pass looks at it, 1 to 100; 5 until set |
| `oa teams settings set <team> --efficiency on\|off` | The weekly efficiency pass, which proposes making an agent's good runs cheaper and faster; off until set, and it runs only while improvement is on. Its numbers: `--efficiency-runs` (10 to 500 good runs needed; 20), `--efficiency-days` (7 to 60; 14), `--efficiency-spread` (125 to 1000, in percent of the usual run, 200 being twice; 200), `--efficiency-mark-runs` (2 to 50 runs a sign of waste must show in, at most `--efficiency-runs`; 3) and `--efficiency-margin` (0 to 3 bad runs sooner an applied efficiency change is flagged; 1) |
| `oa teams settings set <team> --replay on\|off` | Try each proposed change on a few of its agent's past runs before it is decided, sending and changing nothing; off until set, and it runs only while improvement is on. Its numbers: `--replay-runs` (1 to 10 past runs a change; 4), `--replay-tokens` (20000 to 1000000 a try; 200000), `--replay-minutes` (1 to 30 a try; 10) and `--replay-daily` (1 to 500 tries the team runs a day; 40) |
| `oa improvements list <team>` | List the changes agents propose to themselves: waiting for an approver, applied for everyone else; `--status`, `--agent` and `--query` narrow it, and `--status flagged` lists the applied changes whose watch flagged them |
| `oa improvements show <team> <proposal>` | Show one proposal: why, what it expects, what it reaches, the improver pass that wrote it and the change, and for an applied one how it went: its runs after against the ones before, the agent's usual rate and the bar it sets, cost, and the pattern it targeted; and, once tried on past runs, how each went before and after |
| `oa improvements replay <team> <proposal> <run>` | One try of a proposal on a past run: each call with how it was served (recorded, live, or not made) and the answer it came to; only for someone who may read the run's conversation |
| `oa improvements passes <team>` | List the improver's passes, newest first: the agent, why it ran (a pattern, the daily pass, or the weekly efficiency pass with the good runs it drew from), status, proposals made and screened out (or why it failed), tokens and start; `--agent` narrows it, `-o json` carries each pass's report, withheld (`report_withheld`) when it draws on a conversation you cannot read (needs `improvement:approve`) |
| `oa improvements approve <team> <proposal>` | Apply a waiting change (needs `improvement:approve` and the write it makes) |
| `oa improvements reject <team> <proposal>` | Refuse a waiting change |
| `oa improvements revert <team> <proposal>` | Put back what an applied change replaced, if nothing has changed since |
| `oa improvements clear <team> <proposal>` | Keep a change its watch flagged: a person looked (needs `improvement:approve` only) |
| `oa channels list <team>`      | List a team's channels                                   |
| `oa channels create <team> <name>` | Connect a channel: `--provider`, and optionally `--reply-url`, `--secret-stdin`, or `--credential-stdin NAME` for a provider's credential such as Telegram's `bot_token` |
| `oa channels update <team> <channel>` | Set where a channel's replies go (`--reply-url`), stop them (`--no-reply-url`), switch the notices to waiting senders (`--notify-strangers`, `--no-notify-strangers`), or send each step of a run before its answer (`--reply-mode events`, `--reply-mode final`) |
| `oa channels delete <team> <channel>` | Disconnect a channel                              |
| `oa channels senders pending <team> <channel>` | List senders waiting to be approved      |
| `oa channels senders list <team> <channel>` | List approved senders                       |
| `oa channels senders approve <team> <channel> <sender-ref> <user>` | Let a sender speak as a user |
| `oa channels senders remove <team> <channel> <sender-ref>` | Take a sender's access away, or dismiss a pending one |
| `oa sessions list <team>`      | List a team's conversations, or one channel's with `--channel` |
| `oa sessions get <team> <session>` | Show a conversation: its kind, where it came from and when |
| `oa sessions read <team> <session>` | Read a conversation                                 |
| `oa sessions feedback <team> <run> up\|down [--note TEXT]` | Give or change your 👍/👎 on a run's answer, which outranks the judge's verdict; `--clear` removes it |
| `oa runs start <team> [agent] <input>` | Ask the team's hub, or start an agent on a task; `--session` continues a conversation (a run already working there takes the input as a message, and the row says `steered`), `--follow` prints its events |
| `oa runs events <team> <run>`  | Follow what a run does, as it does it: each call and result, `↧ compacted` when it set part of its context aside (`--verbose` adds the summary), and `⚠ wrapping up` when it was asked to finish before a limit |
| `oa runs get <team> <run>`     | Show a run: its status, input, answer and what it spent  |
| `oa runs list <team> <session>`| List the runs started in a conversation                  |
| `oa runs cancel <team> <run>`  | Stop a run                                               |
| `oa agents list <team>`        | List a team's agents                                     |
| `oa agents get <team> <agent>` | Show an agent, with its instructions and its grants      |
| `oa agents create <team> -f <file>` | Define an agent                                     |
| `oa agents replace <team> <agent> -f <file>` | Replace an agent's definition and grants   |
| `oa agents delete <team> <agent>` | Delete an agent                                       |
| `oa skills list <team>`        | List a team's skills                                     |
| `oa skills get <team> <skill>` | Show a skill: its frontmatter, where it came from, and its files |
| `oa skills cat <team> <skill> <path>` | Print one of a skill's files                      |
| `oa skills upload <team> <dir\|zip>` | Upload a skill from a directory or a zip          |
| `oa skills import <team> <source>` | Import a skill from a GitHub folder or a zip at an allowed address |
| `oa skills sync <team> <skill>` | Fetch an imported skill again from where it came from   |
| `oa skills replace <team> <skill> <dir\|zip>` | Replace a skill's files with a new directory or zip |
| `oa skills delete <team> <skill>` | Delete a skill                                        |
| `oa mcp-servers list <team>`   | List a team's MCP servers                                |
| `oa mcp-servers get <team> <server>` | Show an MCP server                                 |
| `oa mcp-servers create <team> <name>` | Register an MCP server, by `--url` or a command after `--` |
| `oa mcp-servers discover <team> <server>` | Ask an MCP server for its tools now, and record them |
| `oa mcp-servers tools <team> <server>` | List the tools it offered when it was last discovered |
| `oa mcp-servers replace <team> <server> <name>` | Replace an MCP server's definition       |
| `oa mcp-servers delete <team> <server>` | Delete an MCP server and its tools              |
| `oa export <team>`            | Print a team as a blueprint: grants by name, ready to edit and plan; `--agent`, `--mcp-server` for only some |
| `oa plan <team> -f <file>`    | Show what applying a [blueprint](../platform/blueprints.md) would change |
| `oa apply <team> -f <file>`   | Make a team what a blueprint says, after showing the plan and asking; `--yes` for a script |
| `oa build <team> <ask>`       | Ask the team's [builder](../platform/blueprints.md#the-builder) for agents, servers or skills; it proposes and writes nothing. `--session` answers it |
| `oa proposals list\|get\|apply <team>` | The builder's proposals; `apply` plans one again as you, shows it and asks |
| `oa setup skills`             | Install oa's [agent skills](../platform/blueprints.md#building-with-a-coding-agent) for Claude Code, Codex, Cursor, Copilot, Gemini CLI and others; `--project`, `--dir` |
| `oa users list [subject]`      | List users, or find one by subject                       |

`list` accepts `ls`, `delete` and `remove` accept `rm`, and `mcp-servers` accepts `mcp`.

Anywhere a command takes `<team>`, `<channel>`, `<user>`, `<agent>`, `<skill>`
or `<server>`, a name works as well as an id. A uuid is used as given and never
looked up, so a script that passes ids pays nothing for the convenience.

## Flags on every command

| Flag | What it does |
| ------------------- | ------------------------------------------------------------------ |
| `-o, --output`      | `table`, `json` or `yaml`. Default: `$OPENARITY_OUTPUT`, then the saved config, then `table` |
| `--server`          | The brain's API address. Default: `$OPENARITY_SERVER`, then the saved config, then `http://127.0.0.1:21120` |
| `--token`           | The credential to send, instead of the saved one                    |
| `--non-interactive` | Never prompt; fail instead of asking                                |

Lists page. A paged command prints `{items, next_cursor}` under `-o json` and
tells you the command to run for the next page under `-o table`; `--limit` and
`--cursor` control it.

!!! info

    This table is the command tree as `oa --help` prints it. For the flags and the
    prose belonging to one command, ask it: `oa channels senders approve --help`.
