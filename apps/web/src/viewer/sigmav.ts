/**
 * The sigma-v diagram of a ROHSA decomposition and its maps per width class (ADR 0046, "Reviewing the decomposition"): the
 * arithmetic the panel and the plot share. The binning, the weights and the units are the worker's; nothing here recomputes them.
 */
import type { RohsaCellMask, RohsaSigmav } from "../api/rohsa";

export type Cell = { s: number; v: number; count: number; weight: number };

/** The non-empty cells of the diagram, by (width bin, centre bin). */
export function cellsOf(d: RohsaSigmav): Map<string, Cell> {
  const m = new Map<string, Cell>();
  for (const [s, v, count, weight] of d.cells) m.set(key(s!, v!), { s: s!, v: v!, count: count!, weight: weight! });
  return m;
}

export const key = (s: number, v: number) => `${s},${v}`;

/**
 * The colour scale of the diagram: log10 of the summed weight over the cells whose weight is above zero. A cell with contributors and a
 * weight of zero (or below) is not on a log scale; it is drawn with a marker of its own, so it never looks like an empty cell.
 */
export type LogScale = { lo: number; hi: number; decades: number[] } | null;

export function logScale(cells: Iterable<Cell>): LogScale {
  let lo = Infinity;
  let hi = -Infinity;
  for (const c of cells) {
    if (c.weight > 0 && Number.isFinite(c.weight)) {
      const l = Math.log10(c.weight);
      lo = Math.min(lo, l);
      hi = Math.max(hi, l);
    }
  }
  if (!Number.isFinite(lo)) return null;
  const decades: number[] = [];
  for (let k = Math.ceil(lo); k <= Math.floor(hi); k++) decades.push(k);
  return { lo, hi, decades };
}

/** Position 0..1 of a weight on the log scale; a single-valued scale puts everything at the middle. */
export function scaleT(scale: NonNullable<LogScale>, weight: number): number {
  return scale.hi === scale.lo ? 0.5 : Math.min(1, Math.max(0, (Math.log10(weight) - scale.lo) / (scale.hi - scale.lo)));
}

/** The bin a cell is in as a half-open interval, in the diagram's unit. */
export function cellRange(edges: number[], i: number): [number, number] {
  return [edges[i]!, edges[i + 1]!];
}

const num = (v: number) => String(Number(v.toPrecision(4)));

/** "[lo, hi)" as the worker bins (half-open: a value on the upper edge is in the next bin). */
export const rangeText = (r: [number, number]) => `[${num(r[0])}, ${num(r[1])})`;

/** The weighting in one sentence: what is summed and its unit. */
export function weightingText(d: RohsaSigmav): string {
  const w = { integrated: "integrated intensity", column_thin: "optically thin column density", count: "number of components" }[d.weight as "integrated"] ?? d.weight;
  return `weighted by ${w} (${d.weight_unit})`;
}

export type Axes = { x: string; y: string; unit: string };

/** The axis labels, each with its unit; widths and centres stay in channels when the crop has no resolved velocity axis. */
export function axisLabels(d: RohsaSigmav): Axes {
  const unit = d.axes.centre?.unit ?? "channels";
  if (unit === "km/s") return { x: `centroid velocity (km/s)`, y: "σ (km/s)", unit };
  return { x: "centroid (channel, 0-based)", y: "σ (channels)", unit: "channels" };
}

/** The bins as the person typed them: empty means the worker's defaults. Returns the bins, or the sentence that says what is wrong. */
export type BinText = { min: string; max: string; n: string };
export type Bins = { min: number; max: number; n: number };

