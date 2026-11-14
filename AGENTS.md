# Openarity

Openarity is an open-source agent platform in which agents, tools, skills and
learnings form a knowledge graph, and selection is a walk over that graph rather
than a ranking of a flat list: a run with more tools than it is worth sending
finds the ones its task needs by searching that graph. The repository holds the brain (the Go server
that runs agents), the `oa` CLI, the dashboard, the Go agent SDK, and the Go
stream SDK that serves a conversation as resumable events.

| Where | Instructions | Skills |
| --- | --- | --- |
| `apps/brain` | `apps/brain/AGENTS.md` | `apps/brain/.agents/skills` |
| `apps/brain/internal/gateway` | `apps/brain/internal/gateway/AGENTS.md` | (brain's) |
| `apps/cli` | `apps/cli/AGENTS.md` | `apps/cli/.agents/skills` |
| `apps/dashboard` | `apps/dashboard/AGENTS.md` | `apps/dashboard/.agents/skills` |
| `sdk/agent` | none | `sdk/agent/.agents/skills` |
| `sdk/stream` | none | none |
| repo root | this file | `.agents/skills`: file-an-issue, open-a-pull-request, running-an-agent-team, update-project-docs, upgrade-the-go-toolchain |

Start your agent in the app's directory to get that app's instructions and
skills.

Every app is a separate Go or TypeScript module. The only thing that crosses an
app boundary is `apps/brain/api/openapi.yaml`.

Commits follow Conventional Commits, scoped by app: `feat(brain): ...`,
`fix(cli): ...`, `docs: ...`. Pull requests are squash-merged, so the pull
request title is the commit subject. No agent attribution in either: no
`Co-Authored-By` trailer naming an AI and no "Generated with" line in a commit
message or a pull request description.

Add a skill under the level's `.agents/skills/<name>/SKILL.md` and link it with `ln -s ../../.agents/skills/<name> .claude/skills/<name>`.
