import { request as call, postJSON as post } from "./http";
import type { Dataset } from "./catalog";
import type { Job } from "./jobs";
import type { components } from "./schema";
import type { Controls } from "./runs";
import type { MaskUse } from "./masks";

export type AnalysisOp = "axial_diff" | "scalar_diff" | "stats" | "moment" | "cutout" | "slab" | "align" | "convolve";
/** What to do with one mask of the source in the new file (ADR 0055): carry it, or leave it behind. */
export type CarryChoice = { id: number; carry: boolean };
export type AnalysisRequest = { op: AnalysisOp; datasetId: number; referenceId?: number; params?: Record<string, string>; masks?: MaskUse[]; carryMasks?: CarryChoice[]; cpus?: number; mem?: string; time?: string };

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
  /** The masks that were applied, each with its own policy, and how many pixels with a value they left out (ADR 0055). */
  masks?: AppliedMask[];
  excluded_by_mask?: number;
};

export type AppliedMask = { id: number; name: string; type: string; policy: string; weight?: number; tier?: string; pixels_flagged_with_value: number; pixels_excluded: number };

/** The masks a result carried from its source, left behind, and that its no-value mask was computed again (ADR 0055). No ids, no paths. */
export type MaskCarryRecord = {
  operation: string;
  by: string;
  flagsFollow: string;
  carried: { type: string; name: string; tier: string; mappingVersion?: string; fingerprint: string; carriedFingerprint: string }[];
  leftBehind: { type: string; name: string; tier: string; mappingVersion?: string; fingerprint: string }[];
  recomputed: string[];
};

export type Derived = { datasetId: number; op: string; params: { options?: Record<string, string>; controls?: Controls; masks?: { type: string; name: string; policy: string; weight?: number; tier: string; mappingVersion?: string }[]; maskCarry?: MaskCarryRecord }; sources: number[]; summary: { policy?: string; unit?: string }; software: Record<string, string>; createdAt: string };

export const runAnalysis = (r: AnalysisRequest, signal?: AbortSignal, requestId?: string) => post<{ summary: StatsSummary | Record<string, unknown>; dataset?: Dataset }>("/api/analysis", r, signal, requestId);
export const createAnalysisJob = (r: AnalysisRequest) => post<Job>("/api/analysis/job", r);
export const getDerived = (id: number) => call<Derived>(`/api/datasets/${id}/derived`);
type Schemas = components["schemas"];
/** Relative orientation (ADR 0047): a declared orientation map against a catalog with a declared angle, or against a second map. */
export type RelOrientParams = Schemas["RelativeOrientationParams"];
export type RelOrientSummary = Schemas["RelativeOrientationSummary"];
/** Aborting the request stops the computation: the server kills the program when its request ends. */
export const runRelativeOrientation = (r: { datasetId: number; otherId: number; params: RelOrientParams; masks?: MaskUse[]; otherMasks?: MaskUse[] }, signal?: AbortSignal, requestId?: string) =>
  post<{ dataset: Dataset; summary: RelOrientSummary }>("/api/analysis/relative-orientation", r, signal, requestId);
export const relativeOrientationTableUrl = (id: number) => `/api/datasets/${id}/relative-orientation.csv`;
/** Sample a HEALPix polarization map onto a dataset's celestial grid as Q and U (ADR 0067). The request runs on this computer and stops with its request. */
export type HealpixSampleRequest = Schemas["HealpixSampleRequest"];
export type HealpixSampleResult = Omit<Schemas["HealpixSampleResult"], "dataset" | "p" | "sigmaP"> & { dataset: Dataset; p: Dataset | null; sigmaP: Dataset | null };
export const runHealpixSample = (r: HealpixSampleRequest, signal?: AbortSignal, requestId?: string) => post<HealpixSampleResult>("/api/analysis/healpix-sample", r, signal, requestId);
export const jobResult =(id: number) => call<{ summary: StatsSummary | Record<string, unknown> }>(`/api/jobs/${id}/result`);
