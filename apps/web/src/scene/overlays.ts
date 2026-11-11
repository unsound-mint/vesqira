import type { Plane } from "../api/planes";
import type { Dataset } from "../api/catalog";
import { glyphStride, orientationLines, MIN_OPACITY } from "../viewer/glyphs";
import { outline } from "../viewer/region";
import { readyGeometry, toPicture } from "./geometry";
import type { GeometryMap } from "./geometry";
import { DEFAULT_WIDTH, OVERLAY_COLORS } from "./palette";
import type { OverlayColor } from "./palette";
import type { OverlayKind, OverlayOf, Scene, SceneOverlay, SceneRegion } from "./scene";

/**
 * What an overlay draws, as plain marks in picture coordinates (image pixels, x right, y down from the top edge, so a plane
 * row y is at height-1-y). Overlay code never touches a canvas or the DOM: the viewer turns marks into SVG elements and a
 * figure strokes them on its canvas, so the screen and a saved figure draw the same layers by the same code (and an SVG
 * export can later write the same marks).
 *
 * Overlays from another dataset (contours, orientation lines, a beam) and the coordinate grid are drawn from geometry the
 * worker placed on the base by WCS (ctx.geometry); without it they draw nothing, and the overlay list says why.
 */
export type MarkRole = "orientation" | "region" | "saved" | "draft";
/** A stroke an overlay chose: a palette colour, a width in output pixels, dashed or not (so layers differ without colour). */
export type Stroke = { color: OverlayColor; width: number; dashed: boolean };
export type Mark =
  | { type: "line"; role: "orientation"; x1: number; y1: number; x2: number; y2: number; opacity: number; stroke?: Stroke }
  | { type: "rect"; role: Exclude<MarkRole, "orientation">; x: number; y: number; w: number; h: number }
  | { type: "circle"; role: Exclude<MarkRole, "orientation">; cx: number; cy: number; r: number }
  /** A polyline (closed: a polygon) as x0, y0, x1, y1, ...; fill is an opacity for a filled polygon. */
  | { type: "path"; role: "contour" | "grid" | "beam"; points: number[]; closed: boolean; stroke: Stroke; fill?: number }
  /** A label at a picture position; size and offset are in output pixels so text does not scale with zoom. */
  | { type: "text"; role: "grid" | "beam"; x: number; y: number; text: string; color: OverlayColor; anchor: "start" | "middle" | "end"; baseline: "top" | "middle" | "bottom" };

/** What drawing needs besides the scene: the base plane as read, and facts the catalog declares about it. */
export type DrawContext = {
  plane: Plane;
  /** Output pixels per image pixel: the screen scale in the viewer, the figure's scale in a figure. */
  scale: number;
  interp: Dataset["interpretation"];
  reliability: Plane | null;
  /** The region being dragged out; a pointer gesture in progress, never part of the scene. */
  draft?: SceneRegion | null;
  /** Geometry the worker placed on the base, by overlay id (useOverlayGeometry). */
  geometry?: GeometryMap;
};

/** The reliability map that can set line opacity: only one on the plane's own grid (figureScene records the same choice). */
export const reliabilityFor = (plane: Plane, reliability: Plane | null): Plane | null =>
  reliability && reliability.head.width === plane.head.width && reliability.head.height === plane.head.height ? reliability : null;

type Styled = { color?: OverlayColor; width?: number; dash?: "solid" | "dashed" };
export function strokeOf(o: SceneOverlay, kind: keyof typeof DEFAULT_WIDTH): Stroke {
  const s = o as Styled;
  return { color: s.color ?? "white", width: s.width ?? DEFAULT_WIDTH[kind], dashed: s.dash === "dashed" };
}

type Renderer<K extends OverlayKind> = (overlay: OverlayOf<K>, scene: Scene, ctx: DrawContext) => Mark[];

/** Orientation lines: of the base map itself from the plane on screen, or of another map from the worker's samples. */
const orientation: Renderer<"orientation"> = (o, scene, ctx) => (o.datasetId === scene.base.datasetId ? ownLines(o, ctx) : otherLines(o, scene, ctx));

