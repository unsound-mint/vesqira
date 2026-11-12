import { postJSON as post } from "./http";
import type { components } from "./schema";

export type ConvertRunValuesRequest = components["schemas"]["ConvertRunValuesRequest"];
export type ConvertRunValuesResponse = components["schemas"]["ConvertRunValuesResponse"];

/**
 * Express numbers a run declared in another unit, by the worker's plain unit conversion (ADR 0062). A conversion that would need a
 * beam, a frame or a spectral axis comes back `refused` with a sentence; nothing is converted by the browser.
 */
export type ConvertProbeAnswer = NonNullable<ConvertRunValuesResponse["pairs"]>[number];

/**
 * Which (recorded unit, target unit) pairs the worker would convert, asked in one request and answered without any value (the dry
 * run of the same route, ADR 0062). Each pair is answered alone: a refusal carries the reason in words.
 */
export async function probeRunConversions(pairs: { from: string; to: string }[]): Promise<ConvertProbeAnswer[]> {
  const res = await post<ConvertRunValuesResponse>("/api/runs/convert-values", { pairs });
  return res.pairs ?? [];
}

export const convertRunValues = (to: string, values: NonNullable<ConvertRunValuesRequest["values"]>) => post<ConvertRunValuesResponse>("/api/runs/convert-values", { to, values });
