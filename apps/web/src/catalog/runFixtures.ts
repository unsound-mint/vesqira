import type { Run } from "../api/runs";
import type { Metric } from "../api/metrics";

/** Builders for the run tests (not imported by the app): a run with parameters and results, and a result with unit and uncertainty. */
export const metric = (key: string, value: number | null, over: Partial<Metric> = {}): Metric => ({ key, value, unit: "", uncertainty: null, uncertaintyKind: "", n: null, definition: "", status: "declared", ...over });

export const run = (id: number, params: Record<string, string> = {}, over: Partial<Run> = {}): Run =>
  ({
    id,
    name: `lmc_${id}`,
    kind: "vgt",
    importedAt: `2026-08-${String(id).padStart(2, "0")}T10:00:00Z`,
    generatedAt: `2026-07-${String(id).padStart(2, "0")}T09:00:00Z`,
    gitCommit: "abc",
    inputName: "hnv1.fits",
    jobId: null,
    params: Object.entries(params).map(([key, value]) => ({ key, value, source: "config" })),
    metrics: [],
    review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" },
    ...over,
  }) as Run;
