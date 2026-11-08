# Openarity

Openarity is an open-source agent platform in which agents, tools, skills and
learnings form a knowledge graph, and a run selects tools by walking that graph
rather than ranking a flat list. The repository holds the brain (the Go server
that runs agents), the `oa` CLI, the dashboard, and the Go agent SDK.

| Where | Instructions | Skills |
| --- | --- | --- |
| `apps/brain` | `apps/brain/AGENTS.md` | `apps/brain/.agents/skills` |
| `apps/brain/internal/gateway` | `apps/brain/internal/gateway/AGENTS.md` | (brain's) |
| `apps/cli` | `apps/cli/AGENTS.md` | `apps/cli/.agents/skills` |
| `apps/dashboard` | `apps/dashboard/AGENTS.md` | `apps/dashboard/.agents/skills` |
| `sdk/agent` | none | `sdk/agent/.agents/skills` |
| repo root | this file | `.agents/skills`: file-an-issue, open-a-pull-request, running-an-agent-team, update-project-docs, upgrade-the-go-toolchain |

Start your agent in the app's directory to get that app's instructions and
skills.

Every app is a separate Go or TypeScript module. The only thing that crosses an
app boundary is `apps/brain/api/openapi.yaml`.

Commits follow Conventional Commits, scoped by app: `feat(brain): ...`,
`fix(cli): ...`, `docs: ...`. Pull requests are squash-merged, so the pull
request title is the commit subject.

Add a skill under the level's `.agents/skills/<name>/SKILL.md` and link it with `ln -s ../../.agents/skills/<name> .claude/skills/<name>`.
