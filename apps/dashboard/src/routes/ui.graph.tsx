import type { Data, FalkorDBCanvas, GraphLink, GraphNode, LayoutMode } from "@falkordb/canvas";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { getGraph } from "@/api";
import { ApiError } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import type { GraphEdge, GraphLabel, GraphNode as Node, TeamGraph } from "@/api/types";
import { Button } from "@/components/ui/button";
import { EmptyState, Mono, PageHeader } from "@/components/ui-bits";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/ui/graph")({
  component: Graph,
});

const LIMIT = 500;

/**
 * Five kinds of node and three hues. With every node on screen at once only
 * three hues stay apart for every reader, colour-blind included — checked
 * with the data-viz validator in both modes. So a server and its tools share
 * orange and differ in shape and size, the team is neutral, and every node
 * carries its own name: colour is never the only thing that tells two apart.
 */
const STYLE: Record<
  GraphLabel,
  { light: string; dark: string; size: number; shape: "circle" | "square"; plural: string }
> = {
  Agent: { light: "#2a78d6", dark: "#3987e5", size: 11, shape: "circle", plural: "Agents" },
  Skill: { light: "#1baf7a", dark: "#199e70", size: 9, shape: "circle", plural: "Skills" },
  Toolkit: { light: "#eb6834", dark: "#d95926", size: 11, shape: "square", plural: "MCP servers" },
  Tool: { light: "#eb6834", dark: "#d95926", size: 6, shape: "circle", plural: "Tools" },
  Team: { light: "#8a8986", dark: "#6f6e69", size: 13, shape: "circle", plural: "Team" },
};

const LABELS: GraphLabel[] = ["Agent", "Skill", "Toolkit", "Tool", "Team"];

const EDGE_WORDS: Record<GraphEdge["type"], string> = {
  OWNS: "owns",
  GRANTED: "is granted",
  CONTAINS: "contains",
  HAS_SKILL: "has the skill",
};

/**
 * A tooltip's content, as text. force-graph hands a label string to its
 * tooltip, which writes it with innerHTML — so a node named
 * `<img src=x onerror=…>` by a team member, or a tool named that way by an
 * MCP server, would run in this page, which holds the session's token. An
 * element is appended as it is and never parsed, so the name is set as its
 * text and stays text.
 */
export function textLabel(text: string): HTMLElement {
  const span = document.createElement("span");
  span.textContent = text;
  return span;
}

/**
 * Replaces the canvas's own tooltip labels, which are strings and so are
 * parsed as HTML, with ones that are text. The canvas sets them when it
 * builds its graph, on the first setData, so this runs after every one.
 */
export function safeLabels(el: FalkorDBCanvas): void {
  const graph = el.getGraph() as unknown as
    | {
        nodeLabel: (fn: (n: GraphNode) => HTMLElement) => unknown;
        linkLabel: (fn: (l: GraphLink) => HTMLElement) => unknown;
      }
    | undefined;
  graph?.nodeLabel((n) => textLabel(String(n.data?.name ?? "")));
  graph?.linkLabel((l) => textLabel(String(l.relationship ?? "")));
}

/** Where a node is managed, so a click in the graph leads somewhere. */
const PAGE: Partial<Record<GraphLabel, string>> = {
  Agent: "/ui/agents",
  Skill: "/ui/skills",
  Toolkit: "/ui/servers",
  Tool: "/ui/servers",
};

/**
 * The API's graph as the canvas takes it. The canvas numbers its nodes, so
 * each uuid is given a number and kept in the node's data; an edge whose ends
 * are not both present — a truncated graph — is dropped rather than drawn
 * to nothing.
 */
