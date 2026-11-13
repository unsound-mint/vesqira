import { isCatalog, isImage, type CatalogSummary, type ImageSummary, type DatasetSummary } from "../api/catalog";
import type { OverlayColor } from "./palette";
import type { Scene, SceneOverlay } from "./scene";
import { ORIENTATION_ID, REGIONS_ID } from "./scene";

/**
 * The overlay form's state, as typed (strings for numbers), and its conversion to a scene/1 overlay. The conversion checks
 * what the server checks (strictly increasing levels, a non-zero step, a factor above 1) so a mistake is said next to the
 * field, but the server's strict reading stays the authority.
 */
export type AddableKind = "contours" | "orientation" | "catalog" | "beam" | "grid";
export type LevelMode = "sigma" | "list" | "linear" | "geometric";
export type Draft = {
  kind: AddableKind;
  datasetId: number | null;
  channel: string; // 1-based, as the viewer counts channels
  levelMode: LevelMode;
  values: string; // sigma multiples or a level list, comma or space separated
  start: string;
  step: string;
  factor: string;
  count: string;
  sampling: "auto" | "step";
  gapPx: string;
  stepPx: string;
  length: "fixed" | "reliability";
  opacity: "uniform" | "reliability";
  threshold: string;
  corner: "bottom-left" | "bottom-right" | "top-left" | "top-right";
  frame: "icrs" | "fk5" | "galactic";
  vectors: boolean; // catalog: draw the declared angle as a headless segment
  lengthColumn: string; // catalog: "" for the same length everywhere
  lengthScale: string; // catalog: base pixels per unit of the length column, "" for the automatic scale
  colorColumn: string; // catalog: "" for one colour
  filterColumn: string; // catalog: "" for no row filter
  filterSigma: string; // catalog: the filter column's uncertainty, "" for none
  filterMin: string;
  color: OverlayColor;
  width: string;
  dash: "solid" | "dashed";
};

export const KIND_COLOR: Record<AddableKind, OverlayColor> = { contours: "amber", orientation: "cyan", catalog: "lime", beam: "white", grid: "white" };

export function emptyDraft(kind: AddableKind, datasetId: number | null): Draft {
  return {
    kind, datasetId, channel: "1", levelMode: "sigma", values: "3, 5, 10", start: "1", step: "1", factor: "2", count: "5",
    sampling: "auto", gapPx: "16", stepPx: "4", length: "fixed", opacity: "uniform", threshold: "", corner: "bottom-right", frame: "icrs",
    vectors: false, lengthColumn: "", lengthScale: "", colorColumn: "", filterColumn: "", filterSigma: "", filterMin: "3",
    color: KIND_COLOR[kind], width: "", dash: kind === "grid" ? "dashed" : "solid",
  };
}

/** A point catalog has no grid to contour and no beam, so only images and cubes are overlay sources. */
export const imageDatasets = (datasets: DatasetSummary[]): ImageSummary[] => datasets.filter((d): d is ImageSummary => isImage(d));

/** The catalogs a catalog overlay may draw: point catalogs whose column declaration is in force (a missing or stale one would be refused). */
export const catalogCandidates = (datasets: DatasetSummary[]): CatalogSummary[] =>
  datasets.filter((d): d is CatalogSummary => isCatalog(d) && !d.archived && !!d.catalogDeclaration && !d.catalogDeclaration.stale);

/** The columns a catalog overlay can read numbers from: one value per row, numeric as the worker read them. */
export const numericColumns = (d: CatalogSummary) => d.facts.numericColumns;

/** The datasets an overlay of this kind may come from: orientation lines from another map declared axial; contours from any map not declared as angles; a beam from any; the grid from the base only. */
export function candidates(kind: AddableKind, datasets: DatasetSummary[], baseId: number): ImageSummary[] {
  const images = imageDatasets(datasets);
  if (kind === "grid") return images.filter((d) => d.id === baseId);
  if (kind === "orientation") return images.filter((d) => d.id !== baseId && d.interpretation?.kind === "axial_orientation");
  if (kind === "contours") return images.filter((d) => !d.interpretation);
  if (kind === "catalog") return [];
  return images;
}

/** The overlay dataset's brightness unit as the worker will state it: the file's BUNIT, else a unit declared for it (not stale). */
export function unitOf(d: ImageSummary): string | undefined {
  if (d.facts.bunit) return d.facts.bunit;
  return d.unitDeclaration && !d.unitDeclaration.stale ? d.unitDeclaration.unit : undefined;
}

/** The channels of a registered dataset by its header facts: the third axis, or one plane. The worker checks the index again. */
export const channelsOf = (d: ImageSummary): number => d.facts.channels ?? 1;

const numbers = (text: string): number[] | null => {
  const parts = text.split(/[\s,;]+/).filter(Boolean);
  if (!parts.length) return null;
  const out = parts.map(Number);
  return out.every(Number.isFinite) ? out : null;
};
const increasing = (v: number[]) => v.every((x, i) => i === 0 || x > v[i - 1]!);

