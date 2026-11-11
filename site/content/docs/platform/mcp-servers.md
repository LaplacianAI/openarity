---
title: MCP servers
---

An MCP server gives agents tools. It is a URL spoken to over streamable HTTP,
or a command started and spoken to over stdio, and the team registers it once
and grants it, whole or tool by tool, to the agents that need it.

## Registering one

```sh
printf %s "$GITHUB_TOKEN" | oa mcp-servers secret platform github token
oa mcp-servers create platform github --url https://api.githubcopilot.com/mcp/ \
    --auth-secret-ref teams/<team_id>/mcp/github#token
oa mcp-servers discover platform github     # ask it for its tools now
oa mcp-servers tools platform github
```

A command server takes its command after `--`, and its environment as
references:

```sh
oa mcp-servers create platform files -- npx -y @modelcontextprotocol/server-filesystem /data
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
its operator allowed with `OPENARITY_MCP_PRIVATE_NETWORKS`. A command server is not
started by the brain to discover it.

**Tool names.** A server's tools reach the model as `<server>__<tool>`, unless
it is registered `--bare`. A bare tool named like one of the brain's own on
that run — `graph`, `delegate`, `plan`, `web_search`, `web_fetch` — is left out
of the run with a `tool_unavailable` event rather than shadowing the brain's.

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