export function toCanvas(graph: TeamGraph, hidden: Set<GraphLabel>, theme: "light" | "dark"): Data {
  const numbers = new Map<string, number>();
  for (const [i, n] of graph.nodes.entries()) numbers.set(n.id, i + 1);

  const nodes = graph.nodes.map((n) => {
    const style = STYLE[n.label];
    return {
      id: numbers.get(n.id) as number,
      labels: [n.label],
      color: style[theme],
      size: style.size,
      shape: style.shape,
      visible: !hidden.has(n.label),
      data: {
        name: n.name ?? (n.label === "Team" ? "team" : n.id.slice(0, 8)),
        uuid: n.id,
        ...(n.description ? { description: n.description } : {}),
        ...(n.kind ? { kind: n.kind } : {}),
        ...(n.model ? { model: n.model } : {}),
      },
    };
  });

  const muted = theme === "dark" ? "#77766f" : "#a9a8a2";
  const links = graph.edges.flatMap((e, i) => {
    const source = numbers.get(e.from);
    const target = numbers.get(e.to);
    if (source === undefined || target === undefined) return [];
    const from = graph.nodes[source - 1];
    const to = graph.nodes[target - 1];
    return [
      {
        id: i + 1,
        relationship: e.type,
        color: muted,
        source,
        target,
        visible: Boolean(from && to && !hidden.has(from.label) && !hidden.has(to.label)),
        data: e.only ? { only: e.only.join(", ") } : {},
      },
    ];
  });

  return { nodes, links } as Data;
}

function Graph() {
  const { teamId } = useCurrentTeam();
  const [hidden, setHidden] = useState<Set<GraphLabel>>(() => new Set());
  const [layout, setLayout] = useState<LayoutMode>("force");
  const [selected, setSelected] = useState<string | null>(null);
  const [asList, setAsList] = useState(false);

  const graph = useQuery({
    queryKey: ["graph", teamId],
    queryFn: ({ signal }) => getGraph(teamId as string, LIMIT, signal),
    enabled: Boolean(teamId),
    retry: false,
  });

  const counts = useMemo(() => {
    const c: Partial<Record<GraphLabel, number>> = {};
    for (const n of graph.data?.nodes ?? []) c[n.label] = (c[n.label] ?? 0) + 1;
    return c;
  }, [graph.data]);

  const toggle = (label: GraphLabel) =>
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      return next;
    });

  const unavailable = graph.error instanceof ApiError && graph.error.status === 503;

  return (
    <div className="space-y-4">
      <PageHeader
        title="Graph"
        description="The team's agents, MCP servers, tools and skills as the graph index holds them. It catches up with changes within about a minute."
        actions={
          <>
            <Button size="sm" variant="outline" onClick={() => setAsList(!asList)}>
              {asList ? "Show as graph" : "Show as list"}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={graph.isFetching}
              onClick={() => graph.refetch()}
            >
              {graph.isFetching ? "Loading…" : "Refresh"}
            </Button>
          </>
        }
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its graph." />
      ) : graph.isPending ? (
        <p className="text-sm text-muted-foreground">Loading the graph…</p>
      ) : unavailable ? (
        <EmptyState
          title="The graph is not enabled"
          body={`${graph.error?.message ?? "No graph"}. The worker projects agents, tools and skills into the graph once an embedding model is set: OPENARITY_EMBEDDING_MODEL and OPENARITY_EMBEDDING_DIMENSIONS.`}
          hint="See “The graph” in deployment/README.md — `make ollama` runs a model on your machine."
        />
      ) : graph.isError ? (
        <EmptyState title="The graph could not be read" body={graph.error.message} />
      ) : graph.data.nodes.length <= 1 ? (
        <EmptyState
          title="Nothing in the graph yet"
          body="Define an agent, register an MCP server or upload a skill, and it appears here once the worker has projected it."
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <fieldset className="flex flex-wrap items-center gap-1.5" aria-label="Show">
              {LABELS.filter((l) => counts[l]).map((label) => (
                <LegendChip
                  key={label}
                  label={label}
                  count={counts[label] ?? 0}
                  shown={!hidden.has(label)}
                  onToggle={() => toggle(label)}
                />
              ))}
            </fieldset>
            {!asList ? (
              <div className="flex items-center gap-1" role="radiogroup" aria-label="Layout">
                {(["force", "tree", "radial"] as const).map((mode) => (
                  <Button
                    key={mode}
                    size="sm"
                    variant={layout === mode ? "secondary" : "ghost"}
                    className="text-xs capitalize"
                    role="radio"
                    aria-checked={layout === mode}
                    onClick={() => setLayout(mode)}
                  >
                    {mode}
                  </Button>
                ))}
              </div>
            ) : null}
          </div>

          {graph.data.truncated ? (
            <p className="text-xs text-muted-foreground">
              Showing the first {graph.data.nodes.length} nodes; the team has more.
            </p>
          ) : null}

          {asList ? (
            <GraphList graph={graph.data} hidden={hidden} />
          ) : (
            <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
              <Canvas graph={graph.data} hidden={hidden} layout={layout} onSelect={setSelected} />
              <Selection graph={graph.data} selected={selected} />
            </div>
          )}
        </>
      )}
    </div>
  );
}

