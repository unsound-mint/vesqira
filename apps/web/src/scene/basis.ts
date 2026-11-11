import { spectralText } from "../viewer/format";
import { beamText } from "./overlays";
import type { GeometryEntry } from "./geometry";
import type { Scene, SceneOverlay } from "./scene";

const num = (v: number) => String(Number(v.toPrecision(4)));

/** "at every pixel", "at every 2nd pixel", "at every 11th pixel". */
export function everyNth(n: number): string {
  if (n <= 1) return "at every pixel";
  const teen = n % 100 >= 11 && n % 100 <= 13;
  const suffix = teen ? "th" : ({ 1: "st", 2: "nd", 3: "rd" } as Record<number, string>)[n % 10] ?? "th";
  return `at every ${n}${suffix} pixel`;
}

/** The levels as the person set them, e.g. "3, 5, 10 × σ" or "0.5 to 2 K in steps of 0.5". */
export function levelsText(o: Extract<SceneOverlay, { kind: "contours" }>): string {
  const l = o.levels;
  const unit = o.unit ? ` ${o.unit}` : "";
  switch (l.mode) {
    case "sigma":
      return `${l.multiples.map(num).join(", ")} × σ`;
    case "list":
      return `${l.values.map(num).join(", ")}${unit}`;
    case "linear":
      return `${l.count} levels from ${num(l.start)}${unit} in steps of ${num(l.step)}`;
    case "geometric":
      return `${l.count} levels from ${num(l.start)}${unit}, each ${num(l.factor)} times the last`;
  }
}

const KIND_NAME: Record<SceneOverlay["kind"], string> = { contours: "Contours", orientation: "Orientation lines", beam: "Beam", grid: "Coordinate grid", regions: "Regions", catalog: "Catalog" };

/** A short title for an overlay row: what it is and which dataset it comes from. */
export function overlayTitle(o: SceneOverlay, scene: Scene, nameOf: (id: number) => string): string {
  if (o.kind === "grid") return `${KIND_NAME.grid} (${o.frame === "fk5" ? "FK5" : o.frame === "icrs" ? "ICRS" : "Galactic"})`;
  if (o.datasetId === scene.base.datasetId) return `${KIND_NAME[o.kind]} of this map`;
  return `${KIND_NAME[o.kind]} of ${nameOf(o.datasetId)}`;
}

/**
 * What an overlay states about itself, in one sentence: what is drawn, from what, and how it was placed. It is what the
 * overlay list shows under each row and what a figure prints in its caption, so the picture and the screen say the same.
 * null while the geometry is computed; a refusal is the server's reason.
 */
export function overlayBasis(o: SceneOverlay, scene: Scene, entry: GeometryEntry | undefined, nameOf: (id: number) => string): string | null {
  const own = o.datasetId === scene.base.datasetId;
  if (o.kind === "regions") return "Pixel regions of this map.";
  if (o.kind === "catalog") return "Catalog markers are not drawn in this version.";
  if (o.kind === "orientation" && own) {
    const how = o.sampling.mode === "step" ? `sampled ${everyNth(o.sampling.step)}` : `about ${o.sampling.gapPx} screen pixels apart`;
    return `Orientation lines of this map, ${how}, from its own pixel grid (the declared convention).`;
  }
  if (!entry || entry.status === "loading") return null;
  if (entry.status === "refused") return `Not drawn: ${entry.reason}`;
  const g = entry.g;
  const place = g.head.placement.method;
  const name = nameOf(o.datasetId);
  if (o.kind === "contours" && g.kind === "contours") {
    const h = g.head;
    const ch = `channel ${(h.channel ?? 0) + 1}${h.spectral_value !== null && h.spectral_value !== undefined ? ` (${spectralText(h.spectral_value, h.spectral_unit ?? null)})` : ""}`;
    const unit = h.unit ? `${h.unit}${h.unit_status === "user" ? ", declared by you" : ""}` : "file has no unit";
    const sigma = h.sigma ? ` (σ = ${num(h.sigma.value ?? 0)}${h.unit ? ` ${h.unit}` : ""}, 1.4826 × MAD of that channel, signal included)` : "";
    const levels = o.levels.mode === "sigma" ? `${levelsText(o)}${sigma}` : `${levelsText(o)} (${unit})`;
    const neg = (h.levels ?? []).some((v) => v < 0) ? "; negative levels dashed" : "";
    const simp = h.simplified ? `; simplified to within ${h.simplified.tolerance_px} base pixels (${h.simplified.vertices_before} vertices were over the budget)` : "";
    const none = (h.paths ?? 0) === 0 ? "; no contour at these levels falls on the base" : "";
    return `${name} contours of ${ch} at ${levels}; ${place}${neg}${simp}${none}.`;
  }
  if (o.kind === "orientation" && g.kind === "orientation") {
    const h = g.head;
    const rel = h.reliability as { threshold?: number | null; dropped_below_threshold?: number } | null | undefined;
    const by = [o.length === "reliability" ? "length" : "", o.opacity === "reliability" && rel ? "opacity" : ""].filter(Boolean).join(" and ");
    const relText = rel ? `${by ? `; ${by} by its declared reliability (relative to the 99th percentile)` : ""}${rel.threshold !== null && rel.threshold !== undefined ? `; only reliability ≥ ${num(rel.threshold)} (${rel.dropped_below_threshold ?? 0} samples below)` : ""}` : "";
    return `${name} B-field orientation, sampled ${everyNth(h.step ?? 1)} of its grid (${num(h.spacing_px ?? 0)} base pixel${num(h.spacing_px ?? 0) === "1" ? "" : "s"} apart): ${h.conversion}; ${place}${relText}.`;
  }
  if (o.kind === "beam" && g.kind === "beam" && g.head.beam) {
    const b = g.head.beam as { source?: string; note?: string; bmaj_arcsec?: number; bmin_arcsec?: number; bpa_deg?: number | null };
    return `Beam of ${own ? "this map" : name}: ${beamText(b)} from ${b.source ?? "its header"}${b.note ? ` (${b.note})` : ""}; ${place}.`;
  }
  if (o.kind === "grid" && g.kind === "grid") {
    const h = g.head as { frame?: string; lon_step_deg?: number; lat_step_deg?: number };
    return `${h.frame ?? "Celestial"} grid; ${place}.`;
  }
  return null;
}
