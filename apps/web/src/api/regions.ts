import { request as call, postJSON as post } from "./http";
import type { LineShape, RegionShape } from "./planes";

/** A named region saved with a dataset: whole 0-based pixels, a rectangle with inclusive bounds, a circle or a line (a path with a width). */
export type SavedRegion = {
  id: number;
  datasetId: number;
  name: string;
  shape: { shape: "rect" | "circle" | "line"; x0: number; y0: number; x1: number; y1: number; cx: number; cy: number; r: number; points?: number[][]; width?: number; widthUnit?: "pixel" | "arcsec" };
  createdAt: string;
  updatedAt: string;
};

export const isLine = (r: SavedRegion): boolean => r.shape.shape === "line";

/** The area of a saved rectangle or circle. A line has none: use `savedShape` or `toLine`. */
export const toShape = (r: SavedRegion): RegionShape =>
  r.shape.shape === "rect" ? { shape: "rect", x0: r.shape.x0, y0: r.shape.y0, x1: r.shape.x1, y1: r.shape.y1 } : { shape: "circle", cx: r.shape.cx, cy: r.shape.cy, r: r.shape.r };

export const toLine = (r: SavedRegion): LineShape => ({
  shape: "line",
  points: (r.shape.points ?? []).map((p) => [p[0]!, p[1]!] as [number, number]),
  width: r.shape.width ?? 1,
  widthUnit: r.shape.widthUnit ?? "pixel",
});

/** Whatever shape it is, to save it again or draw it. */
export const savedShape = (r: SavedRegion): RegionShape | LineShape => (isLine(r) ? toLine(r) : toShape(r));

export const listSavedRegions = (datasetId: number) => call<{ regions: SavedRegion[] }>(`/api/datasets/${datasetId}/regions`).then((r) => r.regions);
export const createSavedRegion = (datasetId: number, name: string, shape: RegionShape | LineShape) => post<SavedRegion>(`/api/datasets/${datasetId}/regions`, { name, shape });
export const updateSavedRegion = (id: number, change: { name?: string; shape?: RegionShape | LineShape }) => post<SavedRegion>(`/api/regions/${id}`, change);
export const deleteSavedRegion = (id: number) => post<{ deleted: boolean }>(`/api/regions/${id}/delete`, {});
export const ds9Url = (datasetId: number, id?: number) => `/api/datasets/${datasetId}/regions.reg${id ? `?id=${id}` : ""}`;

export type ImportItem = { line: number; text: string; status: "exact" | "approximated" | "skipped"; name?: string; shape?: SavedRegion["shape"]; note?: string };
export const previewDs9 = (datasetId: number, text: string) => post<{ items: ImportItem[] }>(`/api/datasets/${datasetId}/regions/import`, { text }).then((r) => r.items);
export const applyDs9 = (datasetId: number, text: string, includeApproximated: boolean) =>
  post<{ items: ImportItem[]; created: SavedRegion[]; failed: string[] }>(`/api/datasets/${datasetId}/regions/import`, { text, apply: true, includeApproximated });