function LegendChip({
  label,
  count,
  shown,
  onToggle,
}: {
  label: GraphLabel;
  count: number;
  shown: boolean;
  onToggle: () => void;
}) {
  const { theme } = useTheme();
  const style = STYLE[label];
  return (
    <button
      type="button"
      aria-pressed={shown}
      onClick={onToggle}
      className={cn(
        "flex items-center gap-1.5 rounded-md border border-border px-2 py-1 text-xs",
        shown ? "bg-card" : "bg-transparent text-muted-foreground line-through",
      )}
    >
      <span
        aria-hidden
        className={cn("inline-block", style.shape === "square" ? "rounded-[2px]" : "rounded-full")}
        style={{
          backgroundColor: style[theme],
          width: style.size >= 9 ? 10 : 7,
          height: style.size >= 9 ? 10 : 7,
        }}
      />
      {style.plural}
      <span className="text-muted-foreground">{count}</span>
    </button>
  );
}

/**
 * The FalkorDB canvas, loaded when this screen is first shown. It is a web
 * component that draws on a canvas, so it is made imperatively rather than
 * through JSX, and its module — d3 and force-graph — is fetched only by
 * someone who opens the graph.
 */
function Canvas({
  graph,
  hidden,
  layout,
  onSelect,
}: {
  graph: TeamGraph;
  hidden: Set<GraphLabel>;
  layout: LayoutMode;
  onSelect: (id: string | null) => void;
}) {
  const { theme } = useTheme();
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<FalkorDBCanvas | null>(null);
  const [ready, setReady] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const select = useRef(onSelect);
  select.current = onSelect;

  useEffect(() => {
    let cancelled = false;
    import("@falkordb/canvas")
      .then(() => {
        if (cancelled || !host.current) return;
        const el = document.createElement("falkordb-canvas");
        el.style.display = "block";
        el.style.width = "100%";
        el.style.height = "100%";
        host.current.replaceChildren(el);
        canvas.current = el;
        setReady(true);
      })
      .catch((err: Error) => setFailed(err.message));
    return () => {
      cancelled = true;
      canvas.current = null;
      host.current?.replaceChildren();
    };
  }, []);

  useEffect(() => {
    const el = canvas.current;
    if (!ready || !el || !host.current) return;
    const styles = getComputedStyle(document.documentElement);
    el.setConfig({
      width: host.current.clientWidth,
      height: host.current.clientHeight,
      backgroundColor:
        styles.getPropertyValue("--card").trim() || (theme === "dark" ? "#1f1f1d" : "#ffffff"),
      foregroundColor:
        styles.getPropertyValue("--foreground").trim() ||
        (theme === "dark" ? "#f5f5f4" : "#1a1a19"),
      captionsKeys: [["name", true]],
      layoutMode: layout,
      eventHandlers: {
        onNodeClick: (node: GraphNode) => select.current(String(node.data.uuid)),
        onBackgroundClick: () => select.current(null),
      },
    });
  }, [ready, theme, layout]);

  useEffect(() => {
    const el = canvas.current;
    if (!ready || !el) return;
    el.setData(toCanvas(graph, hidden, theme));
    safeLabels(el);
  }, [ready, graph, hidden, theme]);

  return (
    <div className="relative h-[65vh] min-h-96 overflow-hidden rounded-md border border-border bg-card">
      <div ref={host} className="absolute inset-0" data-testid="graph-canvas" />
      {failed ? (
        <p className="absolute inset-0 flex items-center justify-center p-6 text-sm text-destructive">
          The graph view could not load: {failed}
        </p>
      ) : !ready ? (
        <p className="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
          Drawing…
        </p>
      ) : null}
    </div>
  );
}

function nameOf(n: Node | undefined): string {
  if (!n) return "?";
  return n.name ?? (n.label === "Team" ? "this team" : n.id.slice(0, 8));
}

