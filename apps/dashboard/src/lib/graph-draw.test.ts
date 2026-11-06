import type { GraphLink, GraphNode } from "@falkordb/canvas";
import { describe, expect, it } from "vitest";

import { type DrawState, drawLink, drawNode, isLit, shortName } from "./graph-draw";

/** A canvas context that records what was drawn, and at what opacity. */
function recorder() {
  const calls: {
    op: string;
    args: unknown[];
    alpha: number;
    font: string;
    fill: string;
    stroke: string;
  }[] = [];
  const ctx = {
    globalAlpha: 1,
    font: "",
    fillStyle: "",
    strokeStyle: "",
    lineWidth: 1,
    lineJoin: "",
    textAlign: "",
    textBaseline: "",
    save() {},
    restore() {
      this.globalAlpha = 1;
    },
  } as Record<string, unknown>;
  for (const op of [
    "beginPath",
    "arc",
    "roundRect",
    "fill",
    "stroke",
    "moveTo",
    "lineTo",
    "closePath",
    "fillText",
    "strokeText",
  ]) {
    ctx[op] = (...args: unknown[]) =>
      calls.push({
        op,
        args,
        alpha: ctx.globalAlpha as number,
        font: ctx.font as string,
        fill: ctx.fillStyle as string,
        stroke: ctx.strokeStyle as string,
      });
  }
  return { ctx: ctx as unknown as CanvasRenderingContext2D, calls };
}

function state(zoom = 1, focus: string | null = null, near: string[] = []): DrawState {
  return {
    palette: {
      surface: "#fff",
      ink: "#111",
      accent: "#0B6A5C",
      edge: "#ccc",
      edgeStrong: "#666",
      font: "Inter",
    },
    zoom: () => zoom,
    focus,
    near: new Set(near),
  };
}

function node(uuid: string, name: string, extra: Partial<GraphNode> = {}): GraphNode {
  return {
    id: 1,
    x: 0,
    y: 0,
    size: 10,
    color: "#2E3A3D",
    labels: ["Agent"],
    visible: true,
    data: { uuid, name },
    ...extra,
  } as unknown as GraphNode;
}

describe("a node", () => {
  it("is named beneath itself, at the same size on screen whatever the zoom", () => {
    for (const zoom of [0.5, 1, 4]) {
      const { ctx, calls } = recorder();
      drawNode(state(zoom), node("a", "triage"), ctx);
      const text = calls.find((c) => c.op === "fillText");
      expect(text?.args[0]).toBe("triage");
      expect(Number(text?.args[2])).toBeGreaterThan(10);
      expect(text?.font).toContain(`${12 / zoom}px`);
      expect(text?.font).toContain("Inter");
    }
  });

  it("cuts a long name, so a label never runs across the graph", () => {
    expect(shortName("a".repeat(40))).toHaveLength(26);
    expect(shortName("short")).toBe("short");
  });

  it("is a rounded square for a server and a circle otherwise", () => {
    const square = recorder();
    drawNode(state(), node("k", "github", { shape: "square" }), square.ctx);
    expect(square.calls.some((c) => c.op === "roundRect")).toBe(true);

    const circle = recorder();
    drawNode(state(), node("a", "triage"), circle.ctx);
    expect(circle.calls.some((c) => c.op === "roundRect")).toBe(false);
  });

  it("fades when another node is in focus and it is not joined to it", () => {
    const s = state(1, "a", ["b"]);
    expect(isLit(s, "a")).toBe(true);
    expect(isLit(s, "b")).toBe(true);
    expect(isLit(s, "c")).toBe(false);
    expect(isLit(state(), "c")).toBe(true);

    const { ctx, calls } = recorder();
    drawNode(s, node("c", "far"), ctx);
    expect(calls.find((c) => c.op === "fill")?.alpha).toBeLessThan(0.5);
  });

  it("wears the accent ring and says hub when it is the team's hub", () => {
    const { ctx, calls } = recorder();
    drawNode(
      state(),
      node("h", "ops-lead", { data: { uuid: "h", name: "ops-lead", hub: true } }),
      ctx,
    );
    expect(calls.some((c) => c.op === "stroke" && c.stroke === "#0B6A5C")).toBe(true);
    expect(calls.find((c) => c.op === "fillText")?.args[0]).toBe("ops-lead · hub");

    const plain = recorder();
    drawNode(state(), node("a", "triage"), plain.ctx);
    expect(plain.calls.some((c) => c.stroke === "#0B6A5C")).toBe(false);
  });

  it("is a ring in its colour around the surface when its kind is hollow", () => {
    const { ctx, calls } = recorder();
    drawNode(state(), node("s", "pdf", { data: { uuid: "s", name: "pdf", hollow: true } }), ctx);
    expect(calls.find((c) => c.op === "fill")?.fill).toBe("#fff");
    expect(calls.find((c) => c.op === "stroke")?.stroke).toBe("#2E3A3D");
  });

  it("is ringed when it is the one in focus", () => {
    const focused = recorder();
    drawNode(state(1, "a"), node("a", "triage"), focused.ctx);
    const plain = recorder();
    drawNode(state(), node("a", "triage"), plain.ctx);
    expect(focused.calls.filter((c) => c.op === "stroke").length).toBeGreaterThan(
      plain.calls.filter((c) => c.op === "stroke").length,
    );
  });
});

