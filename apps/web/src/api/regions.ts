import { request as call, postJSON as post } from "./http";
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

export const listSavedRegions = (datasetId: number) => call<{ regions: SavedRegion[] }>(`/api/datasets/${datasetId}/regions`).then((r) => r.regions);
export const createSavedRegion = (datasetId: number, name: string, shape: RegionShape) => post<SavedRegion>(`/api/datasets/${datasetId}/regions`, { name, shape });
export const updateSavedRegion = (id: number, change: { name?: string; shape?: RegionShape }) => post<SavedRegion>(`/api/regions/${id}`, change);
export const deleteSavedRegion = (id: number) => post<{ deleted: boolean }>(`/api/regions/${id}/delete`, {});
export const ds9Url = (datasetId: number, id?: number) => `/api/datasets/${datasetId}/regions.reg${id ? `?id=${id}` : ""}`;

export type ImportItem = { line: number; text: string; status: "exact" | "approximated" | "skipped"; name?: string; shape?: SavedRegion["shape"]; note?: string };
export const previewDs9 = (datasetId: number, text: string) => post<{ items: ImportItem[] }>(`/api/datasets/${datasetId}/regions/import`, { text }).then((r) => r.items);
export const applyDs9 = (datasetId: number, text: string, includeApproximated: boolean) =>
  post<{ items: ImportItem[]; created: SavedRegion[]; failed: string[] }>(`/api/datasets/${datasetId}/regions/import`, { text, apply: true, includeApproximated });
