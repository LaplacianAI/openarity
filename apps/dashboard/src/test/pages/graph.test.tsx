import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamGraph } from "@/api/types";
import { brain, pageOf, show, TEAM, team } from "@/test/brain";

// jsdom has no canvas to draw on, so the component is replaced by an element
// that records what it was handed. What the page gives it is the contract.
const handed = vi.hoisted(() => ({
  data: [] as unknown[],
  config: [] as unknown[],
  nodeLabel: null as null | ((n: unknown) => unknown),
  linkLabel: null as null | ((l: unknown) => unknown),
  refreshed: 0,
  layouts: [] as string[],
  fitted: 0,
}));
vi.mock("@falkordb/canvas", () => {
  class FakeCanvas extends HTMLElement {
    setData(d: unknown) {
      handed.data.push(d);
    }
    setConfig(c: unknown) {
      handed.config.push(c);
    }
    refresh() {
      handed.refreshed += 1;
    }
    getZoom() {
      return 1;
    }
    setLayout(mode: string) {
      handed.layouts.push(mode);
    }
    zoomToFit() {
      handed.fitted += 1;
    }
    getGraph() {
      return {
        nodeLabel: (fn: (n: unknown) => unknown) => {
          handed.nodeLabel = fn;
        },
        linkLabel: (fn: (l: unknown) => unknown) => {
          handed.linkLabel = fn;
        },
      };
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
  handed.nodeLabel = null;
  handed.refreshed = 0;
  handed.linkLabel = null;
  handed.layouts.length = 0;
  handed.fitted = 0;
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

  // Left out, not drawn invisible: the tree and radial layouts place every
  // node they are given, so a hidden team would still root the tree.
  it("leaves out a hidden kind and every edge touching it, keeping the others' numbers", () => {
    const all = toCanvas(graph, new Set(), "light");
    const data = toCanvas(graph, new Set(["Tool"]), "light");
    expect(data.nodes.find((n) => n.labels[0] === "Tool")).toBeUndefined();
    expect(data.links.find((l) => l.relationship === "CONTAINS")).toBeUndefined();
    expect(data.links.find((l) => l.relationship === "GRANTED")).toMatchObject({
      source: 2,
      target: 3,
    });
    const skill = (d: typeof data) => d.nodes.find((n) => n.labels[0] === "Skill")?.id;
    expect(skill(data)).toBe(skill(all));
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
  // The team starts hidden.
  const last = handed.data.at(-1) as { nodes: unknown[] };
  expect(last.nodes).toHaveLength(4);
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
    expect(last.nodes.find((n) => n.labels[0] === "Tool")).toBeUndefined();
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

// A name is written by a team member, and a tool's name by an MCP server. The
// canvas's own tooltip would parse it as HTML, in the page that holds the
// session's token. The labels the page installs are text.
it("puts a node's name in its tooltip as text, never as markup", async () => {
  const evil = '<img src=x onerror="alert(1)">';
  brain({
    ...team([]),
    "GET /teams/{id}/graph": {
      body: { ...graph, nodes: graph.nodes.map((n) => (n.id === "a1" ? { ...n, name: evil } : n)) },
    },
  });
  show(Graph);

  await waitFor(() => expect(handed.nodeLabel).not.toBeNull());
  const label = handed.nodeLabel?.({ data: { name: evil } }) as HTMLElement;
  expect(label).toBeInstanceOf(HTMLElement);
  expect(label.textContent).toBe(evil);
  expect(label.children).toHaveLength(0);
  expect(label.innerHTML).not.toContain("<img");

  const link = handed.linkLabel?.({ relationship: "<b>GRANTED</b>" }) as HTMLElement;
  expect(link.textContent).toBe("<b>GRANTED</b>");
  expect(link.children).toHaveLength(0);
});

// The team owns everything; shown, its spokes bury the edges that matter.
it("starts with the team hidden, and shows it on request", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  const chip = await screen.findByRole("button", { name: /team\s*1/i });
  expect(chip).toHaveAttribute("aria-pressed", "false");
  await waitFor(() => {
    const last = handed.data.at(-1) as { nodes: { labels: string[]; visible: boolean }[] };
    expect(last.nodes.find((n) => n.labels[0] === "Team")).toBeUndefined();
  });

  await userEvent.click(chip);
  await waitFor(() => {
    const last = handed.data.at(-1) as { nodes: { labels: string[]; visible: boolean }[] };
    expect(last.nodes.find((n) => n.labels[0] === "Team")).toBeDefined();
  });
});

it("draws nodes and edges itself, and repaints when one is selected", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await waitFor(() => expect(handed.config.length).toBeGreaterThan(0));
  const config = handed.config.at(-1) as {
    node: { nodeCanvasObject: unknown };
    link: { linkCanvasObject: unknown };
    eventHandlers: { onNodeClick: (n: unknown) => void };
  };
  expect(typeof config.node.nodeCanvasObject).toBe("function");
  expect(typeof config.link.linkCanvasObject).toBe("function");

  const before = handed.refreshed;
  config.eventHandlers.onNodeClick({ data: { uuid: "a1" } });
  await waitFor(() => expect(handed.refreshed).toBeGreaterThan(before));
});

it("says what points at a node in words that read from it", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await waitFor(() => expect(handed.config.length).toBeGreaterThan(0));
  const config = handed.config.at(-1) as { eventHandlers: { onNodeClick: (n: unknown) => void } };
  config.eventHandlers.onNodeClick({ data: { uuid: "s1" } });

  expect(await screen.findByText(/a skill of/)).toBeInTheDocument();
  expect(screen.queryByText(/has the skill it/)).not.toBeInTheDocument();
});

// setConfig only records a layout; nothing moves until setLayout is called.
it("lays the graph out again when another layout is chosen", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await waitFor(() => expect(handed.data.length).toBeGreaterThan(0));
  expect(handed.layouts).toEqual([]);

  await userEvent.click(screen.getByRole("radio", { name: "tree" }));
  await waitFor(() => expect(handed.layouts).toEqual(["tree"]));
  await userEvent.click(screen.getByRole("radio", { name: "radial" }));
  await waitFor(() => expect(handed.layouts).toEqual(["tree", "radial"]));
});

// Fitted once the forces settle, and once only: a drag reheats the
// simulation, and refitting after it would move the view under the pointer.
it("fits the view when the layout settles, and not after every drag", async () => {
  brain({ ...team([]), "GET /teams/{id}/graph": { body: graph } });
  show(Graph);

  await waitFor(() => expect(handed.data.length).toBeGreaterThan(0));
  const config = handed.config.at(-1) as { eventHandlers: { onEngineStop: () => void } };
  config.eventHandlers.onEngineStop();
  config.eventHandlers.onEngineStop();
  expect(handed.fitted).toBe(1);

  await userEvent.click(screen.getByRole("radio", { name: "tree" }));
  await waitFor(() => expect(handed.layouts).toEqual(["tree"]));
  (handed.config.at(-1) as typeof config).eventHandlers.onEngineStop();
  expect(handed.fitted).toBe(2);
});
