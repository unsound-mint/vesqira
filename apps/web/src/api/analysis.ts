import { ApiError } from "./catalog";
import type { Dataset } from "./catalog";
import type { Job } from "./jobs";

export type AnalysisOp = "axial_diff" | "scalar_diff" | "stats" | "moment" | "cutout" | "slab" | "align" | "convolve";
export type AnalysisRequest = { op: AnalysisOp; datasetId: number; referenceId?: number; params?: Record<string, string>; cpus?: number; mem?: string; time?: string };

export type StatsSummary = {
  channels: [number, number];
  finite: number;
  no_value: number;
  min: number | null;
  max: number | null;
  mean: number | null;
  std: number | null;
  peak: { value: number; channel: number; x: number; y: number; sky?: { lon: number; lat: number } | null } | null;
  per_channel: { channel: number; finite: number; rms: number | null; mad_sigma: number | null }[];
  policy: string;
};

export type Derived = { datasetId: number; op: string; params: { options?: Record<string, string> }; sources: number[]; summary: { policy?: string; unit?: string }; software: Record<string, string>; createdAt: string };

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export const runAnalysis = (r: AnalysisRequest) => post<{ summary: StatsSummary | Record<string, unknown>; dataset?: Dataset }>("/api/analysis", r);
export const createAnalysisJob = (r: AnalysisRequest) => post<Job>("/api/analysis/job", r);
export const getDerived = (id: number) => call<Derived>(`/api/datasets/${id}/derived`);
export const jobResult = (id: number) => call<{ summary: StatsSummary | Record<string, unknown> }>(`/api/jobs/${id}/result`);
