import { describe, expect, it } from "vitest";

import type { TeamGraph } from "@/api/types";

import { BUILT_IN_HUB, withDelegation } from "./graph-delegation";

const graph: TeamGraph = {
  truncated: false,
  nodes: [
    { id: "team", label: "Team" },
    { id: "lead", label: "Agent", name: "ops-lead" },
    { id: "logs", label: "Agent", name: "log-reader" },
    { id: "notes", label: "Agent", name: "release-notes" },
    { id: "m1", label: "Toolkit", name: "grafana" },
  ],
  edges: [{ type: "GRANTED", from: "logs", to: "m1" }],
};

const added = (g: ReturnType<typeof withDelegation>) => g.edges.slice(graph.edges.length);

describe("withDelegation", () => {
  it("draws a listed delegate as an edge to that agent", () => {
    const g = withDelegation(
      graph,
      [{ id: "lead", delegation: "listed", delegates: ["logs", "notes"] }],
      undefined,
    );
    expect(added(g)).toEqual([
      { type: "HANDS_TO", from: "lead", to: "logs" },
      { type: "HANDS_TO", from: "lead", to: "notes" },
    ]);
  });

  // Fifty lines from one agent say less than one to the team.
  it("draws an agent that may hand work to any agent as one edge to the team", () => {
    const g = withDelegation(graph, [{ id: "lead", delegation: "team", delegates: [] }], undefined);
    expect(added(g)).toEqual([{ type: "HANDS_ANY", from: "lead", to: "team" }]);
  });

  it("draws nothing for an agent that hands no work on", () => {
    const g = withDelegation(graph, [{ id: "logs", delegation: "none", delegates: [] }], undefined);
    expect(added(g)).toEqual([]);
  });

  it("leaves out a delegate the graph does not hold, and an agent naming itself", () => {
    const g = withDelegation(
      graph,
      [
        { id: "lead", delegation: "listed", delegates: ["gone", "lead"] },
        { id: "absent", delegation: "team", delegates: [] },
      ],
      undefined,
    );
    expect(added(g)).toEqual([]);
  });

  it("marks the team's chosen hub, which answers for the whole team", () => {
    const g = withDelegation(graph, [], { id: "lead" });
    expect(g.nodes.find((n) => n.id === "lead")?.hub).toBe(true);
    expect(g.nodes.filter((n) => n.hub)).toHaveLength(1);
    expect(added(g)).toEqual([{ type: "ANSWERS", from: "lead", to: "team" }]);
  });

  it("draws the built-in orchestrator when the team has chosen no hub", () => {
    const g = withDelegation(graph, [], null);
    expect(g.nodes.at(-1)).toMatchObject({ id: BUILT_IN_HUB, label: "Agent", hub: true });
    expect(added(g)).toEqual([{ type: "ANSWERS", from: BUILT_IN_HUB, to: "team" }]);
  });

  // A setting that could not be read is not a team with the built-in hub.
  it("claims no hub when the setting is unknown", () => {
    const g = withDelegation(graph, [], undefined);
    expect(g.nodes.some((n) => n.hub)).toBe(false);
    expect(g.nodes).toHaveLength(graph.nodes.length);
  });

  it("leaves the index's own graph as it was", () => {
    const before = JSON.stringify(graph);
    withDelegation(graph, [{ id: "lead", delegation: "team", delegates: [] }], { id: "lead" });
    expect(JSON.stringify(graph)).toBe(before);
  });
});
