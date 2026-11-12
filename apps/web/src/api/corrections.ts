import { getJSON, postJSON as post } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** WCS and unit corrections (ADR 0061): a declared override of what the file says, with a reason, kept as an append-only history. The file is never changed. */
export type Corrections = Schemas["Corrections"];
export type CorrectionState = Schemas["CorrectionState"];
export type CorrectionEntry = Schemas["CorrectionEntry"];
export type CorrectionChange = Schemas["CorrectionChange"];
export type CorrectionRecord = Schemas["CorrectionRecord"];
export type CorrectionList = Schemas["CorrectionList"];
export type CorrectionInput = Schemas["CorrectionInput"];
export type CorrectionPreview = Schemas["CorrectionPreview"];
export type CorrectionSaved = Schemas["CorrectionSaved"];
export type CorrectionImpact = Schemas["CorrectionImpact"];
export type CorrectionCoords = Schemas["CorrectionCoords"];
export type SidecarPreview = Schemas["CorrectionSidecarPreview"];

export const listCorrections = (id: number, signal?: AbortSignal) => getJSON<CorrectionList>(`/api/datasets/${id}/corrections`, signal);
export const previewCorrection = (id: number, body: CorrectionInput, signal?: AbortSignal) => post<CorrectionPreview>(`/api/datasets/${id}/corrections/preview`, body, signal);
export const saveCorrection = (id: number, body: CorrectionInput) => post<CorrectionSaved>(`/api/datasets/${id}/corrections`, body);
export const sidecarURL = (id: number) => `/api/datasets/${id}/corrections/sidecar`;
export const previewSidecar = (id: number, sidecar: unknown) => post<SidecarPreview>(`/api/datasets/${id}/corrections/sidecar/preview`, { sidecar });
export const importSidecar = (id: number, sidecar: unknown, expect: number, differentFile?: string) =>
  post<CorrectionSaved>(`/api/datasets/${id}/corrections/sidecar`, { sidecar, confirm: true, expect, ...(differentFile ? { differentFile } : {}) });

/** A keyword's value as text: numbers to 12 significant digits, "—" for none (the file has no such card, or not corrected). */
export function valueText(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "number") return Number.isInteger(v) ? String(v) : String(Number(v.toPrecision(12)));
  return String(v);
}

/** The keywords a correction may set for an image of `naxis` axes, in the order offered. BUNIT only when the file states one. */
export function correctableKeywords(naxis: number, unit: boolean): string[] {
  const out: string[] = unit ? ["BUNIT"] : [];
  for (const k of ["CRVAL", "CRPIX", "CDELT", "CTYPE", "CUNIT"]) for (let i = 1; i <= naxis; i++) out.push(`${k}${i}`);
  out.push("RESTFRQ", "RESTFREQ", "SPECSYS", "EQUINOX", "RADESYS");
  for (const k of ["PC", "CD"]) for (let i = 1; i <= naxis; i++) for (let j = 1; j <= naxis; j++) out.push(`${k}${i}_${j}`);
  return out;
}

/** True for keywords whose value is text; every other one is a number. */
export const textKeyword = (k: string) => k === "BUNIT" || /^(CTYPE|CUNIT)\d$/.test(k) || k === "SPECSYS" || k === "RADESYS";

/** Plain words for an action of the history. */
export const ACTION_TEXT: Record<CorrectionRecord["action"], string> = {
  set: "Corrected",
  revert: "Reverted",
  confirm: "Confirmed for the changed file",
  import: "Imported from a file",
  withdraw: "Withdrew every correction",
};

/** Angular distance in arcseconds between two sky positions in degrees (haversine), for saying how far a correction moves a point. */
export function shiftArcsec(lon1: number, lat1: number, lon2: number, lat2: number): number {
  const r = Math.PI / 180;
  const a = Math.sin(((lat2 - lat1) * r) / 2) ** 2 + Math.cos(lat1 * r) * Math.cos(lat2 * r) * Math.sin(((lon2 - lon1) * r) / 2) ** 2;
  return (2 * Math.asin(Math.min(1, Math.sqrt(a)))) / r * 3600;
}
