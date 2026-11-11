import { ApiError, getJSON, postJSON, requestRaw } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** A ROHSA decomposition run whose Gaussian components can be drawn over a dataset, or the reason they cannot (ADR 0046). */
export type RohsaRun = Schemas["RohsaRunView"];
export type RohsaIndexState = Schemas["RohsaIndexState"];
export type RohsaComponent = Schemas["RohsaComponent"];
export type RohsaHeader = Schemas["RohsaComponentsHeader"];

/** The decomposition record on a run (null for other kinds). It carries no path. */
export type RohsaRecord = {
  label: string;
  datName: string;
  nGauss: number;
  nGaussBasis: string;
  region: string;
  regionBasis: string;
  cropDatasetId: number | null;
  cropName: string;
  notes: string[];
  unitNotes: Record<string, string>;
};

/** The curves of one pixel on the crop's channel grid; model is the sum of the valid components. */
export type Components = { head: RohsaHeader; axis: Float64Array; model: Float64Array; curves: Float64Array[] };

export function parseComponents(buf: ArrayBuffer): Components {
  const dv = new DataView(buf);
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new ApiError("The components response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as RohsaHeader;
  const c = head.channels;
  const g = head.n_gauss;
  if (!Number.isInteger(c) || c < 1 || !Number.isInteger(g) || g < 1 || buf.byteLength - 4 - n !== (g + 2) * c * 8) {
    throw new ApiError("The components response has the wrong size.");
  }
  const at = 4 + n;
  const f64 = (k: number) => {
    const out = new Float64Array(c);
    for (let i = 0; i < c; i++) out[i] = dv.getFloat64(at + (k * c + i) * 8, true);
    return out;
  };
  return { head, axis: f64(0), model: f64(1), curves: Array.from({ length: g }, (_, k) => f64(2 + k)) };
}

const base = (id: number) => `/api/datasets/${id}/rohsa`;

export const listRohsaRuns = (datasetId: number, signal?: AbortSignal) =>
  getJSON<{ runs: RohsaRun[] }>(base(datasetId), signal).then((x) => x.runs);

/** Advances the index of the run's .dat by a bounded amount of work; call again while the state is "building". */
export const indexRohsa = (datasetId: number, runId: number, signal?: AbortSignal) =>
  postJSON<RohsaIndexState>(`${base(datasetId)}/index`, { runId }, signal);

export const getComponents = (datasetId: number, runId: number, x: number, y: number, signal?: AbortSignal) =>
  requestRaw(`${base(datasetId)}/components?run=${runId}&x=${x}&y=${y}`, { signal }).then((r) => r.arrayBuffer()).then(parseComponents);
