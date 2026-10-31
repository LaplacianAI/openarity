---
title: The dashboard
---

The brain serves a web dashboard at `/ui` — on a local stack,
`http://127.0.0.1:21120/ui`. It signs in through the same identity provider as
`oa`, and everything in it is scoped to the team chosen at the top.

| Page | What it is for |
| --------------- | -------------------------------------------------------------- |
| Overview        | The team at a glance                                            |
| Teams           | The teams you can see, and their members                       |
| Agents          | Defining and editing agents, their grants and the **Web** switch |
| Skills          | Uploading, importing and reading skills                         |
| MCP servers     | Registering servers and seeing the tools they offer             |
| Channels        | Connecting a channel and the address its provider sends to      |
| Approval queue  | Senders nobody has vouched for yet: nothing they send reaches an agent until they are bound to a user |
| Sessions        | Every conversation, its messages, and each run's trace — tool calls, delegations and fetched pages |
| Graph           | The team's agents, servers, tools and skills as the graph holds them |
| Settings        | The team's model, web search, learning, compaction and hub      |

Sign-in needs a secure context, so a dashboard reached at a LAN or tailnet
address must be served over HTTPS; on the machine itself, `127.0.0.1` works
over plain HTTP. The
[staging guide](https://github.com/LaplacianAI/openarity/blob/main/deployment/staging/README.md)
puts it on HTTPS at names you own, with a certificate proven through
Cloudflare DNS.

## Not built

- **Starting a run.** The dashboard shows runs and their traces; a run starts
  from `oa runs start` or the HTTP API.
