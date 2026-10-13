import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamGraph } from "@/api/types";
import { brain, pageOf, show, TEAM, team } from "@/test/brain";

// jsdom has no canvas to draw on, so the component is replaced by an element
// that records what it was handed. What the page gives it is the contract.
const handed = vi.hoisted(() => ({ data: [] as unknown[], config: [] as unknown[] }));
vi.mock("@falkordb/canvas", () => {
  class FakeCanvas extends HTMLElement {
    setData(d: unknown) {
      handed.data.push(d);
    }
    setConfig(c: unknown) {
      handed.config.push(c);
    }
  }
  if (!customElements.get("falkordb-canvas")) customElements.define("falkordb-canvas", FakeCanvas);
  return { default: FakeCanvas, FalkorDBCanvas: FakeCanvas };
});

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}));

const { Route, toCanvas } = await import("@/routes/ui.graph");
const Graph = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
  handed.data.length = 0;
  handed.config.length = 0;
});

const graph: TeamGraph = {
  truncated: false,
  nodes: [
    { id: TEAM, label: "Team" },
    {
      id: "a1",
      label: "Agent",
      name: "triage",
      description: "Sorts issues",
      kind: "custom",
      model: "nomic-embed-text",
    },
    { id: "k1", label: "Toolkit", name: "github", kind: "mcp" },
    { id: "t1", label: "Tool", name: "search_code", model: "nomic-embed-text" },
    { id: "s1", label: "Skill", name: "pdf", model: "nomic-embed-text" },
  ],
  edges: [
    { type: "OWNS", from: TEAM, to: "a1" },
    { type: "OWNS", from: TEAM, to: "k1" },
    { type: "CONTAINS", from: "k1", to: "t1" },
    { type: "GRANTED", from: "a1", to: "k1", only: ["search_code"] },
    { type: "HAS_SKILL", from: "a1", to: "s1" },
  ],
};

describe("what the canvas is handed", () => {
  it("numbers every node, keeps its uuid and its name, and colours it by kind", () => {
    const data = toCanvas(graph, new Set(), "light");

    expect(data.nodes.map((n) => n.id)).toEqual([1, 2, 3, 4, 5]);
    const agent = data.nodes[1];
    expect(agent?.labels).toEqual(["Agent"]);
    expect(agent?.data).toMatchObject({ uuid: "a1", name: "triage", model: "nomic-embed-text" });
    expect(agent?.color).toBe("#2a78d6");
    // A server and its tools share a hue and differ in shape and size.
    expect(data.nodes[2]?.color).toBe(data.nodes[3]?.color);
    expect(data.nodes[2]?.shape).toBe("square");
    expect(data.nodes[3]?.shape).toBe("circle");
  });

  it("steps every colour for the dark surface", () => {
    const light = toCanvas(graph, new Set(), "light").nodes.map((n) => n.color);
    const dark = toCanvas(graph, new Set(), "dark").nodes.map((n) => n.color);
    for (const [i, c] of light.entries()) expect(dark[i]).not.toBe(c);
  });

  it("joins edges by the numbers it gave the nodes, carrying a grant's tools", () => {
    const data = toCanvas(graph, new Set(), "light");
    const granted = data.links.find((l) => l.relationship === "GRANTED");
    expect(granted).toMatchObject({ source: 2, target: 3, data: { only: "search_code" } });
  });

  // A truncated graph can return an edge whose end it did not return. It is
  // dropped rather than drawn to a node that is not there.
  it("drops an edge whose end is missing", () => {
    const data = toCanvas(
      { ...graph, edges: [...graph.edges, { type: "HAS_SKILL", from: "a1", to: "elsewhere" }] },
      new Set(),
      "light",
    );
    expect(data.links).toHaveLength(graph.edges.length);
  });

  it("hides a kind and every edge touching it", () => {
    const data = toCanvas(graph, new Set(["Tool"]), "light");
    expect(data.nodes.find((n) => n.labels[0] === "Tool")?.visible).toBe(false);
    expect(data.links.find((l) => l.relationship === "CONTAINS")?.visible).toBe(false);
    expect(data.links.find((l) => l.relationship === "GRANTED")?.visible).toBe(true);
  });

  it("names a node that has no name", () => {
    const data = toCanvas(graph, new Set(), "light");
    expect(data.nodes[0]?.data.name).toBe("team");
  });
});

it("draws the team's graph and counts each kind", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  expect(await screen.findByRole("button", { name: /agents\s*1/i })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /mcp servers\s*1/i })).toBeInTheDocument();
  await waitFor(() => expect(handed.data.length).toBeGreaterThan(0));
  const last = handed.data.at(-1) as { nodes: unknown[] };
  expect(last.nodes).toHaveLength(5);
});

it("asks for a bounded graph", async () => {
  const seen = brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await screen.findByRole("button", { name: /agents/i });
  expect(seen.find((s) => s.path.includes("/graph"))?.path).toBe(`/teams/${TEAM}/graph?limit=500`);
});

// A 503 is a brain with no embedding model, not a fault. It says what to set.
it("explains a graph that is not enabled", async () => {
  brain({
    ...team([]),
    "GET /teams/{id}/graph": { status: 503, text: "the graph is not enabled on this brain" },
  });
  show(Graph);

  expect(
    await screen.findByRole("heading", { name: /the graph is not enabled/i }),
  ).toBeInTheDocument();
  expect(screen.getByText(/OPENARITY_EMBEDDING_MODEL/)).toBeInTheDocument();
});

it("says so when the graph holds only the team", async () => {
  brain({
    ...team([]),
    "GET /teams/{id}/graph": {
      body: { nodes: [{ id: TEAM, label: "Team" }], edges: [], truncated: false },
    },
  });
  show(Graph);

  expect(await screen.findByText(/nothing in the graph yet/i)).toBeInTheDocument();
});

it("says when the graph was cut short", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: { ...graph, truncated: true } } });
  show(Graph);

  expect(await screen.findByText(/the team has more/i)).toBeInTheDocument();
});

it("hides a kind when its legend entry is pressed", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  const tools = await screen.findByRole("button", { name: /tools\s*1/i });
  expect(tools).toHaveAttribute("aria-pressed", "true");
  await userEvent.click(tools);
  expect(tools).toHaveAttribute("aria-pressed", "false");

  await waitFor(() => {
    const last = handed.data.at(-1) as { nodes: { labels: string[]; visible: boolean }[] };
    expect(last.nodes.find((n) => n.labels[0] === "Tool")?.visible).toBe(false);
  });
});

// The same graph as text: every relationship but ownership, by name.
it("shows the graph as a list", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await userEvent.click(await screen.findByRole("button", { name: /show as list/i }));
  expect(screen.getByRole("table", { name: /relationships/i })).toHaveTextContent(
    "triageis grantedgithub (search_code)",
  );
  expect(screen.getByRole("table", { name: /relationships/i })).not.toHaveTextContent("owns");
});

// Clicking a node in the canvas reports its uuid; the panel then names
// everything it is joined to.
it("describes the node clicked in the canvas", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await waitFor(() => expect(handed.config.length).toBeGreaterThan(0));
  const config = handed.config.at(-1) as { eventHandlers: { onNodeClick: (n: unknown) => void } };
  config.eventHandlers.onNodeClick({ data: { uuid: "a1" } });

  expect(await screen.findByRole("heading", { name: "triage" })).toBeInTheDocument();
  expect(screen.getByText("Sorts issues")).toBeInTheDocument();
  expect(screen.getByText(/only search_code/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /manage on its page/i })).toHaveAttribute(
    "href",
    "/ui/agents",
  );
});
