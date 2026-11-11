import { request as call } from "./http";
import type { JobUsage } from "./jobs";

export type RunParam = { key: string; value: string; source: "config" | "run_parameters" | "parameters" | "run_trace" };
/** psi, reliability, support, fit_rbar2 for a VGT run; the product name (component_g1_mu_kms, cube_model, ...) for a ROHSA run. */
export type RunOutput = { role: string; datasetId: number; name: string };

export type RunReview = { qc: "not_assessed" | "passed" | "failed" | "inconclusive"; interpretation: "not_interpreted" | "supports" | "challenges" | "inconclusive"; note: string };

export type RunFact = { key: string; label: string; status: "declared" | "inferred" | "unresolved"; detail: string };

import type { Metric } from "./metrics";
import type { RohsaRecord } from "./rohsa";

export type Run = {
  review: RunReview;
  reproducibility?: RunFact[];
  id: number;
  name: string;
  kind: string;
  importedAt: string;
  generatedAt: string;
  gitCommit: string;
  dirty: boolean;
  reproducibleFromCommit: boolean;
  elapsedSeconds: number | null;
  params: RunParam[];
  diagnostics: Record<string, string>;
  inputName: string;
  inputSizeBytes: number;
  outputs: RunOutput[];
  metrics: Metric[];
  inputHint: { datasetId: number; basis: string } | null;
  /** The Vesqira job whose outputs this run is, or null (imported runs). */
  jobId: number | null;
  /** The run that job was drafted from with "run again with...", or null. */
  parentRunId: number | null;
  /** Archived runs are hidden from lists by default but still resolve (ADR 0041). */
  archived?: boolean;
  /** The decomposition record of a ROHSA run (ADR 0046); null for other kinds. */
  rohsa?: RohsaRecord | null;
};

export type RerunRequest = {
  repoDir: string | null;
  configPath: string | null;
  baseConfigText: string | null;
  set: Record<string, string>;
  cpus: number | null;
  mem: string | null;
  time: string | null;
  partition: string | null;
  account: string | null;
};

/** The settings a new VGT or ROHSA draft starts from. A null is unknown, never a default: imported runs do not say how they ran. */
export type Rerun = {
  parentRunId: number;
  source: "job" | "imported";
  hostId: number | null;
  template: "vgt" | "rohsa";
  name: string;
  vgt: RerunRequest;
  /** The same record for a ROHSA run; null for a VGT run. */
  rohsa?: RerunRequest | null;
  /** ROHSA only: the editable settings as they were, as a person types them (the config's own values with the job's changes over them, or what an imported run's trace declares). A setting the records do not give is absent. */
  settings?: Record<string, string>;
  params: RunParam[];
  notes: string[];
  /** The stored config text compared with the file on the host now; null when unknown or the host cannot be reached. */
  configChanged: boolean | null;
  /** The job that produced the run and what it used, for sizing the next request; null for an imported run. */
  lastJob: { id: number; name: string; usage: JobUsage | null } | null;
};

export const listRuns = (opts: { archived?: "all" | "only" } = {}) => call<{ runs: Run[] }>(`/api/runs${opts.archived ? `?archived=${opts.archived}` : ""}`).then((r) => r.runs);
export const importRun = (path: string) =>
  call<Run>("/api/runs/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });

export type RunReviewRevision = RunReview & { at: string };
export const runReviewHistory = (id: number) => call<{ history: RunReviewRevision[] }>(`/api/runs/${id}/history`).then((r) => r.history);

export const setRunReview = (id: number, rv: RunReview) =>
  call<Run>(`/api/runs/${id}/review`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(rv) });

export const getRerun = (id: number) => call<Rerun>(`/api/runs/${id}/rerun`);
