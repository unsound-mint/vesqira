import type { components } from "./schema";

type Schemas = components["schemas"];

export type Status = Schemas["Axis"]["ctype_status"];
export type Axis = Schemas["Axis"];
export type Header = Schemas["FitsHeader"];
export type Interpretation = Schemas["Interpretation"];
export type Dataset = Schemas["Dataset"];

export class ApiError extends Error {
  constructor(message: string, readonly reasons: string[] = []) {
    super(message);
  }
}

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API. Is it running on 127.0.0.1:8787?");
  }
  const body = (await res.json().catch(() => null)) as { error?: string; reasons?: string[] } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`, body?.reasons ?? []);
  return body as T;
}

/** Files on this computer that are no longer what was registered. Remote files are not checked here. */
export type Problem = "modified" | "missing" | "not_allowed";
export const getAvailability = () => call<{ problems: Record<string, Problem> }>("/api/datasets/availability").then((r) => r.problems ?? {});
export const listDatasets = () => call<{ datasets: Dataset[] }>("/api/datasets").then((r) => r.datasets);
export const headerCards = (id: number) => call<{ cards: string[]; truncated: boolean }>(`/api/datasets/${id}/header-cards`);
export const listRoots = () => call<{ roots: string[] }>("/api/roots").then((r) => r.roots);
export const registerDataset = (path: string) =>
  call<Dataset>("/api/datasets", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });

export type Compat = { compatible: boolean; reasons: string[] };
export const getCompat = (a: number, b: number) => call<Compat>(`/api/compat?a=${a}&b=${b}`);

export type InterpretationRequest = { kind: "none" } | { kind: "axial_orientation"; unit: "deg" | "rad"; reliabilityId: number | null };
export const setInterpretation = (id: number, body: InterpretationRequest) =>
  call<Dataset>(`/api/datasets/${id}/interpretation`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export type Nature = Dataset["nature"] | "none";
export const setNature = (id: number, nature: Nature) =>
  call<Dataset>(`/api/datasets/${id}/nature`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ nature }) });

export type Preview = { name: string; sizeBytes: number; axes: number; planes: number; planeBytes: number; celestialWcs: boolean; spectralAxis: boolean; access: string };
export const previewFile = (path: string) => call<Preview>(`/api/preview?path=${encodeURIComponent(path)}`);
export const copyDataset = (id: number) =>
  call<{ dataset: Dataset; sha256: string }>(`/api/datasets/${id}/copy`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
