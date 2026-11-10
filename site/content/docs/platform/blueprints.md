---
title: Blueprints
---

A blueprint is a team's MCP servers, skills and agents in one YAML or JSON
file. `oa plan` says what applying it would change; `oa apply` makes the team
what it says. The file can live in a repository, be reviewed in a pull request,
and be applied to staging and then to production.

## A blueprint

```yaml
# team.yaml
mcp_servers:
  - name: helpdesk
    url: https://helpdesk.example/mcp
    auth_secret: helpdesk#token        # a secret's name#key in the team, never the secret
skills:
  - path: ./skills/support-faq         # its folder, beside this file; SKILL.md names it
agents:
  - name: triage
    description: Sorts incoming support email
    model: {name: anthropic/claude-sonnet-5-5, max_tokens: 4096}
    pattern: react
    max_steps: 20
    instructions: Read the email, decide the queue, answer from the FAQ.
    skills: [support-faq]              # by name: in this file, or already in the team
    mcp_servers: [{name: helpdesk, allow: [create_ticket]}]
    delegation: listed
    delegates: [writer]
  - name: writer
    model: {name: anthropic/claude-sonnet-5-5, max_tokens: 4096}
    pattern: react
    max_steps: 10
    instructions: Write the reply.
```

An agent takes the fields `oa agents create` takes, except that its MCP
servers, skills and delegates are named rather than given by id. An MCP server
takes what `oa mcp-servers create` takes, with each secret reference written
relative to the team (`helpdesk#token` rather than
`teams/<team_id>/mcp/helpdesk#token`), so one file works for every team. A key
the format does not have is refused before anything is sent, so a misspelt
field is never quietly left out.

## Plan, then apply

```text
$ oa plan support -f team.yaml
+  mcp server  helpdesk     create
+  skill       support-faq  create
+  agent       triage       create
+  agent       writer       create
4 to create, 0 to change, 0 unchanged.

$ oa apply support -f team.yaml
(the same plan)
Apply these changes to team support? [y/N] y
applied 4 created, 0 changed.

$ oa apply support -f team.yaml
   mcp server  helpdesk     unchanged
   ...
Nothing to change.
```

An update names the fields it changes (`update: instructions, skills`), never
their values. Anything that would stop the plan being applied is a refusal,
shown with a `!`, and one refusal means nothing is applied:

```text
!  mcp server  helpdesk     auth_secret is a secret's name and key, name#key, among the team's MCP secrets
!  agent       triage       skills: "faq" is not a skill in this team or this blueprint
oa: fix the 2 refusals first; nothing was applied
```

`apply` applies exactly the plan it showed. If the team changed between the
plan and the answer, nothing is written and the new plan is shown. Under
`-o json` or `-o yaml`, and from a script, pass `--yes`: there is no table to
show before the question, and the one document printed is what was applied,
or the plan that stopped it.

## The rules

- **Names are the keys.** An item the file names is the team's item of that
  name, and an item that has not changed is not written, so applying a file
  twice changes nothing the second time.
- **An item the file names is described whole**, as a replace is. An agent
  written with no `skills:` loses its skills, and the plan says so.
- **An item the file does not name is left alone.** Nothing is ever removed;
  `oa agents delete` and the others still do that.
- **Custom agents only.** Naming an agent the brain provides is a refusal.
- **All or nothing.** Every row is written in one transaction. A changed
  skill's files are stored first, so a failure leaves at most files the reaper
  removes.
- **No discovery, no secrets.** A new MCP server has no tools until
  `oa mcp-servers discover`; put its secret in before applying.

## Who may apply one

Planning only reads, so anyone in the team may plan. Applying needs the
permission each write already needs: `tool:write` for an MCP server,
`skill:write` for a skill, `agent:write` for an agent and its grants. A plan
names a permission you lack as a refusal, so you find out before the question,
not after it.

## Limits

At most 50 items of each kind. A skill has the limits an uploaded one has, and
its files may be anything a skill may hold.
