import { ApiError } from "./catalog";
import type { RegionShape } from "./planes";

/** A named region saved with a dataset: whole 0-based pixels, a rectangle with inclusive bounds or a circle. */
export type SavedRegion = {
  id: number;
  datasetId: number;
  name: string;
  shape: { shape: "rect" | "circle"; x0: number; y0: number; x1: number; y1: number; cx: number; cy: number; r: number };
  createdAt: string;
  updatedAt: string;
};

export const toShape = (r: SavedRegion): RegionShape =>
  r.shape.shape === "rect" ? { shape: "rect", x0: r.shape.x0, y0: r.shape.y0, x1: r.shape.x1, y1: r.shape.y1 } : { shape: "circle", cx: r.shape.cx, cy: r.shape.cy, r: r.shape.r };

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

export const listSavedRegions = (datasetId: number) => call<{ regions: SavedRegion[] }>(`/api/datasets/${datasetId}/regions`).then((r) => r.regions);
export const createSavedRegion = (datasetId: number, name: string, shape: RegionShape) => post<SavedRegion>(`/api/datasets/${datasetId}/regions`, { name, shape });
export const updateSavedRegion = (id: number, change: { name?: string; shape?: RegionShape }) => post<SavedRegion>(`/api/regions/${id}`, change);
export const deleteSavedRegion = (id: number) => post<{ deleted: boolean }>(`/api/regions/${id}/delete`, {});
export const ds9Url = (datasetId: number, id?: number) => `/api/datasets/${datasetId}/regions.reg${id ? `?id=${id}` : ""}`;
