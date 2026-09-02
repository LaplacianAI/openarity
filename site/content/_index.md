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
  An open-source agent platform where agents, tools, skills and&nbsp;learnings
  are a knowledge graph. What a tool relates to is the&nbsp;signal.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Read the docs" link="docs" >}}
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Chosen by traversal, not by rank"
    subtitle="Most frameworks pick tools by scoring a flat list. Openarity walks the graph — the skills that use a tool, the learnings derived from it, the agents that hold it."
  >}}
  {{< hextra/feature-card
    title="An agent loop you can take on its own"
    subtitle="sdk/agent is a separate Go module with one dependency. ReAct, plan-then-act and ReWOO, behind an interface you can add your own pattern to."
    link="docs/agent-sdk"
  >}}
  {{< hextra/feature-card
    title="Every channel, one message"
    subtitle="A webhook arrives on its own listener, is verified against that channel's signing secret, and becomes the same normalised message whatever sent it."
  >}}
  {{< hextra/feature-card
    title="Authorisation is rows, not a release"
    subtitle="Roles and their permissions live in Postgres. Adding a role is a migration. A verified caller becomes a user on first sight, so nobody is pre-provisioned."
  >}}
  {{< hextra/feature-card
    title="Honest about what is built"
    subtitle="The graph, the planner and the dashboard do not exist yet. The docs say so on every page rather than describing intentions in the present tense."
  >}}
  {{< hextra/feature-card
    title="MIT, and readable"
    subtitle="Go, Postgres, one hand-written OpenAPI contract every app is generated from. No framework to learn before you can read it."
  >}}
{{< /hextra/feature-grid >}}
