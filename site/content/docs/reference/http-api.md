---
title: HTTP API
---

The brain serves two listeners. This page describes the API one,
`OPENARITY_API_BIND`, which defaults to `127.0.0.1:21120`. The other is the
inbound gateway, `OPENARITY_WEBHOOK_BIND` — it authenticates a request by its
channel's signing secret rather than by a credential, so nothing here applies to
it.

The contract is
[`apps/brain/api/openapi.yaml`](https://github.com/LaplacianAI/openarity/blob/main/apps/brain/api/openapi.yaml),
hand-written and reviewed as a diff. A test compares the routes the service
registers against the paths in it, so adding an endpoint without describing it
fails the build. The CLI's client is generated from the same file.

## Authenticating

`Authorization: Bearer <token>` on everything except the probes and
`/auth/config`. The token is an OIDC access token, or the shared development
token when `OPENARITY_DEV_TOKEN` is set. The scheme is matched
case-insensitively.

`GET /auth/config` is readable before you can authenticate, and says how to.

## Endpoints

Every operation in [`openapi.yaml`](https://github.com/LaplacianAI/openarity/blo
b/main/apps/brain/api/openapi.yaml), in its order, with its summary. The spec is
the contract — the CLI's client is generated from it — so it holds every request
and response field this table leaves out.

| Method | Path | What it does |
| ------ | ------------------------------------------------------------- | -------------------------------------------------- |
| GET    | `/healthz`                                                    | Liveness                                           |
| GET    | `/readyz`                                                     | Readiness                                          |
| GET    | `/auth/config`                                                | How to obtain a token                              |
| GET    | `/whoami`                                                     | The authenticated caller                           |
| GET    | `/teams`                                                      | List teams                                         |
| POST   | `/teams`                                                      | Create a team                                      |
| GET    | `/teams/{id}`                                                 | Read one team                                      |
| GET    | `/teams/{id}/permissions`                                     | What the caller may do in a team                   |
| GET    | `/teams/{id}/members`                                         | List a team's members                              |
| POST   | `/teams/{id}/members`                                         | Add a member                                       |
| DELETE | `/teams/{id}/members/{userID}`                                | Remove a member                                    |
| GET    | `/teams/{id}/channels`                                        | List a team's channels                             |
| POST   | `/teams/{id}/channels`                                        | Connect a channel                                  |
| DELETE | `/teams/{id}/channels/{channelID}`                            | Disconnect a channel                               |
| GET    | `/teams/{id}/channels/{channelID}/senders/pending`            | List senders waiting to be approved                |
| GET    | `/teams/{id}/channels/{channelID}/senders`                    | List approved senders                              |
| POST   | `/teams/{id}/channels/{channelID}/senders`                    | Approve a sender                                   |
| DELETE | `/teams/{id}/channels/{channelID}/senders`                    | Remove a sender, or dismiss a pending one          |
| GET    | `/teams/{id}/channels/{channelID}/sessions`                   | List a channel's conversations                     |
| GET    | `/teams/{id}/sessions`                                        | List every conversation in a team                  |
| GET    | `/teams/{id}/sessions/{sessionID}`                            | Read a conversation's details                      |
| GET    | `/teams/{id}/sessions/{sessionID}/messages`                   | Read a conversation                                |
| GET    | `/teams/{id}/sessions/{sessionID}/runs`                       | List the runs in a conversation                    |
| GET    | `/teams/{id}/sessions/{sessionID}/attachments`                | List the files in a conversation                   |
| GET    | `/teams/{id}/sessions/{sessionID}/attachments/{attachmentID}` | Download a file                                    |
| GET    | `/teams/{id}/skills`                                          | List a team's skills                               |
| POST   | `/teams/{id}/skills`                                          | Upload a skill                                     |
| GET    | `/teams/{id}/skills/{skillID}`                                | Read a skill                                       |
| PUT    | `/teams/{id}/skills/{skillID}`                                | Replace a skill                                    |
| DELETE | `/teams/{id}/skills/{skillID}`                                | Delete a skill                                     |
| GET    | `/teams/{id}/skills/{skillID}/files/{path}`                   | Download one of a skill's files                    |
| POST   | `/teams/{id}/skills/import`                                   | Import a skill from GitHub or a zip on the web     |
| POST   | `/teams/{id}/skills/{skillID}/sync`                           | Fetch an imported skill's source again             |
| GET    | `/teams/{id}/agents`                                          | List a team's agents                               |
| POST   | `/teams/{id}/agents`                                          | Define an agent                                    |
| GET    | `/teams/{id}/agents/{agentID}`                                | Read an agent, with its grants                     |
| PUT    | `/teams/{id}/agents/{agentID}`                                | Replace an agent and its grants                    |
| DELETE | `/teams/{id}/agents/{agentID}`                                | Delete an agent                                    |
| GET    | `/teams/{id}/loops`                                           | List the loops an agent may use                    |
| POST   | `/teams/{id}/runs`                                            | Start a run of an agent, or ask the team's hub     |
| GET    | `/teams/{id}/runs/{runID}`                                    | Read a run                                         |
| GET    | `/teams/{id}/runs/{runID}/events`                             | Follow what a run does                             |
| POST   | `/teams/{id}/runs/{runID}/cancel`                             | Cancel a run                                       |
| GET    | `/teams/{id}/settings`                                        | Read a team's settings                             |
| PUT    | `/teams/{id}/settings`                                        | Change a team's settings                           |
| GET    | `/teams/{id}/mcp-servers`                                     | List a team's MCP servers                          |
| POST   | `/teams/{id}/mcp-servers`                                     | Register an MCP server                             |
| GET    | `/teams/{id}/mcp-servers/{serverID}`                          | Read an MCP server                                 |
| PUT    | `/teams/{id}/mcp-servers/{serverID}`                          | Replace an MCP server                              |
| DELETE | `/teams/{id}/mcp-servers/{serverID}`                          | Delete an MCP server                               |
| GET    | `/teams/{id}/mcp-servers/{serverID}/tools`                    | List the tools a server offers                     |
| POST   | `/teams/{id}/mcp-servers/{serverID}/discover`                 | Ask a server which tools it offers, and store them |
| GET    | `/teams/{id}/graph`                                           | The team's graph                                   |
| GET    | `/users`                                                      | Find a user                                        |

## Two things that surprise people

**Errors are `text/plain`, not JSON.** Successful responses are JSON; failures
carry a short sentence and nothing machine-readable. Branch on the status code,
never on the body.

**404 does not mean the thing does not exist.** A team you cannot see answers
404 rather than 403, so a caller cannot map what exists by probing.

## Paging

Every list takes `limit` and `cursor` as query parameters and answers with an
envelope, never a bare array:

```json
{ "items": [], "next_cursor": "…" }
```

`next_cursor` is absent on the last page, and its absence is the only
end-of-collection signal — there is no separate flag that could disagree with
it. It is opaque and not constructible by a client; an altered one is a 400
rather than a silent restart.

`limit` defaults to 50 and is clamped at 100, so asking for a thousand gets you
a hundred rather than an error.
