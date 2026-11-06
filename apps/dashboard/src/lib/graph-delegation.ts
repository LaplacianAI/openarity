import type { Delegation, GraphEdge, GraphNode, TeamGraph } from "@/api/types";

/**
 * The graph index holds what a team owns and grants, not who hands work to
 * whom. That lives on each agent, so the screen adds it here: the claim the
 * graph is for is a hub that delegates, and a graph without it says less
 * than the Agents table does.
 */
export type DrawnEdgeType = GraphEdge["type"] | "HANDS_TO" | "HANDS_ANY" | "ANSWERS";
export type DrawnEdge = Omit<GraphEdge, "type"> & { type: DrawnEdgeType };
export type DrawnNode = GraphNode & { hub?: boolean };
export type DrawnGraph = { nodes: DrawnNode[]; edges: DrawnEdge[]; truncated: boolean };

/** The built-in orchestrator has no agent row, so it is drawn under an id of its own. */
export const BUILT_IN_HUB = "hub:built-in";

/** What the graph needs of an agent to draw whom it hands work to. */
export type Delegator = { id: string; delegation: Delegation; delegates: string[] };

/** Which edges say who hands work on, as against what is owned or granted. */
export function isDelegation(type: DrawnEdgeType): boolean {
  return type === "HANDS_TO" || type === "HANDS_ANY" || type === "ANSWERS";
}

/**
 * The index's graph with the hub and delegation added. An agent that may hand
 * work to any agent points at the team, not at each agent, so a team of fifty
 * is not drawn as fifty lines from every such agent. The hub answers for the
 * whole team; `hub` undefined means the setting could not be read, and then
 * no hub is claimed. An edge to a node the graph does not hold is left out.
 */
export function withDelegation(
  graph: TeamGraph,
  agents: Delegator[],
  hub: { id: string } | null | undefined,
): DrawnGraph {
  const ids = new Set(graph.nodes.map((n) => n.id));
  const team = graph.nodes.find((n) => n.label === "Team")?.id;
  const nodes: DrawnNode[] = graph.nodes.map((n) =>
    hub && n.id === hub.id ? { ...n, hub: true } : n,
  );
  const edges: DrawnEdge[] = [...graph.edges];

  for (const a of agents) {
    if (!ids.has(a.id)) continue;
    if (a.delegation === "team" && team) edges.push({ type: "HANDS_ANY", from: a.id, to: team });
    if (a.delegation === "listed") {
      for (const d of a.delegates) {
        if (ids.has(d) && d !== a.id) edges.push({ type: "HANDS_TO", from: a.id, to: d });
      }
    }
  }

  if (hub === null) {
    nodes.push({
      id: BUILT_IN_HUB,
      label: "Agent",
      name: "orchestrator",
      kind: "built-in hub",
      hub: true,
    });
    if (team) edges.push({ type: "ANSWERS", from: BUILT_IN_HUB, to: team });
  } else if (hub && ids.has(hub.id) && team) {
    edges.push({ type: "ANSWERS", from: hub.id, to: team });
  }

  return { nodes, edges, truncated: graph.truncated };
}