function ownLines(o: OverlayOf<"orientation">, { plane, interp, reliability, scale }: DrawContext): Mark[] {
  if (!interp || scale <= 0) return [];
  const { width: w, height: h } = plane.head;
  const needsRel = o.opacity === "reliability" || o.length === "reliability" || o.threshold !== undefined;
  const rel = needsRel ? reliabilityFor(plane, reliability) : null;
  const stride = o.sampling.mode === "step" ? o.sampling.step : glyphStride(scale, o.sampling.gapPx);
  const styled = (o as Styled).color !== undefined || (o as Styled).width !== undefined || (o as Styled).dash !== undefined;
  const stroke = styled ? strokeOf(o, "orientation") : undefined;
  return orientationLines(plane.data, w, h, interp.unit === "deg" ? 1 : 180 / Math.PI, {
    stride,
    reliability: rel?.data ?? null,
    relScale: rel ? (rel.head.stats.p99 ?? undefined) : undefined,
    opacityByReliability: o.opacity === "reliability",
    lengthByReliability: o.length === "reliability",
    threshold: o.threshold,
  }).map((g) => ({ type: "line", role: "orientation", ...g, ...(stroke ? { stroke } : {}) }));
}

/** Samples of another dataset's orientation map, placed and turned into the base's pixel frame by the worker. */
function otherLines(o: OverlayOf<"orientation">, _scene: Scene, { plane, geometry }: DrawContext): Mark[] {
  const g = readyGeometry(geometry, o.id, "orientation");
  if (!g) return [];
  const h = plane.head.height;
  const spacing = g.head.spacing_px ?? 1;
  const stroke = strokeOf(o, "orientation");
  const out: Mark[] = [];
  const s = g.segments;
  for (let i = 0; i + 3 < s.length; i += 4) {
    const w = s[i + 3]!;
    const known = Number.isFinite(w);
    const half = 0.4 * spacing * (o.length === "reliability" ? (known ? Math.max(MIN_OPACITY, Math.min(1, w)) : MIN_OPACITY) : 1);
    const t = (s[i + 2]! * Math.PI) / 180;
    const [cx, cy] = toPicture(s[i]!, s[i + 1]!, h);
    const dx = Math.cos(t) * half;
    const dy = -Math.sin(t) * half; // picture y points down
    const opacity = o.opacity === "reliability" && g.head.reliability ? (known ? Math.max(MIN_OPACITY, Math.min(1, w)) : MIN_OPACITY) : 1;
    out.push({ type: "line", role: "orientation", x1: cx - dx, y1: cy - dy, x2: cx + dx, y2: cy + dy, opacity, stroke });
  }
  return out;
}

function polylines(paths: { group: number; xy: Float32Array }[], height: number, role: "contour" | "grid" | "beam", stroke: (group: number) => Stroke, closed = false, fill?: number): Mark[] {
  return paths.map((p) => {
    const points: number[] = new Array(p.xy.length);
    for (let i = 0; i + 1 < p.xy.length; i += 2) {
      const [x, y] = toPicture(p.xy[i]!, p.xy[i + 1]!, height);
      points[i] = x;
      points[i + 1] = y;
    }
    return { type: "path", role, points, closed, stroke: stroke(p.group), ...(fill !== undefined ? { fill } : {}) };
  });
}

/** Contours traced on the overlay's own grid; negative levels are dashed, as is usual, so their sign does not rest on colour. */
const contours: Renderer<"contours"> = (o, _scene, { plane, geometry }) => {
  const g = readyGeometry(geometry, o.id, "contours");
  if (!g) return [];
  const base = strokeOf(o, "contours");
  const levels = g.head.levels ?? [];
  return polylines(g.paths, plane.head.height, "contour", (i) => ({ ...base, dashed: base.dashed || (levels[i] ?? 0) < 0 }));
};

/** The beam ellipse (built on the sky by the worker) with its size beside it. */
const beam: Renderer<"beam"> = (o, _scene, { plane, geometry }) => {
  const g = readyGeometry(geometry, o.id, "beam");
  if (!g || !g.head.beam) return [];
  const h = plane.head.height;
  const stroke = strokeOf(o, "beam");
  const out = polylines(g.paths, h, "beam", () => stroke, true, 0.35);
  const pts = g.paths[0]?.xy;
  if (pts && pts.length >= 2) {
    let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
    for (let i = 0; i + 1 < pts.length; i += 2) {
      const [x, y] = toPicture(pts[i]!, pts[i + 1]!, h);
      x0 = Math.min(x0, x); x1 = Math.max(x1, x); y0 = Math.min(y0, y); y1 = Math.max(y1, y);
    }
    const top = ((g.head as { corner?: string }).corner ?? "bottom-left").startsWith("top");
    out.push({ type: "text", role: "beam", x: (x0 + x1) / 2, y: top ? y1 : y0, text: beamText(g.head.beam), color: stroke.color, anchor: "middle", baseline: top ? "top" : "bottom" });
  }
  return out;
};

