---
title: openarity
---

**Capability is not a flat list.**

An open-source agent platform that selects capability by walking a graph
rather than ranking a list — and an enterprise harness around the run.

![A swarm of agents joined by edges converges into the openarity mark, a rounded-square O; it splits on the diagonal into two halves, a visor opens, a signal runs down the cut from one node to the other, and the name openarity arrives beside it](assets/intro-light.gif#only-light)
![A swarm of agents joined by edges converges into the openarity mark, a rounded-square O; it splits on the diagonal into two halves, a visor opens, a signal runs down the cut from one node to the other, and the name openarity arrives beside it](assets/intro-dark.gif#only-dark)

## Get started

Run the whole stack with one command, put a brain in front of a Postgres you
already have, or take the agent loop on its own. **[Read the docs](docs/index.md)**.

<div class="grid cards" markdown>

- **[Traversal, not ranking](docs/concepts/why-a-graph.md)**: Tool selection measurably collapses as the pool grows, and retrieving tools by embedding only postpones it — the paper that popularised that fix reports its own precision degrading into the thousands. What separates two similar tools is not in their descriptions. It is what they connect to.
- **[Graph engineering, not GraphRAG](docs/concepts/why-a-graph.md)**: A knowledge graph models what a system knows. This one models what it is — agents, tools, skills, teams and learnings, with edges of authority and provenance. Asking what a run may do and what it can do is the same walk.
- **[Boundaries that fail loudly](docs/concepts/the-harness.md)**: The agent loop cannot authorise anything: it is a separate module, so the import does not compile. It cannot reach a provider either — a CI check fails on the linked package. Rules a reviewer has to remember last until the first deadline.
- **[Deletion that actually deletes](docs/concepts/the-harness.md)**: Attachments are encrypted with a per-team key. Deleting a team destroys the key first, so every one of its attachments is unreadable before a single object has been removed — because no transaction spans Postgres, an object store and a vault.
- **[One graph, many surfaces](docs/concepts/surfaces.md)**: Code, Work, Knowledge, Design, Mobile. Not products that share a vendor — views onto the same graph, so a learning earned in one is capability in every other, with nothing to integrate. None of it is built.
- **[An agent loop you can take on its own](docs/agent-sdk/index.md)**: sdk/agent is a separate Go module with one dependency and no database, server or configuration file. ReAct, plan-then-act, ReWOO and reflection, behind an interface you can add your own pattern to.

</div>

!!! warning "Early"

    There is no release yet, and no stable API. Every page says what is built
    and what is not.