/** A fresh overlay id for a kind, never one of the built-in ids. */
export function newOverlayId(kind: AddableKind, scene: Scene): string {
  const used = new Set(scene.overlays.map((o) => o.id));
  for (let n = 1; ; n++) {
    const id = `${kind}-${n}`;
    if (!used.has(id) && id !== ORIENTATION_ID && id !== REGIONS_ID) return id;
  }
}

/** The overlay the draft describes, or the first problem as a sentence. */
export function draftToOverlay(d: Draft, id: string, visible: boolean, datasets: DatasetSummary[], baseId: number): SceneOverlay | string {
  if (d.kind === "catalog") return catalogOverlay(d, id, visible, datasets);
  const pool = imageDatasets(datasets);
  const ds = d.kind === "grid" ? pool.find((x) => x.id === baseId) : pool.find((x) => x.id === d.datasetId);
  if (!ds) return "Choose the dataset the overlay comes from.";
  const style: { color: OverlayColor; width?: number; dash?: "dashed" } = { color: d.color };
  if (d.width.trim()) {
    const w = Number(d.width);
    if (!Number.isFinite(w) || w < 0.5 || w > 4) return "The line width is between 0.5 and 4 pixels.";
    style.width = w;
  }
  if (d.dash === "dashed") style.dash = "dashed";
  const common = { id, datasetId: ds.id, visible, ...style };
  switch (d.kind) {
    case "contours": {
      const ch = Number(d.channel);
      const n = channelsOf(ds);
      if (!Number.isInteger(ch) || ch < 1 || ch > n) return `The channel is a whole number from 1 to ${n}.`;
      const unit = unitOf(ds);
      let levels: Extract<SceneOverlay, { kind: "contours" }>["levels"];
      if (d.levelMode === "sigma" || d.levelMode === "list") {
        const v = numbers(d.values);
        if (!v || v.length > 64 || !increasing(v)) return "Levels are up to 64 numbers, each larger than the last.";
        if (d.levelMode === "sigma" && v.includes(0)) return "A multiple of σ cannot be zero.";
        levels = d.levelMode === "sigma" ? { mode: "sigma", multiples: v } : { mode: "list", values: v };
      } else {
        const start = Number(d.start);
        const count = Number(d.count);
        if (!Number.isFinite(start) || !Number.isInteger(count) || count < 1 || count > 64) return "Give a start and a count from 1 to 64.";
        if (d.levelMode === "linear") {
          const step = Number(d.step);
          if (!Number.isFinite(step) || step === 0) return "The step is a number that is not zero.";
          levels = { mode: "linear", start, step, count };
        } else {
          const factor = Number(d.factor);
          if (start === 0 || !Number.isFinite(factor) || factor <= 1) return "Geometric levels need a start that is not zero and a factor above 1.";
          levels = { mode: "geometric", start, factor, count };
        }
      }
      return { kind: "contours", ...common, channel: ch - 1, levels, ...(unit ? { unit } : {}) };
    }
    case "orientation": {
      let sampling: Extract<SceneOverlay, { kind: "orientation" }>["sampling"];
      if (d.sampling === "auto") {
        const gap = Number(d.gapPx);
        if (!Number.isFinite(gap) || gap < 4 || gap > 256) return "Lines are 4 to 256 screen pixels apart.";
        sampling = { mode: "auto", gapPx: gap };
      } else {
        const step = Number(d.stepPx);
        if (!Number.isInteger(step) || step < 1 || step > 4096) return "The step is a whole number of the map's pixels, 1 to 4096.";
        sampling = { mode: "step", step };
      }
      const hasRel = ds.interpretation?.reliabilityId != null;
      if (!hasRel && (d.length === "reliability" || d.opacity === "reliability" || d.threshold.trim())) return "Length, opacity or a threshold by reliability needs a reliability map declared with that orientation map.";
      let threshold: number | undefined;
      if (d.threshold.trim()) {
        threshold = Number(d.threshold);
        if (!Number.isFinite(threshold)) return "The reliability threshold is a number.";
      }
      return { kind: "orientation", ...common, sampling, length: d.length, opacity: d.opacity, ...(threshold !== undefined ? { threshold } : {}) };
    }
    case "beam":
      return { kind: "beam", ...common, ...(d.corner !== "bottom-left" ? { corner: d.corner } : {}) };
    case "grid":
      return { kind: "grid", ...common, frame: d.frame };
  }
}

function styleOf(d: Draft): { color: OverlayColor; width?: number; dash?: "dashed" } | string {
  const style: { color: OverlayColor; width?: number; dash?: "dashed" } = { color: d.color };
  if (d.width.trim()) {
    const w = Number(d.width);
    if (!Number.isFinite(w) || w < 0.5 || w > 4) return "The line width is between 0.5 and 4 pixels.";
    style.width = w;
  }
  if (d.dash === "dashed") style.dash = "dashed";
  return style;
}

