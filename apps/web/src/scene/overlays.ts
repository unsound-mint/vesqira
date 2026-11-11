import type { Plane } from "../api/planes";
import type { Dataset } from "../api/catalog";
import { glyphStride, orientationLines } from "../viewer/glyphs";
import { outline } from "../viewer/region";
import type { OverlayKind, OverlayOf, Scene, SceneRegion } from "./scene";

/**
 * What an overlay draws, as plain marks in picture coordinates (image pixels, x right, y down from the top edge, so a plane
 * row y is at height-1-y). Overlay code never touches a canvas or the DOM: the viewer turns marks into SVG elements and a
 * figure strokes them on its canvas, so the screen and a saved figure draw the same layers by the same code (and an SVG
 * export can later write the same marks).
 */
export type MarkRole = "orientation" | "region" | "saved" | "draft";
export type Mark =
  | { type: "line"; role: "orientation"; x1: number; y1: number; x2: number; y2: number; opacity: number }
  | { type: "rect"; role: Exclude<MarkRole, "orientation">; x: number; y: number; w: number; h: number }
  | { type: "circle"; role: Exclude<MarkRole, "orientation">; cx: number; cy: number; r: number };

/** What drawing needs besides the scene: the base plane as read, and facts the catalog declares about it. */
export type DrawContext = {
  plane: Plane;
  /** Output pixels per image pixel: the screen scale in the viewer, the figure's scale in a figure. */
  scale: number;
  interp: Dataset["interpretation"];
  reliability: Plane | null;
  /** The region being dragged out; a pointer gesture in progress, never part of the scene. */
  draft?: SceneRegion | null;
};

/** The reliability map that can set line opacity: only one on the plane's own grid (figureScene records the same choice). */
export const reliabilityFor = (plane: Plane, reliability: Plane | null): Plane | null =>
  reliability && reliability.head.width === plane.head.width && reliability.head.height === plane.head.height ? reliability : null;

type Renderer<K extends OverlayKind> = (overlay: OverlayOf<K>, scene: Scene, ctx: DrawContext) => Mark[];

/** Orientation lines of the base map itself. A map from another dataset would need WCS placement (a later slice), so it draws nothing. */
const orientation: Renderer<"orientation"> = (o, scene, { plane, interp, reliability, scale }) => {
  if (o.datasetId !== scene.base.datasetId || !interp || scale <= 0) return [];
  const { width: w, height: h } = plane.head;
  const rel = o.opacity === "reliability" ? reliabilityFor(plane, reliability) : null;
  const stride = o.sampling.mode === "step" ? o.sampling.step : glyphStride(scale, o.sampling.gapPx);
  return orientationLines(plane.data, w, h, interp.unit === "deg" ? 1 : 180 / Math.PI, {
    stride,
    reliability: rel?.data ?? null,
    relScale: rel ? (rel.head.stats.p99 ?? undefined) : undefined,
  }).map((g) => ({ type: "line", role: "orientation", ...g }));
};

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
 * Overlay kind -> renderer. A kind without an entry is specified in scene/1 but not drawn yet (contours, catalog, beam,
 * grid land in later slices); the figure endpoint refuses such a scene, so a picture never claims a layer it lacks.
 */
const registry: { [K in OverlayKind]?: Renderer<K> } = { orientation, regions };

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

/**
 * Strokes marks on a 2D canvas whose origin is the picture's top-left corner, at sx, sy canvas pixels per image pixel.
 * Lines get a dark halo first and the light line on top, every halo before every line, so crossings stay readable.
 */
export function strokeMarks(ctx: CanvasRenderingContext2D, marks: Mark[], sx: number, sy: number, ink: string): void {
  ctx.save();
  ctx.lineCap = "round";
  const lines = marks.filter((m): m is Extract<Mark, { type: "line" }> => m.type === "line");
  for (const [w, color] of [[4, "#06080c"], [2, ink]] as const) {
    ctx.lineWidth = w;
    ctx.strokeStyle = color;
    for (const g of lines) {
      ctx.globalAlpha = g.opacity;
      ctx.beginPath();
      ctx.moveTo(g.x1 * sx, g.y1 * sy);
      ctx.lineTo(g.x2 * sx, g.y2 * sy);
      ctx.stroke();
    }
  }
  ctx.globalAlpha = 1;
  for (const m of marks) {
    if (m.type === "line") continue;
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
  ctx.restore();
}
