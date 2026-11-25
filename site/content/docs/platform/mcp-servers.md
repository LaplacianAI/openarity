---
title: MCP servers
---

An MCP server gives agents tools. The brain calls one at a URL, over
streamable HTTP; the team registers it once and grants it, whole or tool by
tool, to the agents that need it. A server that is a command, spoken to over
stdio, is put behind a bridge first: see [A command server](#a-command-server).

## Registering one

```sh
printf %s "$GITHUB_TOKEN" | oa mcp-servers secret platform github token
oa mcp-servers create platform github --url https://api.githubcopilot.com/mcp/ \
    --auth-secret-ref teams/<team_id>/mcp/github#token
oa mcp-servers discover platform github     # ask it for its tools now
oa mcp-servers tools platform github
```

**Credentials are references, never the secret.** `--auth-secret-ref` and
`--env NAME=…` name a path in the secret store,
`teams/<team_id>/mcp/<name>#<key>`. The brain reads it when it connects, so a
credential is never in a row or a log, and never comes back out of the API.

**Writing one.** In the dashboard, a server's form has a **Token** field: what
you type is written to the secret store as `<name>#token`, the server names it,
and it is asked for its tools at once. `oa mcp-servers secret <team> <name>
<key>` writes one from stdin, and a builder proposal's card offers **Set** for
each secret it needs. All three use `PUT /teams/{id}/mcp-secrets/{name}`, which
needs `tool:write` and writes every key of that secret together, because a
write replaces the keys it held. A secret you keep in OpenBao yourself still
works: name it in the reference.

**Discovery connects from the brain**, and only to a public address or a range
its operator allowed with `OPENARITY_MCP_PRIVATE_NETWORKS`. A command server is
never asked: see [A command server](#a-command-server).

**Tool names.** A server's tools reach the model as `<server>__<tool>`, unless
it is registered `--bare`. A bare tool named like one of the brain's own on
that run — `graph`, `delegate`, `plan`, `web_search`, `web_fetch`,
`send_file` — is left out of the run with a `tool_unavailable` event rather
than shadowing the brain's.

## A command server

Many MCP servers are a command, such as `npx mcp-mail-server`, spoken to over
stdio. **The brain never runs one itself**: a command a team chose would run
beside the database's credentials and every other team's data. A team that has
chosen a sandbox in Settings (E2B, Daytona or sandboxd) has its command servers
run there instead: the brain makes a room on the first call to the server,
keeps it while it is used, and deletes it after `OPENARITY_SANDBOX_IDLE`
(thirty minutes by default) without a call. A team with no sandbox is told to
choose one, and its command servers are not run.

Without a sandbox, run the command behind a stdio-to-HTTP bridge, where you
choose and control the machine, and register the bridge's URL.
[Supergateway](https://github.com/supercorp-ai/supergateway) is one:

```sh
docker run -d --name mail-mcp --restart unless-stopped \
  -e EMAIL_USER -e EMAIL_PASS \
  -e IMAP_HOST=imap.gmail.com -e IMAP_PORT=993 -e IMAP_SECURE=true \
  -e SMTP_HOST=smtp.gmail.com -e SMTP_PORT=465 -e SMTP_SECURE=true \
  node:22-alpine npx -y supergateway \
    --stdio "npx -y mcp-mail-server@2.1.3" \
    --outputTransport streamableHttp --port 8000

oa mcp-servers create platform mail --url http://mail-mcp:8000/mcp
oa mcp-servers discover platform mail
```

- **Its secrets are yours to keep.** The bridge's environment holds them, so
  keep it on a network only the brain reaches: never publish its port to the
  internet, since anyone who reaches it can use the mailbox.
- **A private address must be allowed.** A bridge beside the brain is at a
  private address, which the brain refuses unless
  `OPENARITY_MCP_PRIVATE_NETWORKS` names it. Name that one address as a `/32`,
  as `deployment/docker-compose.searxng.yml` does for SearXNG, rather than the
  whole network, which would open Postgres and OpenBao to any URL a team saves.
- **Pin the package.** `mcp-mail-server@2.1.3`, not `mcp-mail-server`, so an
  update cannot change what runs.

A command server can still be registered, with its command after `--` and its
environment as references, so a team can record what it uses:

```sh
oa mcp-servers create platform files -- npx -y @modelcontextprotocol/server-filesystem /data
```

It is never discovered and never reaches a run: the dashboard says so where
**Discover** would be, and a run of an agent granted it starts without it.

## Granting

```sh
oa agents replace platform triage -f triage.yaml --mcp-server github=search_code
```

Seeing a team's servers and their tools needs only to be in the team;
registering, replacing, discovering or deleting one needs `tool:write`. A
server an agent is granted is not deleted: revoke the grant first. The secrets
it referred to stay in the secret store; they are the team's to remove.

## Not built

- **A deleted team's MCP secrets are left in the secret store.**