function Selection({ graph, selected }: { graph: TeamGraph; selected: string | null }) {
  const byId = useMemo(() => new Map(graph.nodes.map((n) => [n.id, n])), [graph]);
  const node = selected ? byId.get(selected) : undefined;

  if (!node) {
    return (
      <aside className="rounded-md border border-border bg-card p-3 text-xs text-muted-foreground">
        Click a node to see what it is joined to. Drag to move one; scroll to zoom.
      </aside>
    );
  }

  const out = graph.edges.filter((e) => e.from === node.id);
  const into = graph.edges.filter((e) => e.to === node.id);
  const page = PAGE[node.label];

  return (
    <aside className="space-y-3 rounded-md border border-border bg-card p-3 text-xs">
      <div>
        <p className="text-muted-foreground">{STYLE[node.label].plural.replace(/s$/, "")}</p>
        <h2 className="text-sm font-semibold">{nameOf(node)}</h2>
        {node.description ? <p className="mt-1 text-muted-foreground">{node.description}</p> : null}
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
        {node.kind ? (
          <>
            <dt className="text-muted-foreground">Kind</dt>
            <dd>{node.kind}</dd>
          </>
        ) : null}
        <dt className="text-muted-foreground">Embedded</dt>
        <dd>{node.model ? <Mono>{node.model}</Mono> : "no vector"}</dd>
        <dt className="text-muted-foreground">Id</dt>
        <dd>
          <Mono className="break-all">{node.id}</Mono>
        </dd>
      </dl>
      {out.length + into.length > 0 ? (
        <ul className="space-y-1">
          {out.map((e) => (
            <li key={`o-${e.type}-${e.to}`}>
              {EDGE_WORDS[e.type]} <span className="font-medium">{nameOf(byId.get(e.to))}</span>
              {e.only ? (
                <span className="text-muted-foreground"> — only {e.only.join(", ")}</span>
              ) : null}
            </li>
          ))}
          {into.map((e) => (
            <li key={`i-${e.type}-${e.from}`} className="text-muted-foreground">
              <span className="font-medium text-foreground">{nameOf(byId.get(e.from))}</span>{" "}
              {EDGE_WORDS[e.type]} it
            </li>
          ))}
        </ul>
      ) : null}
      {page ? (
        <Link to={page} className="inline-block text-primary underline-offset-4 hover:underline">
          Manage on its page
        </Link>
      ) : null}
    </aside>
  );
}

/** The same graph as text: what a screen reader, or a crowded graph, needs. */
function GraphList({ graph, hidden }: { graph: TeamGraph; hidden: Set<GraphLabel> }) {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const shown = graph.edges.filter((e) => {
    const a = byId.get(e.from);
    const b = byId.get(e.to);
    return a && b && !hidden.has(a.label) && !hidden.has(b.label) && e.type !== "OWNS";
  });

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <table className="w-full rounded-md border border-border bg-card text-left text-xs">
        <caption className="px-3 py-2 text-left font-medium">Nodes</caption>
        <tbody className="divide-y divide-border">
          {graph.nodes
            .filter((n) => !hidden.has(n.label))
            .map((n) => (
              <tr key={n.id}>
                <td className="px-3 py-1.5 text-muted-foreground">{n.label}</td>
                <td className="py-1.5 font-medium">{nameOf(n)}</td>
                <td className="px-3 py-1.5 text-right">
                  {n.model ? <Mono>{n.model}</Mono> : null}
                </td>
              </tr>
            ))}
        </tbody>
      </table>
      <table className="w-full rounded-md border border-border bg-card text-left text-xs">
        <caption className="px-3 py-2 text-left font-medium">Relationships</caption>
        <tbody className="divide-y divide-border">
          {shown.length === 0 ? (
            <tr>
              <td className="px-3 py-1.5 text-muted-foreground">None besides ownership.</td>
            </tr>
          ) : (
            shown.map((e) => (
              <tr key={`${e.type}-${e.from}-${e.to}`}>
                <td className="px-3 py-1.5 font-medium">{nameOf(byId.get(e.from))}</td>
                <td className="py-1.5 text-muted-foreground">{EDGE_WORDS[e.type]}</td>
                <td className="px-3 py-1.5 font-medium">
                  {nameOf(byId.get(e.to))}
                  {e.only ? (
                    <span className="font-normal text-muted-foreground">
                      {" "}
                      ({e.only.join(", ")})
                    </span>
                  ) : null}
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
