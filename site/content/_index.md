---
title: "Openarity"
layout: hextra-home
---

{{< hextra/hero-badge link="https://github.com/LaplacianAI/openarity" >}}
  <span>Early — no release yet</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  Capability is not a flat list
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  An open-source agent platform that selects capability by walking a graph
  rather than ranking a list — and an enterprise harness around the run.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Read the docs" link="docs" >}}
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Traversal, not ranking"
    subtitle="Tool selection measurably collapses as the pool grows, and retrieving tools by embedding only postpones it — the paper that popularised that fix reports its own precision degrading into the thousands. What separates two similar tools is not in their descriptions. It is what they connect to."
    link="docs/why-a-graph"
  >}}
  {{< hextra/feature-card
    title="Graph engineering, not GraphRAG"
    subtitle="A knowledge graph models what a system knows. This one models what it is — agents, tools, skills, teams and learnings, with edges of authority and provenance. Asking what a run may do and what it can do is the same walk."
    link="docs/why-a-graph"
  >}}
  {{< hextra/feature-card
    title="Boundaries that fail loudly"
    subtitle="The agent loop cannot authorise anything: it is a separate module, so the import does not compile. It cannot reach a provider either — a CI check fails on the linked package. Rules a reviewer has to remember last until the first deadline."
    link="docs/the-harness"
  >}}
  {{< hextra/feature-card
    title="Deletion that actually deletes"
    subtitle="Attachments are encrypted with a per-team key. Deleting a team destroys the key first, so every one of its attachments is unreadable before a single object has been removed — because no transaction spans Postgres, an object store and a vault."
    link="docs/the-harness"
  >}}
  {{< hextra/feature-card
    title="One graph, many surfaces"
    subtitle="Code, Work, Knowledge, Design, Mobile. Not products that share a vendor — views onto the same graph, so a learning earned in one is capability in every other, with nothing to integrate. None of it is built."
    link="docs/surfaces"
  >}}
  {{< hextra/feature-card
    title="An agent loop you can take on its own"
    subtitle="sdk/agent is a separate Go module with one dependency and no database, server or configuration file. ReAct, plan-then-act, ReWOO and reflection, behind an interface you can add your own pattern to."
    link="docs/agent-sdk"
  >}}
{{< /hextra/feature-grid >}}
