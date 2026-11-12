import { postJSON as post } from "./http";
import type { components } from "./schema";

export type ConvertRunValuesRequest = components["schemas"]["ConvertRunValuesRequest"];
export type ConvertRunValuesResponse = components["schemas"]["ConvertRunValuesResponse"];

/**
 * Express numbers a run declared in another unit, by the worker's plain unit conversion (ADR 0062). A conversion that would need a
 * beam, a frame or a spectral axis comes back `refused` with a sentence; nothing is converted by the browser.
 */
export const convertRunValues = (to: string, values: ConvertRunValuesRequest["values"]) => post<ConvertRunValuesResponse>("/api/runs/convert-values", { to, values });
