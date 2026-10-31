---
title: Skills
---

A skill is a directory with `SKILL.md` at the top, as the Agent Skills
specification lays it out. Its frontmatter is its name and description, and
the description is what a model reads when deciding whether to load it — so a
run carries every skill's description and only the bodies it asks for. See
[Tools and skills](../agent-sdk/tools-and-skills.md) for why that costs one
entry in the tool list.

## Getting one into a team

```sh
oa skills upload platform ./pdf-processing          # a directory, or a .zip
oa skills import platform github:anthropics/skills/skills/pdf@main
oa skills sync platform pdf                          # fetch an imported skill again
```

**Upload** sends a directory or a zip holding `SKILL.md`, at the top or inside
one folder named as the skill. A `.git` directory is left out; the brain
refuses a path it does not like rather than cleaning it.

**Import** takes `github:<owner>/<repo>/<path>@<ref>`, or
`https://<host>/<path>.zip` from a host listed in the brain's
`OPENARITY_SKILL_IMPORT_HOSTS`. The brain fetches it, records the exact commit
or digest, and `sync` fetches it again. Replacing an imported skill's files
makes it the team's own: it is no longer synced.

## Reading and granting

```sh
oa skills get platform pdf                # frontmatter, source, files
oa skills get platform pdf -o yaml        # the same, with SKILL.md's body
oa skills cat platform pdf forms.md       # one bundled file's bytes, as stored
oa agents replace platform triage -f triage.yaml --skill pdf
```

`SKILL.md` is kept as the skill's frontmatter and body, not as a file, so
`oa skills get` shows it and `oa skills cat` reads the files beside it. `cat`
writes the stored bytes to stdout; to a terminal it refuses a
file holding control characters unless given `--raw`, since a skill's files are
written by its team or its author.

Seeing a team's skills needs only to be in the team; uploading, importing,
syncing, replacing or deleting one needs `skill:write`. A skill an agent has is
not deleted: take it from the agent first.
