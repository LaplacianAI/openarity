---
title: Documentation
next: /docs/get-started
---

Openarity is an agent platform whose authority comes from a graph rather than a
flat list of permissions. Two parts of it run today, and they are independent of
each other.

{{< cards >}}
  {{< card link="get-started" title="Get started" icon="download" subtitle="One command for the whole stack, or a brain against a Postgres you already have." >}}
  {{< card link="examples" title="Examples" icon="code" subtitle="Eleven runnable agents. They work with nothing installed and no key." >}}
{{< /cards >}}

## The parts

{{< cards >}}
  {{< card link="platform" title="Platform" icon="server" subtitle="The brain, the CLI and the inbound gateway — running Openarity itself." >}}
  {{< card link="agent-sdk" title="Agent SDK" icon="code" subtitle="sdk/agent, a Go module you can install into a project that has never heard of the rest." >}}
  {{< card link="reference" title="Reference" icon="template" subtitle="Every command, every endpoint, every environment variable." >}}
{{< /cards >}}

The SDK is a separate module on purpose: which tools and skills a run may see is
an authorisation decision, and keeping the loop out of that module means the
decision cannot leak into it — the compiler refuses the import rather than a
reviewer noticing.

## Why it is built this way

{{< cards >}}
  {{< card link="concepts" title="Concepts" icon="share" subtitle="The argument: why a graph, what the harness owes you, and where none of it helps." >}}
{{< /cards >}}

{{< callout type="warning" >}}
Early. There is no release and no stable API. The graph, the planner and the
dashboard do not exist yet — every page says what is built and what is not.
{{< /callout >}}
