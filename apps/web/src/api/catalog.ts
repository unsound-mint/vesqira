import type { components } from "./schema";

type Schemas = components["schemas"];

export type Status = Schemas["Axis"]["ctype_status"];
export type Axis = Schemas["Axis"];
export type Header = Schemas["FitsHeader"];
export type Interpretation = Schemas["Interpretation"];
/** `archived` is set by the server on every dataset it sends (ADR 0041); the generated type predates it. */
export type Dataset = Schemas["Dataset"] & { archived?: boolean };

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
/** Without `archived` the server hides archived datasets; "all" is for the explorer, which filters them itself. */
export const listDatasets = (opts: { archived?: "all" | "only" } = {}) => call<{ datasets: Dataset[] }>(`/api/datasets${opts.archived ? `?archived=${opts.archived}` : ""}`).then((r) => r.datasets);
export const headerCards = (id: number) => call<{ cards: string[]; truncated: boolean }>(`/api/datasets/${id}/header-cards`);
export const listRoots = () => call<{ roots: string[] }>("/api/roots").then((r) => r.roots);
export const registerDataset = (path: string) =>
  call<Dataset>("/api/datasets", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });

export type Compat = { compatible: boolean; reasons: string[] };
export const getCompat = (a: number, b: number) => call<Compat>(`/api/compat?a=${a}&b=${b}`);

export type AngleFrame = NonNullable<Interpretation["frame"]>;
export type AngleQuantity = NonNullable<Interpretation["quantity"]>;
/** frame and quantity are null when the person has not declared them: the file never says. */
export type InterpretationRequest =
  | { kind: "none" }
  | { kind: "axial_orientation"; unit: "deg" | "rad"; reliabilityId: number | null; frame: AngleFrame | null; quantity: AngleQuantity | null };
export const setInterpretation = (id: number, body: InterpretationRequest) =>
  call<Dataset>(`/api/datasets/${id}/interpretation`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

/** Declare (or withdraw) the unit of a file whose header has no BUNIT. The file and its header facts are not changed. */
export const setUnit = (id: number, body: { unit: string } | { clear: true }) =>
  call<Dataset>(`/api/datasets/${id}/unit`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export type Nature = Dataset["nature"] | "none";
export const setNature = (id: number, nature: Nature) =>
  call<Dataset>(`/api/datasets/${id}/nature`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ nature }) });

export type Preview = { name: string; sizeBytes: number; axes: number; planes: number; planeBytes: number; celestialWcs: boolean; spectralAxis: boolean; access: string };
export const previewFile = (path: string) => call<Preview>(`/api/preview?path=${encodeURIComponent(path)}`);
export const copyDataset = (id: number) =>
  call<{ dataset: Dataset; sha256: string }>(`/api/datasets/${id}/copy`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
