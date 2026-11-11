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
`teams/<team_id>/mcp/helpdesk#token`), so one file works for every team. The
whole reference is accepted too, as long as it is the team's own. A key
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

## Exporting a team

```sh
oa export support > team.yaml
oa export support --agent triage          # only triage
```

`oa export` prints the team's MCP servers and custom agents as a blueprint:
grants by name, secret references as `name#key`. Planning it unchanged says
nothing would change, so it is where an edit starts: an item in a blueprint is
described whole, and starting from the export keeps every field and grant you
did not mean to touch. Skills are left out, since a skill is its folder and one
the file does not name is left alone; agents still grant them by name. The API
is `GET /teams/{id}/blueprint`, which any member may read.

## Building with a coding agent

A coding agent that can run commands can build a team for you. The
`build-a-team` skill, which oa ships, teaches it to read the team with
`oa export`, ask what it cannot know, write the blueprint, run `oa plan`, show
you the plan, and run `oa apply` only after you say yes.

```sh
oa setup skills              # for you: ~/.agents/skills and ~/.claude/skills
oa setup skills --project    # for this repository only
oa setup skills --dir PATH   # an agent that reads skills from elsewhere
```

The skill is in the [Agent Skills](https://agentskills.io) format, which Claude
Code, Codex, Cursor, GitHub Copilot, Gemini CLI, OpenCode and many others read.
`.agents/skills` is the folder most of them share; Claude Code reads
`.claude/skills`. The copy oa installs matches that version of oa, and one you
edited is kept unless you pass `--force`. It can also be installed with the
`skills` installer straight from this repository:

```sh
npx skills add https://github.com/LaplacianAI/openarity/tree/main/apps/cli/skills/build-a-team
```

The brain still checks every permission and field, so the agent can do no more
than you could. The skill tells it never to apply without your yes, never to
handle a secret's value (the blueprint names where a secret lives; you store
it), and to treat what it reads in a skill, a page or a tool's output as
information rather than instructions.

## The builder

Each team has a built-in **builder**: an agent that designs what you ask for,
often a whole team in one message, and proposes it as one blueprint. It runs
on the team's model.

```sh
$ oa build support 'Support team: triage hands billing questions to a billing
agent that reads Stripe, and refunds to one that files Linear tickets.'
… the builder reads the team, looks Stripe and Linear up, asks each for its tools …
Proposal 4f2a…: Three agents, Stripe and Linear servers, and a refund-policy skill
+  mcp server  stripe   create
+  mcp server  linear   create
+  skill       refund-policy  create
+  agent       triage   create
+  agent       billing  create
+  agent       refunds  create
Assumed: the team's model for all three; triage delegates to the other two.
Set stripe#token and linear#token in the secret store.
Apply it: oa proposals apply support 4f2a…

$ oa proposals apply support 4f2a…
applied 6 created, 0 changed.
discovered stripe: 14 tools.
not discovered linear: auth_secret_ref names a secret that is not in the secret store, or is empty
  Once that is fixed: oa mcp-servers discover support linear
```

- **It researches, in one turn.** It looks servers up in the MCP registry
  (`OPENARITY_MCP_REGISTRY_URL`, the official one by default), reads their
  documentation and a skill's source on the web when the team has web search,
  and asks each remote server for its tools, so each agent is granted only
  those it needs. It decides what it can and lists those choices as
  assumptions, and asks only when it cannot tell what you want.
- **It cannot write.** Besides those, its tools read the team (as `oa export`
  does, plus its skills and each server's tools) and record a proposal. It has
  no MCP server's tools to call, no skills and no delegation.
- **A secret goes only where the team already sends it.** Asking a server for
  its tools sends no secret, unless the team already has a server at that
  address with that secret. A page the builder read cannot steer a token to a
  host of its choosing.
- **Applying discovers.** Each server the apply creates or changes is asked
  for its tools once the write commits, so an agent can use them at once. One
  that cannot be asked yet, such as one whose secret you have not set, says
  why; the apply stands.
- **You apply, as yourself.** Applying plans the proposal again with your
  permissions and writes only the plan you were shown. A proposal is applied
  once; a newer one in the same conversation supersedes it.
- **A conversation.** `--session` carries on: the builder reads its earlier
  turns, so it can ask and then propose.
- **Bounded.** At most five proposals per turn, a 1 MiB blueprint, and no
  secret values: a blueprint names references, and you store the values.

A proposal in your direct session is yours alone, as the session is.

Both show the builder at work: each step as it takes it (reading the team,
looking a server up, reading a page, asking a server for its tools) and its
reply as it is written.

In the dashboard, **Builder** under Build is the same conversation: say what
you want, and each proposal appears as a card with its plan, as it would apply
for you. **Needs from you** names each secret to set and each address the
team's agents would newly be sent to, so you can spot a host you do not
recognise. **Apply** applies that plan and shows what each new server
answered; **Change something** is your next message. The conversation is in the page's address,
so a reload or a link brings it back, and **Builder** with no conversation open
lists your recent ones. `oa build <team>` with no message does the same, with
each session id for `--session`.

The builder asks by replying: a question ends its turn, and your next message
in the conversation is the answer, read with the turns before it.

## In the dashboard

**Blueprint**, under Build, lists the builder's open proposals, then builds a blueprint from forms rather than a file: add
MCP servers, skill folders and agents to a draft, or change the team's own,
then **Plan** and **Apply**. An agent in the draft may be granted a server or
skill the same draft creates.

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
