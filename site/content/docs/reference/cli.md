---
title: oa, the CLI
weight: 1
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
| `oa teams members list <team>` | List a team's members                                    |
| `oa teams members add <team> <user>` | Add someone to a team                              |
| `oa teams members remove <team> <user>` | Take someone out of a team                      |
| `oa channels list <team>`      | List a team's channels                                   |
| `oa channels create <team> <name>` | Connect a channel                                    |
| `oa channels delete <team> <channel>` | Disconnect a channel                              |
| `oa channels senders pending <team> <channel>` | List senders waiting to be approved      |
| `oa channels senders list <team> <channel>` | List approved senders                       |
| `oa channels senders approve <team> <channel> <sender-ref> <user>` | Let a sender speak as a user |
| `oa channels senders remove <team> <channel> <sender-ref>` | Take a sender's access away, or dismiss a pending one |
| `oa sessions list <team>`      | List a team's conversations                              |
| `oa sessions read <team> <session>` | Read a conversation                                 |
| `oa users list [subject]`      | List users, or find one by subject                       |

`list` accepts `ls`, and `delete` and `remove` accept `rm`.

Anywhere a command takes `<team>`, `<channel>` or `<user>`, a name works as well
as an id. A uuid is used as given and never looked up, so a script that passes
ids pays nothing for the convenience.

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

{{< callout type="info" >}}
This table is the command tree as `oa --help` prints it. For the flags and the
prose belonging to one command, ask it: `oa channels senders approve --help`.
{{< /callout >}}
