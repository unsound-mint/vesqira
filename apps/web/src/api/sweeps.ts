import { request as call, postJSON as post } from "./http";
import type { components } from "./schema";
import type { Board } from "./boards";

type Schemas = components["schemas"];

export type SweepParam = Schemas["SweepParam"];
export type SweepRequest = Schemas["SweepRequest"];
export type Sweep = Schemas["Sweep"];
export type SweepMember = Schemas["SweepMember"];
export type SweepPreview = Schemas["SweepPreview"];
export type SweepReview = Schemas["SweepReview"];
export type SweepGrid = Schemas["SweepGrid"];
export type SweepRow = Schemas["SweepRow"];
export type SweepPlot = Schemas["SweepPlot"];
export type SweepTotals = Schemas["SweepTotals"];
export type SweepEstimate = Schemas["SweepEstimate"];

export const listSweeps = () => call<{ sweeps: Sweep[] }>("/api/sweeps").then((r) => r.sweeps);
export const getSweep = (id: number) => call<Sweep>(`/api/sweeps/${id}`);
export const previewSweep = (r: SweepRequest) => post<SweepPreview>("/api/sweeps/preview", r);
export const createSweep = (r: SweepRequest) => post<Sweep>("/api/sweeps", r);
export const reviewSweep = (id: number) => call<SweepReview>(`/api/sweeps/${id}/review`);
export const submitSweep = (id: number, sweepHash: string, concurrency: number, stopOnFailure: boolean) => post<Sweep>(`/api/sweeps/${id}/submit`, { sweepHash, concurrency, stopOnFailure });
export const cancelSweep = (id: number, cancelRunning: boolean) => post<Sweep>(`/api/sweeps/${id}/cancel`, { cancelRunning });
export const sweepBoard = (id: number) => post<Board>(`/api/sweeps/${id}/board`, {});
export const sweepGrid = (id: number, metric?: string, x?: string) => {
  const q = new URLSearchParams();
  if (metric) q.set("metric", metric);
  if (x) q.set("x", x);
  const qs = q.toString();
  return call<SweepGrid>(`/api/sweeps/${id}/grid${qs ? `?${qs}` : ""}`);
};

/** "8, 12, 16" -> ["8","12","16"]: the list as typed, trimmed, empty entries dropped. */
export const parseValues = (text: string) => text.split(/[,\s]+/).map((v) => v.trim()).filter(Boolean);

export const gib = (bytes: number) => `${(bytes / 2 ** 30).toFixed(bytes % 2 ** 30 === 0 ? 0 : 1)} GiB`;
