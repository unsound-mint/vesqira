import type { GraphEdge, GraphNode } from "../api/reports";

export type Point = { x: number; y: number };

/** Above this many nodes the O(n²) simulation is skipped for a plain circle. */
export const CIRCLE_ABOVE = 400;
const ITERATIONS = 300;
const MARGIN = 0.04;

function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function seedOf(nodes: GraphNode[]): number {
  let h = 2166136261;
  for (const n of nodes) for (let i = 0; i < n.id.length; i++) h = Math.imul(h ^ n.id.charCodeAt(i), 16777619);
  return h >>> 0;
}

/**
 * Deterministic force-directed layout: seeded start, pairwise repulsion, spring edges, gentle centering.
 * Returns positions in the unit box [0,1]²; the same input always gives the same output.
 */
export function layoutGraph(nodes: GraphNode[], edges: GraphEdge[]): Record<string, Point> {
  const n = nodes.length;
  const out: Record<string, Point> = {};
  if (n === 0) return out;
  if (n === 1) return { [nodes[0]!.id]: { x: 0.5, y: 0.5 } };

  const circle = (i: number): Point => {
    const a = (2 * Math.PI * i) / n;
    return { x: 0.5 + 0.5 * Math.cos(a), y: 0.5 + 0.5 * Math.sin(a) };
  };
  if (n > CIRCLE_ABOVE) {
    nodes.forEach((node, i) => (out[node.id] = circle(i)));
    return out;
  }

  const index = new Map(nodes.map((node, i) => [node.id, i]));
  const links: [number, number][] = [];
  for (const e of edges) {
    const a = index.get(e.from);
    const b = index.get(e.to);
    if (a !== undefined && b !== undefined && a !== b) links.push([a, b]);
  }

  const rand = rng(seedOf(nodes));
  const px = new Float64Array(n);
  const py = new Float64Array(n);
  for (let i = 0; i < n; i++) {
    const a = (2 * Math.PI * i) / n + rand() * 0.5;
    const r = 0.25 + rand() * 0.15;
    px[i]! = Math.cos(a) * r;
    py[i]! = Math.sin(a) * r;
  }
  const k = Math.sqrt(1 / n) * 0.9; // ideal edge length
  const fx = new Float64Array(n);
  const fy = new Float64Array(n);
  for (let it = 0; it < ITERATIONS; it++) {
    const cool = 0.1 * (1 - it / ITERATIONS) + 0.002;
    fx.fill(0);
    fy.fill(0);
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        let dx = px[i]! - px[j]!;
        let dy = py[i]! - py[j]!;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1e-9) {
          dx = rand() - 0.5;
          dy = rand() - 0.5;
          d2 = dx * dx + dy * dy + 1e-9;
        }
        const d = Math.sqrt(d2);
        const f = (k * k) / d2; // repulsion ~ 1/d
        fx[i]! += (dx / d) * f * d;
        fy[i]! += (dy / d) * f * d;
        fx[j]! -= (dx / d) * f * d;
        fy[j]! -= (dy / d) * f * d;
      }
    }
    for (const [a, b] of links) {
      const dx = px[b]! - px[a]!;
      const dy = py[b]! - py[a]!;
      const d = Math.sqrt(dx * dx + dy * dy) || 1e-9;
      const f = (d - k) * 0.6; // spring
      fx[a]! += (dx / d) * f;
      fy[a]! += (dy / d) * f;
      fx[b]! -= (dx / d) * f;
      fy[b]! -= (dy / d) * f;
    }
    for (let i = 0; i < n; i++) {
      fx[i]! -= px[i]! * 0.15; // gentle centering keeps disconnected parts in frame
      fy[i]! -= py[i]! * 0.15;
      const m = Math.sqrt(fx[i]! * fx[i]! + fy[i]! * fy[i]!) || 1e-9;
      const step = Math.min(m, cool);
      px[i]! += (fx[i]! / m) * step;
      py[i]! += (fy[i]! / m) * step;
    }
  }

  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  for (let i = 0; i < n; i++) {
    minX = Math.min(minX, px[i]!); maxX = Math.max(maxX, px[i]!);
    minY = Math.min(minY, py[i]!); maxY = Math.max(maxY, py[i]!);
  }
  const span = 1 - 2 * MARGIN;
  const norm = (v: number, lo: number, hi: number) => (hi - lo < 1e-9 ? 0.5 : MARGIN + ((v - lo) / (hi - lo)) * span);
  nodes.forEach((node, i) => (out[node.id] = { x: norm(px[i]!, minX, maxX), y: norm(py[i]!, minY, maxY) }));
  return out;
}

/** The id of the nearest node strictly in a direction from `from`, or null. Coordinates are any consistent 2D space. */
export function nearestInDirection(from: string, pos: Record<string, Point>, dir: "left" | "right" | "up" | "down"): string | null {
  const p = pos[from];
  if (!p) return null;
  let best: string | null = null;
  let bestScore = Infinity;
  for (const [id, q] of Object.entries(pos)) {
    if (id === from) continue;
    const dx = q.x - p.x;
    const dy = q.y - p.y;
    const along = dir === "right" ? dx : dir === "left" ? -dx : dir === "down" ? dy : -dy;
    const across = dir === "left" || dir === "right" ? Math.abs(dy) : Math.abs(dx);
    if (along <= 1e-9 || across > along * 2) continue; // within a 90° cone
    const score = along + across * 2;
    if (score < bestScore) {
      bestScore = score;
      best = id;
    }
  }
  return best;
}
