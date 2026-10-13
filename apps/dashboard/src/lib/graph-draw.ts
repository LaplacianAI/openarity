/**
 * How the graph screen draws a node and an edge.
 *
 * The canvas component's own drawing fits each name inside its circle, so a
 * long name in a small circle shrinks to a few pixels, and it labels every
 * edge, so the picture is mostly words. This draws a plain shape with the
 * name beneath it at a fixed size on screen, whatever the zoom, labels only
 * the edges of the node in focus, and fades everything else while one is.
 */
import type { GraphLink, GraphNode } from "@falkordb/canvas";

export type Palette = {
  /** The canvas background, used as the halo behind text and around shapes. */
  surface: string;
  /** Text and a selected node's ring. */
  ink: string;
  /** Edges at rest. */
  edge: string;
  /** Edges of the node in focus. */
  edgeStrong: string;
  font: string;
};

export type DrawState = {
  palette: Palette;
  /** The canvas's current zoom, so text and lines keep their size on screen. */
  zoom: () => number;
  /** The uuid of the node in focus, and the uuids joined to it. */
  focus: string | null;
  near: Set<string>;
};

/** The words an edge is shown with, read from its source to its target. */
export const EDGE_LABEL: Record<string, string> = {
  OWNS: "owns",
  GRANTED: "granted",
  CONTAINS: "contains",
  HAS_SKILL: "has skill",
};

const LABEL_PX = 12;
const EDGE_LABEL_PX = 10.5;
const MAX_LABEL = 26;
const FADED = 0.14;

export function shortName(name: string): string {
  return name.length > MAX_LABEL ? `${name.slice(0, MAX_LABEL - 1)}…` : name;
}

function uuidOf(node: GraphNode | undefined): string {
  return String(node?.data?.uuid ?? "");
}

/** In focus, or nothing is: what is drawn at full strength. */
export function isLit(state: DrawState, uuid: string): boolean {
  return state.focus === null || uuid === state.focus || state.near.has(uuid);
}

function edgeTouchesFocus(state: DrawState, link: GraphLink): boolean {
  return (
    state.focus !== null &&
    (uuidOf(link.source) === state.focus || uuidOf(link.target) === state.focus)
  );
}

function shape(ctx: CanvasRenderingContext2D, node: GraphNode, r: number) {
  const x = node.x ?? 0;
  const y = node.y ?? 0;
  ctx.beginPath();
  if (node.shape === "square") {
    const s = r * 1.7;
    const corner = r * 0.35;
    ctx.roundRect(x - s / 2, y - s / 2, s, s, corner);
  } else {
    ctx.arc(x, y, r, 0, 2 * Math.PI);
  }
}

export function drawNode(state: DrawState, node: GraphNode, ctx: CanvasRenderingContext2D): void {
  const k = Math.max(state.zoom(), 0.05);
  const r = node.size;
  const uuid = uuidOf(node);
  const selected = uuid === state.focus;

  ctx.save();
  ctx.globalAlpha = isLit(state, uuid) ? 1 : FADED;

  // A ring in the surface colour separates a node from the edges under it.
  shape(ctx, node, r);
  ctx.fillStyle = node.color;
  ctx.fill();
  ctx.lineWidth = 2.5 / k;
  ctx.strokeStyle = state.palette.surface;
  ctx.stroke();

  if (selected) {
    shape(ctx, node, r + 4 / k);
    ctx.lineWidth = 2 / k;
    ctx.strokeStyle = state.palette.ink;
    ctx.stroke();
  }

  const name = shortName(String(node.data?.name ?? ""));
  if (name) {
    ctx.font = `${selected ? 600 : 500} ${LABEL_PX / k}px ${state.palette.font}`;
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    const y = (node.y ?? 0) + (node.shape === "square" ? r * 0.85 : r) + 5 / k;
    ctx.lineJoin = "round";
    ctx.lineWidth = 4 / k;
    ctx.strokeStyle = state.palette.surface;
    ctx.strokeText(name, node.x ?? 0, y);
    ctx.fillStyle = state.palette.ink;
    ctx.fillText(name, node.x ?? 0, y);
  }
  ctx.restore();
}

export function paintNodeArea(node: GraphNode, color: string, ctx: CanvasRenderingContext2D): void {
  ctx.beginPath();
  ctx.arc(node.x ?? 0, node.y ?? 0, node.size + 3, 0, 2 * Math.PI);
  ctx.fillStyle = color;
  ctx.fill();
}

/** Where a line from a node's centre towards another leaves its shape. */
function rim(from: GraphNode, to: GraphNode, gap: number) {
  const dx = (to.x ?? 0) - (from.x ?? 0);
  const dy = (to.y ?? 0) - (from.y ?? 0);
  const length = Math.hypot(dx, dy) || 1;
  const out = from.size + gap;
  return {
    x: (from.x ?? 0) + (dx / length) * out,
    y: (from.y ?? 0) + (dy / length) * out,
    ux: dx / length,
    uy: dy / length,
  };
}

export function drawLink(
  state: DrawState,
  link: GraphLink,
  ctx: CanvasRenderingContext2D,
  globalScale: number,
): void {
  const k = Math.max(globalScale, 0.05);
  const source = link.source;
  const target = link.target;
  if (!source || !target) return;

  const strong = edgeTouchesFocus(state, link);
  const lit = state.focus === null || strong;
  const start = rim(source, target, 2 / k);
  const end = rim(target, source, 4 / k);

  ctx.save();
  ctx.globalAlpha = lit ? 1 : FADED;
  ctx.strokeStyle = strong ? state.palette.edgeStrong : state.palette.edge;
  ctx.fillStyle = ctx.strokeStyle;
  ctx.lineWidth = (strong ? 2 : 1.25) / k;

  ctx.beginPath();
  ctx.moveTo(start.x, start.y);
  ctx.lineTo(end.x, end.y);
  ctx.stroke();

  // The arrowhead, pointing at the target's rim.
  const head = 7 / k;
  const wing = 3.5 / k;
  ctx.beginPath();
  ctx.moveTo(end.x, end.y);
  ctx.lineTo(end.x + end.ux * head - end.uy * wing, end.y + end.uy * head + end.ux * wing);
  ctx.lineTo(end.x + end.ux * head + end.uy * wing, end.y + end.uy * head - end.ux * wing);
  ctx.closePath();
  ctx.fill();

  if (strong) {
    const only = link.data?.only ? ` · ${link.data.only}` : "";
    const text = `${EDGE_LABEL[link.relationship] ?? link.relationship.toLowerCase()}${only}`;
    const mx = (start.x + end.x) / 2;
    const my = (start.y + end.y) / 2;
    ctx.font = `500 ${EDGE_LABEL_PX / k}px ${state.palette.font}`;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.lineJoin = "round";
    ctx.lineWidth = 4 / k;
    ctx.strokeStyle = state.palette.surface;
    ctx.strokeText(text, mx, my);
    ctx.fillStyle = state.palette.ink;
    ctx.fillText(text, mx, my);
  }
  ctx.restore();
}

export function paintLinkArea(link: GraphLink, color: string, ctx: CanvasRenderingContext2D): void {
  if (!link.source || !link.target) return;
  ctx.beginPath();
  ctx.moveTo(link.source.x ?? 0, link.source.y ?? 0);
  ctx.lineTo(link.target.x ?? 0, link.target.y ?? 0);
  ctx.lineWidth = 8;
  ctx.strokeStyle = color;
  ctx.stroke();
}
