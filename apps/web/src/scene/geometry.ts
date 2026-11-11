import type { OverlayGeometry } from "../api/overlays";
import type { Scene, SceneOverlay } from "./scene";

/**
 * What the worker computes for an overlay (ADR 0042, section 3): contours, the beam and the grid always, orientation lines
 * only from another dataset (the base map's own lines are drawn from the plane on screen). Regions need nothing.
 */
export function needsGeometry(o: SceneOverlay, scene: Scene): boolean {
  return o.kind === "contours" || o.kind === "beam" || o.kind === "grid" || (o.kind === "orientation" && o.datasetId !== scene.base.datasetId);
}

/**
 * The spacing between orientation lines asked of the worker, in base pixels, for automatic sampling: gapPx output pixels at
 * this scale, rounded to a power of the square root of two so zooming refetches only when the step really changes.
 * null when the overlay does not use it.
 */
export function spacingFor(o: SceneOverlay, scale: number): number | null {
  if (o.kind !== "orientation" || o.sampling.mode !== "auto" || !(scale > 0)) return null;
  const raw = o.sampling.gapPx / scale;
  const q = 2 ** (Math.round(Math.log2(raw) * 2) / 2);
  return Math.min(4096, Math.max(1, Math.round(q * 100) / 100));
}

/** Everything that changes an overlay's geometry: the base, its kind-specific settings and the spacing. Not its id, visibility or style. */
export function geometryKey(baseId: number, o: SceneOverlay, spacing: number | null): string {
  const { id: _i, visible: _v, ...rest } = o as SceneOverlay & { color?: string; width?: number; dash?: string };
  const { color: _c, width: _w, dash: _d, ...settings } = rest as Record<string, unknown>;
  return JSON.stringify([baseId, settings, spacing]);
}

/** The geometry of each overlay that needs one, by overlay id: being computed, ready, or refused with the server's reason. */
export type GeometryEntry =
  | { key: string; status: "loading" }
  | { key: string; status: "ready"; g: OverlayGeometry }
  | { key: string; status: "refused"; reason: string };
export type GeometryMap = Readonly<Record<string, GeometryEntry>>;

/** The ready geometry of an overlay of the given kind, or null. */
type GeometryOf<K extends OverlayGeometry["kind"]> = K extends "orientation" ? Extract<OverlayGeometry, { kind: "orientation" }> : Exclude<OverlayGeometry, { kind: "orientation" }>;
export function readyGeometry<K extends OverlayGeometry["kind"]>(map: GeometryMap | undefined, id: string, kind: K): GeometryOf<K> | null {
  const e = map?.[id];
  return e && e.status === "ready" && e.g.kind === kind ? (e.g as GeometryOf<K>) : null;
}

/**
 * A base pixel position (0-based, centres at integers, y up) in picture coordinates (x right, y down from the top edge of an
 * image `height` pixels tall, a pixel's square spanning [x, x+1]). An angle measured counter-clockwise from +x in base pixels
 * is drawn with dy = -sin, because the picture's y points down.
 */
export const toPicture = (x: number, y: number, height: number): [number, number] => [x + 0.5, height - 0.5 - y];