export function beamText(b: { bmaj_arcsec?: number; bmin_arcsec?: number; bpa_deg?: number | null }): string {
  const a = (v: number | undefined) => (v === undefined ? "?" : v >= 120 ? `${Number((v / 60).toPrecision(3))}′` : `${Number(v.toPrecision(3))}″`);
  return `${a(b.bmaj_arcsec)} × ${a(b.bmin_arcsec)}${b.bpa_deg !== null && b.bpa_deg !== undefined ? `, PA ${Number(b.bpa_deg.toPrecision(4))}°` : ""}`;
}

/** Lines of constant longitude and latitude with their labels at the bottom and left edges. */
const grid: Renderer<"grid"> = (o, _scene, { plane, geometry }) => {
  const g = readyGeometry(geometry, o.id, "grid");
  if (!g) return [];
  const h = plane.head.height;
  const stroke = strokeOf(o, "grid");
  const out = polylines(g.paths, h, "grid", () => stroke);
  for (const l of g.head.labels ?? []) {
    const [x, y] = toPicture(l.x, l.y, h);
    out.push({ type: "text", role: "grid", x, y, text: l.text, color: stroke.color, ...labelAnchor(x, y, plane.head.width, h) });
  }
  return out;
};

/**
 * Where a grid label sits so it stays inside the picture: against the edge its line leaves by (the nearest one), reading
 * inwards, and away from a corner. x, y are picture coordinates of a point on the edge of a w by h picture.
 */
export function labelAnchor(x: number, y: number, w: number, h: number): { anchor: "start" | "middle" | "end"; baseline: "top" | "middle" | "bottom" } {
  const d = { left: x, right: w - x, top: y, bottom: h - y };
  const edge = (Object.keys(d) as (keyof typeof d)[]).reduce((a, b) => (d[b] < d[a] ? b : a));
  const along = (pos: number, size: number) => (pos < 0.15 * size ? "start" : pos > 0.85 * size ? "end" : "middle");
  if (edge === "left") return { anchor: "start", baseline: y < 0.1 * h ? "top" : y > 0.9 * h ? "bottom" : "middle" };
  if (edge === "right") return { anchor: "end", baseline: y < 0.1 * h ? "top" : y > 0.9 * h ? "bottom" : "middle" };
  return { anchor: along(x, w), baseline: edge === "top" ? "top" : "bottom" };
}

function shape(r: SceneRegion, height: number, role: Exclude<MarkRole, "orientation">): Mark {
  const o = outline(r, height);
  return o.kind === "rect" ? { type: "rect", role, x: o.x, y: o.y, w: o.w, h: o.h } : { type: "circle", role, cx: o.cx, cy: o.cy, r: o.r };
}

/** Saved regions (the selected one emphasised), then the working region, then the one being drawn. */
const regions: Renderer<"regions"> = (o, scene, { plane, draft }) => {
  if (o.datasetId !== scene.base.datasetId) return [];
  const h = plane.head.height;
  const out = o.saved.map((r) => shape(r.shape, h, r.selected ? "region" : "saved"));
  if (o.active) out.push(shape(o.active, h, "region"));
  if (draft) out.push(shape(draft, h, "draft"));
  return out;
};

/**
 * Overlay kind -> renderer. A kind without an entry (catalog) is specified in scene/1 but not drawn yet; the figure endpoint
 * refuses such a scene, so a picture never claims a layer it lacks.
 */
const registry: { [K in OverlayKind]?: Renderer<K> } = { orientation, regions, contours, beam, grid };

export const drawnKinds = (): OverlayKind[] => Object.keys(registry) as OverlayKind[];

/** The visible overlays' marks, one layer per overlay in scene order (the first at the bottom). */
export function sceneLayers(scene: Scene, ctx: DrawContext): { id: string; kind: OverlayKind; marks: Mark[] }[] {
  const out: { id: string; kind: OverlayKind; marks: Mark[] }[] = [];
  for (const o of scene.overlays) {
    const draw = registry[o.kind] as Renderer<typeof o.kind> | undefined;
    if (!o.visible || !draw) continue;
    out.push({ id: o.id, kind: o.kind, marks: draw(o as never, scene, ctx) });
  }
  return out;
}

export const sceneMarks = (scene: Scene, ctx: DrawContext): Mark[] => sceneLayers(scene, ctx).flatMap((l) => l.marks);