describe("an edge", () => {
  const link = (from: GraphNode, to: GraphNode, extra: Partial<GraphLink> = {}) =>
    ({
      id: 1,
      relationship: "GRANTED",
      source: from,
      target: to,
      visible: true,
      color: "#ccc",
      curve: 0,
      data: { only: "search" },
      ...extra,
    }) as unknown as GraphLink;

  it("carries no words until one of its ends is in focus", () => {
    const a = node("a", "triage");
    const b = node("b", "github", { x: 100 });

    const rest = recorder();
    drawLink(state(), link(a, b), rest.ctx, 1);
    expect(rest.calls.some((c) => c.op === "fillText")).toBe(false);

    const focused = recorder();
    drawLink(state(1, "a", ["b"]), link(a, b), focused.ctx, 1);
    expect(focused.calls.find((c) => c.op === "fillText")?.args[0]).toBe("granted · search");
  });

  // Who hands work to whom is the graph's point, so it is never quiet.
  it("is drawn in the accent when it hands work on, and quiet otherwise", () => {
    const a = node("a", "lead");
    const b = node("b", "logs", { x: 100 });
    const hands = recorder();
    drawLink(
      state(),
      link(a, b, { relationship: "HANDS_TO", data: { delegation: true } }),
      hands.ctx,
      1,
    );
    expect(hands.calls.find((c) => c.op === "stroke")?.stroke).toBe("#0B6A5C");

    const granted = recorder();
    drawLink(state(), link(a, b), granted.ctx, 1);
    expect(granted.calls.find((c) => c.op === "stroke")?.stroke).toBe("#ccc");
  });

  it("names a delegation in plain words when in focus", () => {
    const a = node("a", "lead");
    const b = node("b", "logs", { x: 100 });
    const { ctx, calls } = recorder();
    drawLink(
      state(1, "a", ["b"]),
      link(a, b, { relationship: "HANDS_TO", data: { delegation: true } }),
      ctx,
      1,
    );
    expect(calls.find((c) => c.op === "fillText")?.args[0]).toBe("hands work to");
  });

  it("fades when neither end is in focus", () => {
    const { ctx, calls } = recorder();
    drawLink(state(1, "z"), link(node("a", "x"), node("b", "y", { x: 100 })), ctx, 1);
    expect(calls.find((c) => c.op === "stroke")?.alpha).toBeLessThan(0.5);
  });

  it("starts and ends at the shapes' rims, not their centres", () => {
    const { ctx, calls } = recorder();
    drawLink(state(), link(node("a", "x"), node("b", "y", { x: 100 })), ctx, 1);
    const start = calls.find((c) => c.op === "moveTo")?.args as number[];
    const end = calls.find((c) => c.op === "lineTo")?.args as number[];
    expect(start[0]).toBeGreaterThan(10);
    expect(end[0]).toBeLessThan(90);
  });
});
