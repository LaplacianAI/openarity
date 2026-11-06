import type { Data, FalkorDBCanvas, GraphLink, GraphNode, LayoutMode } from "@falkordb/canvas";
import { useQueries, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { getAgent, getGraph, getTeamSettings, listAgents } from "@/api";
import { ApiError } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import type { GraphLabel } from "@/api/types";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, Mono, PageHeader } from "@/components/ui-bits";
import {
  BUILT_IN_HUB,
  type DrawnEdgeType,
  type DrawnGraph,
  isDelegation,
  type DrawnNode as Node,
  withDelegation,
} from "@/lib/graph-delegation";
import { type DrawState, drawLink, drawNode, paintLinkArea, paintNodeArea } from "@/lib/graph-draw";
import { useMediaQuery } from "@/lib/media";
import { count } from "@/lib/runs";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/ui/graph")({
  component: Graph,
});

const LIMIT = 500;

/**
 * Five kinds of node in BRAND's Spectral palette and nothing else: agents in
 * the one teal, everything they reach in its neutrals. Kinds are told apart
 * by shape and size as well as tone, a skill and the team as rings, and every
 * node carries its own name: colour is never the only thing that tells two
 * apart. ok, warn and error stay for status, which a node kind is not.
 */
const STYLE: Record<
  GraphLabel,
  {
    light: string;
    dark: string;
    size: number;
    shape: "circle" | "square";
    hollow?: boolean;
    plural: string;
  }
> = {
  Agent: { light: "#0B6A5C", dark: "#5FD3BC", size: 14, shape: "circle", plural: "Agents" },
  Skill: {
    light: "#5F6763",
    dark: "#8A9391",
    size: 10,
    shape: "circle",
    hollow: true,
    plural: "Skills",
  },
  Toolkit: { light: "#2E3A3D", dark: "#D9D3C7", size: 13, shape: "square", plural: "MCP servers" },
  Tool: { light: "#5F6763", dark: "#8A9391", size: 7, shape: "circle", plural: "Tools" },
  Team: {
    light: "#2E3A3D",
    dark: "#D9D3C7",
    size: 16,
    shape: "circle",
    hollow: true,
    plural: "Team",
  },
};

const LABELS: GraphLabel[] = ["Agent", "Skill", "Toolkit", "Tool", "Team"];

const EDGE_WORDS: Record<DrawnEdgeType, string> = {
  OWNS: "owns",
  GRANTED: "is granted",
  CONTAINS: "contains",
  HAS_SKILL: "has the skill",
  HANDS_TO: "hands work to",
  HANDS_ANY: "hands work to any agent of",
  ANSWERS: "answers first for",
};

