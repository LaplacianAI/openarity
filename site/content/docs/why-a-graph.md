---
title: Why a graph
weight: 1
---

Every agent platform has to answer one question before it can do anything else:
given a task and a thousand things the system could do, which few reach the
model?

Nearly all of them answer it the same way. Put every tool in the prompt, or
embed the tools and retrieve the top *k*. Openarity's argument is that both
break for the same reason, and that the fix is not a better ranking.

## The flat list is measurably failing

This used to be a design opinion. It is now measured.

[RAG-MCP](https://arxiv.org/abs/2505.03275) put a growing pool of MCP tools in
front of a model and watched selection accuracy fall to **13.62%**. Retrieving
tools by embedding first — showing the model only the ones a vector search
returned — lifted it to **43.13%** and cut prompt tokens by more than half.

The cost side is worse than the accuracy side. Practitioner measurements
reported through 2026 put a few ordinary MCP servers at **tens of thousands of
tokens of tool schema** before the user has said anything, and describe output
quality falling away somewhere around fifty always-loaded tools.

So the field has largely settled on retrieval: index the tools, fetch the
relevant ones, keep the prompt small.

## Retrieval is not the end of the argument

RAG-MCP's own conclusion is the part worth reading twice. Its retrieval
precision and throughput **degrade as the registry scales into the thousands**.

That is not a flaw in their implementation. It is what similarity does. A vector
search over tool descriptions asks *which of these reads like the task*, and at
a thousand tools the honest answer is that forty of them do. Ranking cannot
separate them, because the thing that separates them is not in the description:
it is what each tool is connected to.

- The skill that already uses this tool, and what that skill is for
- The learning from the last run that used it, and whether that run went well
- The agent that holds it, and what that agent is allowed to touch
- The team whose credential it would spend

None of that is text similarity. All of it is an edge.

## Traversal, not ranking

Openarity stores agents, tools, skills and past learnings as a graph and
selects by walking it. The question stops being *what looks relevant* and
becomes *what is reachable from here*, which is a different query with a
different failure mode: it can return nothing, and returning nothing is a
better answer than returning forty things that read alike.

{{< callout type="warning" >}}
**The graph is not built yet.** This page describes the design the rest of the
system is being shaped around, not something you can run today. What exists is
in [Platform](../platform) and [Agent SDK](../agent-sdk), and both say plainly
what they do and do not do.
{{< /callout >}}

## This is graph engineering, not GraphRAG

The two get conflated, and the difference is the whole point.

**Knowledge-graph engineering** models what a system *knows* — documents,
entities, the facts connecting them. **Graph engineering**, as the term
settled in 2026, models what a system *is*: its members, their mandates, and
their message paths.

Microsoft's GraphRAG is the first kind, and good at it: it has a model build a
graph from a corpus, clusters the entities into communities, pre-summarises
each community, and beats vector-only retrieval by a wide margin on global
questions — *what are the themes in this corpus*. If your problem is a large
pile of documents, that is the technique.

Openarity's graph is the second kind. Its nodes are not paragraphs. They are
agents, tools, skills, teams, channels and learnings, and its edges are
authority and provenance: who may call what, which skill uses which tool, which
learning came from which run. A traversal is therefore an answer to a question
about **capability**, and the same traversal is an answer about
**authorisation** — the two are the same walk, which is why they cannot drift
apart.

Both kinds can coexist. A document graph is one region of a capability graph,
reached by the tool that queries it.

## Where a graph does not help

A page arguing for graphs should say where they lose, or it is marketing.

The [GEM 2026](https://aclanthology.org/2026.gem-main.40/) framework evaluated
nine RAG configurations and reports a **retrieval–generation gap**: retrieving
more does not proportionally improve what is generated. Handing a model more
context, however well-chosen, has a ceiling — their context-engineering method
cuts token usage by 19–53% without a matching quality loss, which is the same
finding from the other side.

Concretely:

- **Ten tools do not need a graph.** They need a good prompt. The overhead
  arrives before the benefit, and a small system pays it for nothing.
- **A single-hop lookup does not need a graph.** If the right tool is the one
  whose description matches, similarity already found it.
- **A graph nobody maintains is worse than a list.** Edges that were true last
  quarter route confidently into the wrong place, and nothing about a
  confident wrong answer looks wrong.

The claim is not that graphs beat retrieval. It is that capability selection is
a relationship problem wearing a similarity problem's clothes, and that it gets
harder in exactly the range — hundreds to thousands of tools, across teams, with
history — where similarity is reported to fall apart.

## What follows from it

Two things in the codebase already exist because of this argument, before the
graph does.

**Tools carry their implementation as a closure**, so the credential, the MCP
server and the network call stay on the caller's side. A tool is a *node with an
owner*, not a schema in a registry — which is what makes an edge to a team
meaningful.

**Skills spend their description and not their body.** Sixty skills cost one
entry in the tool list, because the listing is a cheap projection of a subgraph
and the body is only paid for when the walk actually reaches it. That is
progressive disclosure applied to capability, and it is the shape the graph
will feed. See [Tools and skills](../agent-sdk/tools-and-skills).