/** How each role looks on a figure (the viewer styles the same roles with CSS tokens in PlaneViewer.module.css). */
const FIGURE_STROKE: Record<Exclude<MarkRole, "orientation">, { color: string; width: number; dash: number[] }> = {
  region: { color: "#3cc4e8", width: 2, dash: [] },
  saved: { color: "#a7b3c1", width: 1.5, dash: [] },
  draft: { color: "#3cc4e8", width: 1.5, dash: [4, 3] },
};
const HALO = "#06080c"; // --canvas: every line gets a dark halo so it reads over bright and dark pixels alike
export const LABEL_PX = 11;
const FONT = "'Geist Mono Variable', 'Geist Mono', ui-monospace, monospace";
export const dashOf = (width: number): number[] => [Math.max(3, 4 * width), Math.max(2, 3 * width)];

/**
 * Strokes marks on a 2D canvas whose origin is the picture's top-left corner, at sx, sy canvas pixels per image pixel, clipped
 * to the picture (pw, ph image pixels), so a contour or grid line that leaves the image never draws over the caption. Lines and
 * paths get a dark halo first and the ink on top, every halo before every ink, so crossings stay readable.
 */
export function strokeMarks(ctx: CanvasRenderingContext2D, marks: Mark[], sx: number, sy: number, ink: string, picture?: { w: number; h: number }): void {
  ctx.save();
  if (picture) {
    ctx.beginPath();
    ctx.rect(0, 0, picture.w * sx, picture.h * sy);
    ctx.clip();
  }
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  const lines = marks.filter((m): m is Extract<Mark, { type: "line" }> => m.type === "line");
  const paths = marks.filter((m): m is Extract<Mark, { type: "path" }> => m.type === "path");
  const tracePath = (p: Extract<Mark, { type: "path" }>) => {
    ctx.beginPath();
    for (let i = 0; i + 1 < p.points.length; i += 2) (i === 0 ? ctx.moveTo : ctx.lineTo).call(ctx, p.points[i]! * sx, p.points[i + 1]! * sy);
    if (p.closed) ctx.closePath();
  };
  for (const p of paths) {
    if (p.fill === undefined) continue;
    tracePath(p);
    ctx.globalAlpha = p.fill;
    ctx.fillStyle = OVERLAY_COLORS[p.stroke.color];
    ctx.fill();
  }
  ctx.globalAlpha = 1;
  for (const halo of [true, false]) {
    for (const g of lines) {
      const w = g.stroke?.width ?? 2;
      ctx.lineWidth = halo ? w + 2 : w;
      ctx.strokeStyle = halo ? HALO : g.stroke ? OVERLAY_COLORS[g.stroke.color] : ink;
      ctx.globalAlpha = g.opacity;
      ctx.beginPath();
      ctx.moveTo(g.x1 * sx, g.y1 * sy);
      ctx.lineTo(g.x2 * sx, g.y2 * sy);
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    for (const p of paths) {
      ctx.lineWidth = halo ? p.stroke.width + 2 : p.stroke.width;
      ctx.strokeStyle = halo ? HALO : OVERLAY_COLORS[p.stroke.color];
      ctx.setLineDash(p.stroke.dashed ? dashOf(p.stroke.width) : []);
      tracePath(p);
      ctx.stroke();
    }
    ctx.setLineDash([]);
  }
  for (const m of marks) {
    if (m.type !== "rect" && m.type !== "circle") continue;
    const st = FIGURE_STROKE[m.role];
    ctx.lineWidth = st.width;
    ctx.strokeStyle = st.color;
    ctx.setLineDash(st.dash);
    ctx.beginPath();
    if (m.type === "rect") ctx.rect(m.x * sx, m.y * sy, m.w * sx, m.h * sy);
    else ctx.ellipse(m.cx * sx, m.cy * sy, m.r * sx, m.r * sy, 0, 0, Math.PI * 2);
    ctx.stroke();
  }
  ctx.setLineDash([]);
  ctx.font = `${LABEL_PX}px ${FONT}`;
  for (const m of marks) {
    if (m.type !== "text") continue;
    const pad = 3;
    ctx.textAlign = m.anchor === "middle" ? "center" : m.anchor === "end" ? "right" : "left";
    ctx.textBaseline = m.baseline === "top" ? "top" : m.baseline === "bottom" ? "bottom" : "middle";
    const x = m.x * sx + (m.anchor === "start" ? pad : m.anchor === "end" ? -pad : 0);
    const y = m.y * sy + (m.baseline === "bottom" ? -pad : m.baseline === "top" ? pad : 0);
    ctx.lineWidth = 3;
    ctx.strokeStyle = HALO;
    ctx.strokeText(m.text, x, y);
    ctx.fillStyle = OVERLAY_COLORS[m.color];
    ctx.fillText(m.text, x, y);
  }
  ctx.restore();
}
