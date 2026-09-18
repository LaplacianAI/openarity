---
title: HTTP API
weight: 2
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

| Method | Path | What it does |
| ------ | ------------------------------------------------------------ | ---------------------------------------- |
| GET    | `/healthz`                                                    | Liveness                                 |
| GET    | `/readyz`                                                     | Readiness — 503 when Postgres is unreachable |
| GET    | `/auth/config`                                                | How to obtain a token                    |
| GET    | `/whoami`                                                     | The authenticated caller                 |
| GET    | `/teams`                                                      | List teams                               |
| POST   | `/teams`                                                      | Create a team                            |
| GET    | `/teams/{id}`                                                 | Read one team                            |
| GET    | `/teams/{id}/members`                                         | List a team's members                    |
| POST   | `/teams/{id}/members`                                         | Add a member                             |
| DELETE | `/teams/{id}/members/{userID}`                                | Remove a member                          |
| GET    | `/teams/{id}/channels`                                        | List a team's channels                   |
| POST   | `/teams/{id}/channels`                                        | Connect a channel                        |
| DELETE | `/teams/{id}/channels/{channelID}`                            | Disconnect a channel                     |
| GET    | `/teams/{id}/channels/{channelID}/senders/pending`            | List senders waiting to be approved      |
| GET    | `/teams/{id}/channels/{channelID}/senders`                    | List approved senders                    |
| POST   | `/teams/{id}/channels/{channelID}/senders`                    | Approve a sender                         |
| DELETE | `/teams/{id}/channels/{channelID}/senders`                    | Remove a sender, or dismiss a pending one |
| GET    | `/teams/{id}/channels/{channelID}/sessions`                   | List a channel's conversations           |
| GET    | `/teams/{id}/sessions`                                        | List every conversation in a team        |
| GET    | `/teams/{id}/sessions/{sessionID}/messages`                   | Read a conversation                      |
| GET    | `/teams/{id}/sessions/{sessionID}/attachments`                | List the files in a conversation         |
| GET    | `/teams/{id}/sessions/{sessionID}/attachments/{attachmentID}` | Download a file                          |
| GET    | `/users`                                                      | Find a user                              |

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