/** The same edges read from their target: "a skill of triage". */
const EDGE_WORDS_INTO: Record<DrawnEdgeType, string> = {
  OWNS: "owned by",
  GRANTED: "granted to",
  CONTAINS: "a tool of",
  HAS_SKILL: "a skill of",
  HANDS_TO: "takes work from",
  HANDS_ANY: "its agents take work from",
  ANSWERS: "answered first by",
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

/** Where a node is managed, so a click in the graph leads somewhere. An agent has its own page. */
const PAGE: Partial<Record<GraphLabel, string>> = {
  Skill: "/ui/skills",
  Toolkit: "/ui/servers",
  Tool: "/ui/servers",
};

/**
 * The API's graph as the canvas takes it. The canvas numbers its nodes, so
 * each uuid is given a number and kept in the node's data; an edge whose ends
 * are not both present — a truncated graph — is dropped rather than drawn
 * to nothing. A hidden kind is left out rather than drawn invisible: the
 * tree and radial layouts place every node they are given, and an invisible
 * team would still be the root everything else hangs from. Numbers come from
 * the whole graph, so a node keeps its number when a kind is toggled.
 */
export function toCanvas(
  graph: DrawnGraph,
  hidden: Set<GraphLabel>,
  theme: "light" | "dark",
): Data {
  const numbers = new Map<string, number>();
  for (const [i, n] of graph.nodes.entries()) numbers.set(n.id, i + 1);

  const nodes = graph.nodes.flatMap((n) => {
    if (hidden.has(n.label)) return [];
    const style = STYLE[n.label];
    const node = {
      id: numbers.get(n.id) as number,
      labels: [n.label],
      color: style[theme],
      size: style.size,
      shape: style.shape,
      visible: true,
      data: {
        name: n.name ?? (n.label === "Team" ? "team" : n.id.slice(0, 8)),
        uuid: n.id,
        ...(n.description ? { description: n.description } : {}),
        ...(n.kind ? { kind: n.kind } : {}),
        ...(n.model ? { model: n.model } : {}),
        ...(n.hub ? { hub: true } : {}),
        ...(style.hollow ? { hollow: true } : {}),
      },
    };
    return [node];
  });

  const muted = theme === "dark" ? "#77766f" : "#a9a8a2";
  const links = graph.edges.flatMap((e, i) => {
    const source = numbers.get(e.from);
    const target = numbers.get(e.to);
    if (source === undefined || target === undefined) return [];
    const from = graph.nodes[source - 1];
    const to = graph.nodes[target - 1];
    if (!from || !to || hidden.has(from.label) || hidden.has(to.label)) return [];
    return [
      {
        id: i + 1,
        relationship: e.type,
        color: muted,
        source,
        target,
        visible: true,
        data: {
          ...(e.only ? { only: e.only.join(", ") } : {}),
          ...(isDelegation(e.type) ? { delegation: true } : {}),
        },
      },
    ];
  });

  return { nodes, links } as Data;
}

/**
 * The MCP servers and skills no agent is granted: in the team, and of no use
 * to it. A graph cut short cannot say, since the grant may be past the cut.
 */
export function unreached(graph: DrawnGraph): Node[] {
  if (graph.truncated) return [];
  const agents = new Set(graph.nodes.filter((n) => n.label === "Agent").map((n) => n.id));
  const held = new Set(
    graph.edges
      .filter((e) => (e.type === "GRANTED" || e.type === "HAS_SKILL") && agents.has(e.from))
      .map((e) => e.to),
  );
  return graph.nodes.filter(
    (n) => (n.label === "Toolkit" || n.label === "Skill") && !held.has(n.id),
  );
}

/** "1 MCP server and 2 skills no agent is granted.", or nothing. */
export function unreachedFinding(nodes: Node[]): string | null {
  const servers = nodes.filter((n) => n.label === "Toolkit").length;
  const skills = nodes.filter((n) => n.label === "Skill").length;
  const parts = [
    servers ? count(servers, "MCP server") : null,
    skills ? count(skills, "skill") : null,
  ].filter(Boolean);
  return parts.length ? `${parts.join(" and ")} no agent is granted.` : null;
}

/** The shown nodes whose name holds the query, ignoring case; none for an empty one. */
export function matching(graph: DrawnGraph, hidden: Set<GraphLabel>, query: string): Node[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [];
  return graph.nodes.filter((n) => !hidden.has(n.label) && nameOf(n).toLowerCase().includes(q));
}

/** How many matches the search offers at once. */
const MATCHES = 8;

function Graph() {
  const { teamId } = useCurrentTeam();
  // Shown from the start: the hub answers for the team, and an agent that may
  // hand work to any agent points at it.
  const [hidden, setHidden] = useState<Set<GraphLabel>>(() => new Set());
  const [layout, setLayout] = useState<LayoutMode>("force");
  const [selected, setSelected] = useState<string | null>(null);
  const [asList, setAsList] = useState(false);
  const [query, setQuery] = useState("");
  // Written as max-width so that where nothing matches (jsdom) the canvas and
  // the side panel are what is drawn.
  const phone = useMediaQuery("(max-width: 39.99rem)");
  const narrow = useMediaQuery("(max-width: 63.99rem)");
  // A phone gets the list: a canvas of hundreds of nodes is not read with a thumb.
  const listed = asList || phone;

  const graph = useQuery({
    queryKey: ["graph", teamId],
    queryFn: ({ signal }) => getGraph(teamId as string, LIMIT, signal),
    enabled: Boolean(teamId),
    retry: false,
  });

  // Who hands work to whom is on the agents, not in the index: the hub from
  // the team's settings, and the delegates of each agent that lists them.
  const agents = useQuery({
    queryKey: ["agents", teamId, "names"],
    queryFn: () => listAgents(teamId as string),
    enabled: Boolean(teamId),
  });
  const settings = useQuery({
    queryKey: ["settings", teamId],
    queryFn: () => getTeamSettings(teamId as string),
    enabled: Boolean(teamId),
    retry: false,
  });
  const listing = (agents.data?.items ?? []).filter((a) => a.delegation === "listed");
  const delegates = useQueries({
    queries: listing.map((a) => ({
      queryKey: ["agents", teamId, a.id],
      queryFn: () => getAgent(teamId as string, a.id),
    })),
  });
  const delegatesOf = new Map(
    delegates.flatMap((q) => (q.data ? [[q.data.id, q.data.delegates] as const] : [])),
  );
  const delegators = (agents.data?.items ?? []).map((a) => ({
    id: a.id,
    delegation: a.delegation,
    delegates: delegatesOf.get(a.id) ?? [],
  }));
  const delegatorsKey = JSON.stringify(delegators);
  const hub = settings.isSuccess ? (settings.data.hub ?? null) : undefined;
  const hubKey = hub === undefined ? "?" : (hub?.id ?? "built-in");
  // biome-ignore lint/correctness/useExhaustiveDependencies: the keys stand for the lists they are made from
  const drawn = useMemo(
    () => (graph.data ? withDelegation(graph.data, delegators, hub) : undefined),
    [graph.data, delegatorsKey, hubKey],
  );

  const counts = useMemo(() => {
    const c: Partial<Record<GraphLabel, number>> = {};
    for (const n of drawn?.nodes ?? []) c[n.label] = (c[n.label] ?? 0) + 1;
    return c;
  }, [drawn]);

  const toggle = (label: GraphLabel) =>
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      return next;
    });

  const unavailable = graph.error instanceof ApiError && graph.error.status === 503;
  const finding = drawn ? unreachedFinding(unreached(drawn)) : null;
  const found = drawn ? matching(drawn, hidden, query) : [];

  return (
    <div className="space-y-4">
      <PageHeader
        title="Graph"
        finding={finding}
        description="The team's hub, its agents and whom they hand work to, and the MCP servers, tools and skills they are granted. Changes show here within about a minute."
        actions={
          <>
            {phone ? null : (
              <Button size="sm" variant="outline" onClick={() => setAsList(!asList)}>
                {asList ? "Show as graph" : "Show as list"}
              </Button>
            )}
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
        <Skeleton className="h-[440px] w-full" role="status" aria-label="Loading the graph" />
      ) : unavailable ? (
        <EmptyState
          title="The graph is not enabled"
          body="Agents, tools and skills are added to the graph once this brain has an embedding model to search them with."
          hint="Ask whoever runs this brain to connect an embedding model."
        />
      ) : graph.isError ? (
        <ErrorState
          what="the graph"
          error={graph.error}
          onRetry={() => graph.refetch()}
          retrying={graph.isRefetching}
        />
      ) : !drawn || graph.data.nodes.length <= 1 ? (
        <EmptyState
          title="Nothing in the graph yet"
          body="Define an agent, register an MCP server or upload a skill, and it appears here once it has been indexed."
        />
      ) : (
        <>
          <Search query={query} onQuery={setQuery} found={found} onPick={setSelected} />

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
            {!listed ? (
              <div className="flex items-center gap-1" role="radiogroup" aria-label="Layout">
                {(["force", "tree", "radial"] as const).map((mode) => (
                  <Button
                    key={mode}
                    size="sm"
                    variant={layout === mode ? "outline" : "ghost"}
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

          {drawn.truncated ? (
            <p className="text-xs text-muted-foreground">
              Showing the first {graph.data.nodes.length} nodes; the team has more.
            </p>
          ) : null}

          <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
            {listed ? (
              <GraphList graph={drawn} hidden={hidden} onPick={setSelected} />
            ) : (
              <Canvas
                graph={drawn}
                hidden={hidden}
                layout={layout}
                selected={selected}
                onSelect={setSelected}
              />
            )}
            {narrow ? null : (
              <aside
                aria-label="Selected node"
                className="h-fit rounded-md border border-border bg-card p-3 text-xs"
              >
                <Selection graph={drawn} selected={selected} />
              </aside>
            )}
          </div>

          {/* Narrower than a desktop, a node is read in a sheet over the graph. */}
          {narrow ? (
            <Sheet open={selected !== null} onOpenChange={(o) => (o ? null : setSelected(null))}>
              <SheetContent>
                <Selection graph={drawn} selected={selected} inSheet />
              </SheetContent>
            </Sheet>
          ) : null}
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
        data-hollow={style.hollow ? "" : undefined}
        style={{
          // A ring for the kinds the canvas draws as rings, so the key matches.
          ...(style.hollow
            ? { border: `1.5px solid ${style[theme]}` }
            : { backgroundColor: style[theme] }),
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
  selected,
  onSelect,
}: {
  graph: DrawnGraph;
  hidden: Set<GraphLabel>;
  layout: LayoutMode;
  selected: string | null;
  onSelect: (id: string | null) => void;
}) {
  const { theme } = useTheme();
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<FalkorDBCanvas | null>(null);
  const [ready, setReady] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const select = useRef(onSelect);
  select.current = onSelect;
  const fit = useRef(true);

  // Read by the drawing on every frame, so a selection repaints without the
  // canvas being configured again.
  const draw = useRef<DrawState>({
    palette: {
      surface: "#ffffff",
      ink: "#1a1a19",
      accent: "#0B6A5C",
      edge: "#c9c8c2",
      edgeStrong: "#6f6e69",
      font: "sans-serif",
    },
    zoom: () => canvas.current?.getZoom() ?? 1,
    focus: null,
    near: new Set(),
  });

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
    const surface =
      styles.getPropertyValue("--card").trim() || (theme === "dark" ? "#1f1f1d" : "#ffffff");
    const ink =
      styles.getPropertyValue("--foreground").trim() || (theme === "dark" ? "#f5f5f4" : "#1a1a19");
    draw.current.palette = {
      surface,
      ink,
      accent:
        styles.getPropertyValue("--primary").trim() || (theme === "dark" ? "#5FD3BC" : "#0B6A5C"),
      edge: theme === "dark" ? "#4a4944" : "#d3d2cc",
      edgeStrong: theme === "dark" ? "#a9a8a2" : "#6f6e69",
      font: getComputedStyle(document.body).fontFamily || "sans-serif",
    };
    el.setConfig({
      width: host.current.clientWidth,
      height: host.current.clientHeight,
      backgroundColor: surface,
      foregroundColor: ink,
      captionsKeys: [["name", true]],
      layoutMode: layout,
      layoutOptions: {
        force: { linkDistance: 110, collisionPadding: 34 },
        tree: { direction: "lr", levelDistance: 220, nodeSpacing: 70 },
        radial: { levelDistance: 170, nodeSpacing: 40 },
      },
      simulation: { chargeStrength: -900 },
      node: {
        nodeCanvasObject: (node, ctx) => drawNode(draw.current, node, ctx),
        nodePointerAreaPaint: paintNodeArea,
      },
      link: {
        linkCanvasObject: (link, ctx, scale) => drawLink(draw.current, link, ctx, scale),
        linkPointerAreaPaint: paintLinkArea,
      },
      eventHandlers: {
        onNodeClick: (node: GraphNode) => select.current(String(node.data.uuid)),
        onBackgroundClick: () => select.current(null),
        // The canvas fits the view 50ms in, while the forces are still
        // spreading the nodes out; fitting again once they settle keeps the
        // whole graph on screen. Once per layout, so a drag is not undone.
        onEngineStop: () => {
          if (!fit.current) return;
          fit.current = false;
          canvas.current?.zoomToFit();
        },
      },
    });
  }, [ready, theme, layout]);

  useEffect(() => {
    const el = canvas.current;
    if (!ready || !el) return;
    fit.current = true;
    el.setData(toCanvas(graph, hidden, theme));
    safeLabels(el);
  }, [ready, graph, hidden, theme]);

  // setConfig records a layout without moving anything; setLayout lays the
  // nodes out again. The first layout is applied by setData.
  const applied = useRef(layout);
  useEffect(() => {
    if (!ready || layout === applied.current) return;
    applied.current = layout;
    fit.current = true;
    canvas.current?.setLayout(layout);
  }, [ready, layout]);

  // A selected node and the nodes joined to it stay lit; the rest fade.
  useEffect(() => {
    const near = new Set<string>();
    if (selected) {
      for (const e of graph.edges) {
        if (e.from === selected) near.add(e.to);
        if (e.to === selected) near.add(e.from);
      }
    }
    draw.current.focus = selected;
    draw.current.near = near;
    if (ready) canvas.current?.refresh();
  }, [ready, selected, graph]);

  return (
    <div className="relative h-[calc(100dvh-15rem)] min-h-[440px] overflow-hidden rounded-md border border-border bg-card">
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

/** A node's kind in the words the legend uses: "MCP server", not the index's "Toolkit". */
function kindOf(n: Node): string {
  return STYLE[n.label].plural.replace(/s$/, "");
}

function nameOf(n: Node | undefined): string {
  if (!n) return "?";
  return n.name ?? (n.label === "Team" ? "this team" : n.id.slice(0, 8));
}

/** Finding a node by name: the matches, each one a click from its detail. */
function Search({
  query,
  onQuery,
  found,
  onPick,
}: {
  query: string;
  onQuery: (q: string) => void;
  found: Node[];
  onPick: (id: string) => void;
}) {
  return (
    <div className="space-y-2">
      <Input
        type="search"
        aria-label="Find in the graph"
        placeholder="Find an agent, server, tool or skill"
        value={query}
        className="max-w-md"
        onChange={(e) => onQuery(e.target.value)}
      />
      {query.trim() !== "" ? (
        found.length === 0 ? (
          <p className="text-xs text-muted-foreground">Nothing shown is named like that.</p>
        ) : (
          <ul aria-label="Matches" className="flex flex-wrap gap-1.5">
            {found.slice(0, MATCHES).map((n) => (
              <li key={n.id}>
                <Button size="sm" variant="outline" onClick={() => onPick(n.id)}>
                  {nameOf(n)}
                  <span className="text-muted-foreground">{kindOf(n)}</span>
                </Button>
              </li>
            ))}
            {found.length > MATCHES ? (
              <li className="self-center text-xs text-muted-foreground">
                and {found.length - MATCHES} more
              </li>
            ) : null}
          </ul>
        )
      ) : null}
    </div>
  );
}

function Selection({
  graph,
  selected,
  inSheet = false,
}: {
  graph: DrawnGraph;
  selected: string | null;
  /** In a sheet the name is the sheet's title, so the dialog is named by it. */
  inSheet?: boolean;
}) {
  const byId = useMemo(() => new Map(graph.nodes.map((n) => [n.id, n])), [graph]);
  const node = selected ? byId.get(selected) : undefined;

  if (!node) {
    return (
      <p className="text-muted-foreground">
        Click a node, or find one by name, to see what it is joined to. Drag to move one; scroll to
        zoom.
      </p>
    );
  }

  const out = graph.edges.filter((e) => e.from === node.id);
  const into = graph.edges.filter((e) => e.to === node.id);
  const page = PAGE[node.label];

  const kind = node.hub ? "Hub" : kindOf(node);
  return (
    <div className="space-y-3 text-xs">
      {inSheet ? (
        <SheetHeader>
          <SheetTitle>{nameOf(node)}</SheetTitle>
          <SheetDescription>{node.description ?? kind}</SheetDescription>
        </SheetHeader>
      ) : (
        <div>
          <p className="text-muted-foreground">{kind}</p>
          <h2 className="text-sm font-semibold">{nameOf(node)}</h2>
          {node.description ? (
            <p className="mt-1 text-muted-foreground">{node.description}</p>
          ) : null}
        </div>
      )}
      {node.hub ? (
        <p className="font-medium text-primary">
          The team's hub: every message reaches it first, and it can hand work to any agent.
        </p>
      ) : null}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
        {node.kind ? (
          <>
            <dt className="text-muted-foreground">Kind</dt>
            <dd>{node.kind}</dd>
          </>
        ) : null}
        {node.id === BUILT_IN_HUB ? null : (
          <>
            <dt className="text-muted-foreground">Searchable by</dt>
            <dd>{node.model ? <Mono>{node.model}</Mono> : "not yet indexed"}</dd>
            <dt className="text-muted-foreground">Id</dt>
            <dd>
              <Mono className="break-all">{node.id}</Mono>
            </dd>
          </>
        )}
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
              {EDGE_WORDS_INTO[e.type]}{" "}
              <span className="font-medium text-foreground">{nameOf(byId.get(e.from))}</span>
            </li>
          ))}
        </ul>
      ) : null}
      {node.id === BUILT_IN_HUB ? (
        <Link
          to="/ui/settings"
          className="inline-block text-primary underline-offset-4 hover:underline"
        >
          Choose the hub in team settings
        </Link>
      ) : node.label === "Agent" ? (
        <Link
          to="/ui/agents/$agentId"
          params={{ agentId: node.id }}
          className="inline-block text-primary underline-offset-4 hover:underline"
        >
          Manage on its page
        </Link>
      ) : page ? (
        <Link to={page} className="inline-block text-primary underline-offset-4 hover:underline">
          Manage on its page
        </Link>
      ) : null}
    </div>
  );
}

/** The same graph as text: what a screen reader, or a crowded graph, needs. */
function GraphList({
  graph,
  hidden,
  onPick,
}: {
  graph: DrawnGraph;
  hidden: Set<GraphLabel>;
  onPick: (id: string) => void;
}) {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const shown = graph.edges.filter((e) => {
    const a = byId.get(e.from);
    const b = byId.get(e.to);
    return a && b && !hidden.has(a.label) && !hidden.has(b.label) && e.type !== "OWNS";
  });

  return (
    <div className="grid content-start gap-4 xl:grid-cols-2">
      <table className="w-full rounded-md border border-border bg-card text-left text-xs">
        <caption className="px-3 py-2 text-left font-medium">Nodes</caption>
        <tbody className="divide-y divide-border">
          {graph.nodes
            .filter((n) => !hidden.has(n.label))
            .map((n) => (
              <tr key={n.id}>
                <td className="px-3 py-1.5 text-muted-foreground">{n.hub ? "Hub" : kindOf(n)}</td>
                <td className="py-1.5 font-medium">
                  <button
                    type="button"
                    className="text-left hover:underline"
                    onClick={() => onPick(n.id)}
                  >
                    {nameOf(n)}
                  </button>
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
