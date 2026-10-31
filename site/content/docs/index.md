---
title: Documentation
---

Openarity is an agent platform whose authority comes from a graph rather than a
flat list of permissions. Two parts of it run today, and they are independent of
each other.

<div class="grid cards" markdown>

  - **[Get started](get-started/index.md)**: One command for the whole stack, or a brain against a Postgres you already have.
  - **[Examples](agent-sdk/examples.md)**: Eleven runnable agents. They work with nothing installed and no key.

</div>

## The parts

<div class="grid cards" markdown>

  - **[Platform](platform/index.md)**: The brain, the CLI and the inbound gateway — running Openarity itself.
  - **[Agent SDK](agent-sdk/index.md)**: sdk/agent, a Go module you can install into a project that has never heard of the rest.
  - **[Reference](reference/index.md)**: Every command, every endpoint, every environment variable.

</div>

The SDK is a separate module on purpose: which tools and skills a run may see is
an authorisation decision, and keeping the loop out of that module means the
decision cannot leak into it — the compiler refuses the import rather than a
reviewer noticing.

## Why it is built this way

<div class="grid cards" markdown>

  - **[Concepts](concepts/index.md)**: The argument: why a graph, what the harness owes you, and where none of it helps.

</div>

!!! warning

    Early. There is no release and no stable API, and every page says what is
    built and what is not.
