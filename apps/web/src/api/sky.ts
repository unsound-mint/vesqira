import { getJSON } from "./http";
import type { components } from "./schema";

export type SkyFrame = "icrs" | "galactic";
export type SkyFootprint = components["schemas"]["SkyFootprint"];
export type SkyPoint = components["schemas"]["SkyPoint"];

/** Where a dataset's image lies on the sky, in the panel's frame (ADR 0072): the worker's astropy WCS, corrections applied. */
export const getSkyFootprint = (id: number, frame: SkyFrame, signal?: AbortSignal) => getJSON<SkyFootprint>(`/api/datasets/${id}/sky-footprint?frame=${frame}`, signal);

/** Where one pixel is on the sky (the viewer's cursor), in the panel's frame. World coordinates, never pixels. */
export const getSkyPoint = (id: number, frame: SkyFrame, x: number, y: number, signal?: AbortSignal) => getJSON<SkyPoint>(`/api/datasets/${id}/sky-point?frame=${frame}&x=${x}&y=${y}`, signal);
