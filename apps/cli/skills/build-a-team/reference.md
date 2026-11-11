# The blueprint format

One YAML (or JSON) file. Three lists, each optional. Items refer to each
other, and to the team's own items, **by name**.

```yaml
mcp_servers:
  - name: helpdesk                       # letters, digits, - and _; the key
    url: https://helpdesk.example/mcp    # a remote server, or…
    auth_secret: helpdesk#token          # its bearer token: a secret's name#key, never the value
  - name: files
    command: [files-mcp, --stdio]        # …a local one, run by the brain
    env: {FILES_KEY: files#key}          # each value a secret reference too
    bare: false                          # true: no credential at all

skills:
  - path: ./skills/support-faq           # a folder beside this file; its SKILL.md names it

agents:
  - name: triage
    description: Sorts incoming support email
    instructions: |
      Read the email, decide the queue, answer from the FAQ.
    model:
      name: anthropic/claude-sonnet-5-5   # a model the team's gateway serves
      max_tokens: 4096
      temperature: 0.2                    # optional
    pattern: react                        # the loop: oa agents create --help lists them
    max_steps: 20
    steer_continuations: 0
    token_budget: 200000                  # optional: stop the run past this many tokens
    web: false                            # web_search and web_fetch
    mcp_servers:
      - name: helpdesk
        allow: [create_ticket]            # only these tools; leave allow out for all
    skills: [support-faq]
    delegation: listed                    # none (default), team, or listed
    delegates: [writer]                   # only with listed
    output_schema:                        # optional: the answer as JSON
      name: verdict
      json: {type: object, properties: {queue: {type: string}}, required: [queue]}
      strict: true
    parser: false                         # true: a second model turns the answer into output_schema
    # parser_model: {name: ..., max_tokens: 512}

  - name: writer
    model: {name: anthropic/claude-sonnet-5-5, max_tokens: 4096}
    pattern: react
    max_steps: 10
    instructions: Write the reply.
    mcp_servers: []
    skills: []
```

## What the brain does with it

- **Names are the keys.** An item named is the team's item of that name; one
  not in the team is created. An item that has not changed is not written,
  so applying a file twice changes nothing.
- **An item named is described whole.** Every field and grant it leaves out
  is reset: no `skills:` means no skills.
- **An item not named is left alone.** Nothing is ever removed; deleting is
  `oa agents delete`, `oa mcp-servers delete`, `oa skills delete`.
- **Plan, then apply.** `oa apply` plans again and applies only if the team is
  as the plan said.
- **All or nothing.** One refusal, and nothing is written.
- **Permissions:** `tool:write` for a server, `skill:write` for a skill,
  `agent:write` for an agent and its grants. Planning needs none.
- **Limits:** 50 of each kind in one file.
- **A key the format does not have is refused** before anything is sent.