export function parseBins(t: BinText, name: string, maxBins = 128): { bins: Bins | null; error: string | null } {
  if (t.min.trim() === "" && t.max.trim() === "" && t.n.trim() === "") return { bins: null, error: null };
  const min = Number(t.min);
  const max = Number(t.max);
  const n = Number(t.n);
  if (t.min.trim() === "" || t.max.trim() === "" || t.n.trim() === "" || !Number.isFinite(min) || !Number.isFinite(max) || !Number.isFinite(n)) return { bins: null, error: `${name}: give min, max and the number of bins, or leave all three empty for the defaults.` };
  if (!(max > min)) return { bins: null, error: `${name}: max must be above min.` };
  if (!Number.isInteger(n) || n < 1 || n > maxBins) return { bins: null, error: `${name}: the number of bins is a whole number from 1 to ${maxBins}.` };
  return { bins: { min, max, n }, error: null };
}

/** Width edges as typed ("2.03" or "1.5, 4"): positive, strictly increasing, at most seven. */
export function parseEdges(text: string): { edges: number[] | null; error: string | null } {
  const parts = text.split(/[\s,;]+/).filter((p) => p !== "");
  if (parts.length < 1 || parts.length > 7) return { edges: null, error: "Give from 1 to 7 width edges in km/s." };
  const edges = parts.map(Number);
  if (!edges.every((e) => Number.isFinite(e) && e > 0) || edges.some((e, i) => i > 0 && e <= edges[i - 1]!)) return { edges: null, error: "The width edges must be positive numbers in increasing order." };
  return { edges, error: null };
}

/**
 * Unpacks a pixel mask the worker sent: width x height booleans in row-major order (row 0 is the fit's first row, the FITS bottom row;
 * x fastest), 8 to a byte most significant bit first, deflated, base64. Returns one byte per pixel, 1 for a pixel in the cell.
 */
export async function decodeMask(m: RohsaCellMask["mask"]): Promise<Uint8Array> {
  if (m.encoding !== "packbits-msb-row-major+zlib+base64") throw new Error(`Unknown mask encoding ${m.encoding}.`);
  const bin = atob(m.data);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  const ds = new DecompressionStream("deflate");
  const w = ds.writable.getWriter();
  void w.write(bytes).catch(() => undefined);
  void w.close().catch(() => undefined);
  const packed = new Uint8Array(await new Response(ds.readable).arrayBuffer());
  const n = m.width * m.height;
  if (packed.length < Math.ceil(n / 8)) throw new Error("The pixel mask is shorter than its size.");
  const out = new Uint8Array(n);
  for (let i = 0; i < n; i++) out[i] = (packed[i >> 3]! >> (7 - (i & 7))) & 1;
  return out;
}

/** The provenance an exported diagram carries: what was summed, in which bins, from which fit. No folder, no host. */
export type SigmavProvenance = {
  tool: "Vesqira sigma-v diagram";
  run: string;
  runId: number;
  crop: string;
  fit: { name: string; sizeBytes: number; mtimeNs: number };
  weight: string;
  weightUnit: string;
  weightHow: string;
  scale: "log10 of the summed weight per cell";
  centre: { unit: string; edges: number[] };
  sigma: { unit: string; edges: number[] };
  binning: string;
  components: number;
  invalidSigma: number;
  outsideDiagram: number;
  cut: { sigma: number; unit: string } | null;
  unitSource: string | null;
  exportedAt: string;
};

export function provenanceOf(d: RohsaSigmav, run: string, cut: number | null, exportedAt: string): SigmavProvenance {
  const unit = axisLabels(d).unit;
  return {
    tool: "Vesqira sigma-v diagram",
    run,
    runId: d.runId,
    crop: d.cropName ?? "",
    fit: { name: d.dat.name ?? "", sizeBytes: d.dat.size_bytes ?? 0, mtimeNs: d.dat.mtime_ns ?? 0 },
    weight: d.weight,
    weightUnit: d.weight_unit,
    weightHow: d.weight_how,
    scale: "log10 of the summed weight per cell",
    centre: { unit, edges: d.v_edges },
    sigma: { unit, edges: d.sigma_edges },
    binning: d.binning ?? "",
    components: d.components,
    invalidSigma: d.invalid_sigma,
    outsideDiagram: d.outside,
    cut: cut !== null && unit === "km/s" ? { sigma: cut, unit } : null,
    unitSource: d.unit_source ?? null,
    exportedAt,
  };
}
