---
title: MCP servers
---

An MCP server gives agents tools. It is a URL spoken to over streamable HTTP,
or a command started and spoken to over stdio, and the team registers it once
and grants it, whole or tool by tool, to the agents that need it.

## Registering one

```sh
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
`teams/<team_id>/mcp/<name>#<key>`. The team writes the secret there and the
brain reads it when it connects, so a credential is never in a request, a row
or a log.

**Discovery connects from the brain**, and only to a public address or a range
its operator allowed with `MCP_PRIVATE_NETWORKS`. A command server is not
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