/** A catalog overlay from the draft. The position columns, frame and the angle's meaning are the catalog's declaration, not the draft's. */
function catalogOverlay(d: Draft, id: string, visible: boolean, datasets: DatasetSummary[]): SceneOverlay | string {
  const ds = catalogCandidates(datasets).find((x) => x.id === d.datasetId);
  if (!ds) return "Choose a point catalog whose columns are declared (and not out of date).";
  const style = styleOf(d);
  if (typeof style === "string") return style;
  const cols = new Set(numericColumns(ds).map((c) => c.name));
  const angle = ds.catalogDeclaration?.angle;
  if (d.vectors) {
    if (!angle) return "This catalog declares no angle column, so there is nothing to draw as vectors.";
    if (!angle.quantity || angle.quantity === "unspecified") return `Declare what the angle column ${angle.column} is (the B-field, a gradient, dust polarization or starlight polarization) before drawing vectors.`;
  } else if (d.lengthColumn) {
    return "A length column needs vectors to be on.";
  }
  for (const [what, name] of [["length", d.lengthColumn], ["colour", d.colorColumn], ["filter", d.filterColumn], ["filter uncertainty", d.filterSigma]] as const) {
    if (name && !cols.has(name)) return `The ${what} column ${name} is not a numeric column of this catalog.`;
  }
  let lengthScale: number | undefined;
  if (d.lengthScale.trim()) {
    lengthScale = Number(d.lengthScale);
    if (!d.lengthColumn || !Number.isFinite(lengthScale) || lengthScale <= 0) return "The length scale is a positive number of base pixels per unit, and goes with a length column.";
  }
  let filter: { column: string; sigmaColumn?: string; min: number } | undefined;
  if (d.filterColumn) {
    const min = Number(d.filterMin);
    if (!d.filterMin.trim() || !Number.isFinite(min)) return "The filter's minimum is a number.";
    filter = { column: d.filterColumn, ...(d.filterSigma ? { sigmaColumn: d.filterSigma } : {}), min };
  } else if (d.filterSigma) {
    return "Choose the column the filter applies to, or clear its uncertainty column.";
  }
  return {
    kind: "catalog", id, datasetId: ds.id, visible, ...style,
    ...(d.vectors ? { vectors: true } : {}),
    ...(d.vectors && d.lengthColumn ? { lengthColumn: d.lengthColumn } : {}),
    ...(lengthScale !== undefined ? { lengthScale } : {}),
    ...(d.colorColumn ? { colorColumn: d.colorColumn } : {}),
    ...(filter ? { filter } : {}),
  };
}

/** The draft that edits an existing overlay. */
export function overlayToDraft(o: SceneOverlay): Draft | null {
  if (o.kind !== "contours" && o.kind !== "orientation" && o.kind !== "catalog" && o.kind !== "beam" && o.kind !== "grid") return null;
  const d = emptyDraft(o.kind, o.datasetId);
  const st = o as { color?: OverlayColor; width?: number; dash?: string };
  d.color = st.color ?? "white";
  d.width = st.width !== undefined ? String(st.width) : "";
  d.dash = st.dash === "dashed" ? "dashed" : "solid";
  if (o.kind === "contours") {
    d.channel = String(o.channel + 1);
    d.levelMode = o.levels.mode;
    if (o.levels.mode === "sigma") d.values = o.levels.multiples.join(", ");
    if (o.levels.mode === "list") d.values = o.levels.values.join(", ");
    if (o.levels.mode === "linear") Object.assign(d, { start: String(o.levels.start), step: String(o.levels.step), count: String(o.levels.count) });
    if (o.levels.mode === "geometric") Object.assign(d, { start: String(o.levels.start), factor: String(o.levels.factor), count: String(o.levels.count) });
  } else if (o.kind === "orientation") {
    d.sampling = o.sampling.mode;
    if (o.sampling.mode === "auto") d.gapPx = String(o.sampling.gapPx);
    else d.stepPx = String(o.sampling.step);
    d.length = o.length;
    d.opacity = o.opacity;
    d.threshold = o.threshold !== undefined ? String(o.threshold) : "";
  } else if (o.kind === "catalog") {
    d.vectors = !!o.vectors;
    d.lengthColumn = o.lengthColumn ?? "";
    d.lengthScale = o.lengthScale !== undefined ? String(o.lengthScale) : "";
    d.colorColumn = o.colorColumn ?? "";
    d.filterColumn = o.filter?.column ?? "";
    d.filterSigma = o.filter?.sigmaColumn ?? "";
    d.filterMin = o.filter ? String(o.filter.min) : "3";
  } else if (o.kind === "beam") {
    d.corner = o.corner ?? "bottom-left";
  } else {
    d.frame = o.frame;
  }
  return d;
}
