import { postJSON, request as call } from "./http";
import type { Job, JobUsage } from "./jobs";

export type RunParam = { key: string; value: string; source: "config" | "run_parameters" | "parameters" | "run_trace" };
/** psi, reliability, support, fit_rbar2 for a VGT run; the product name (component_g1_mu_kms, cube_model, ...) for a ROHSA run. */
export type RunOutput = { role: string; datasetId: number; name: string };

export type RunReview = { qc: "not_assessed" | "passed" | "failed" | "inconclusive"; interpretation: "not_interpreted" | "supports" | "challenges" | "inconclusive"; note: string };

/** verified: observed by the run's own job (ADR 0054); declared: the run's metadata says so, not checked; inferred: a weak rule matched; unresolved: nothing records it. */
export type RunFact = { key: string; label: string; status: "verified" | "declared" | "inferred" | "unresolved"; detail: string };

/** One thing a job observed about where it ran. Unresolved says why it could not be read; value is empty then. */
export type EnvFact = { key: string; label: string; status: "verified" | "unresolved"; value: string; reason: string };

/** A fact compared between two runs. unknown means a side did not observe it: never "matched". */
export type EnvDifference = components["schemas"]["EnvDifference"];
export type RerunMode = "strict" | "best_effort" | "adapted";
export type ParentComparison = { parentRunId: number; mode: RerunMode | ""; parentObserved: boolean; thisObserved: boolean; facts: EnvDifference[] };

/** Smoothing, noise model and control construction stated for a result; a null block was not stated (not "none applied"). */
export type Controls = components["schemas"]["Controls"];
export type ControlsDeclaration = components["schemas"]["ControlsDeclaration"];
export type RerunCheck = components["schemas"]["RerunCheck"];

import type { components } from "./schema";
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
  /** What the job observed about where it ran (ADR 0054); empty for a run no job of this catalog made. */
  environment?: EnvFact[];
  /** Smoothing, noise model and control construction stated for the run; null when none was stated. */
  controls?: Controls | null;
  /** How the draft repeated its parent run, and what both runs observed; null for a run that repeats nothing. */
  rerunMode?: RerunMode | null;
  parentComparison?: ParentComparison | null;
  /** The configuration the run's job was rendered from, apart from its input and output paths (a SHA-256, hex); runs that share it ran the same configuration. Null when the job kept no settings. */
  configHash?: string | null;
  /** Set for a run made with "run this configuration on another cube": the run it repeats and the input as the catalog knew it. */
  inputRerun?: InputRerunRecord | null;
};

export type RerunOnInputSummary = components["schemas"]["RerunOnInputSummary"];
export type RerunInputIdentity = components["schemas"]["RerunInputIdentity"];
export type InputRerunRecord = {
  sourceRunId: number;
  sourceConfigHash: string;
  configHash: string;
  /** The recorded hashes agree: it is the same configuration as the source run. */
  sameConfiguration: boolean;
  previousInput: string;
  input: RerunInputIdentity;
  notes: string[];
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
export const importRun = (path: string, hostId?: number) =>
  call<Run>("/api/runs/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(hostId ? { path, hostId } : { path }) });

export type RunReviewRevision = RunReview & { at: string };
export const runReviewHistory = (id: number) => call<{ history: RunReviewRevision[] }>(`/api/runs/${id}/history`).then((r) => r.history);

export const setRunReview = (id: number, rv: RunReview) =>
  call<Run>(`/api/runs/${id}/review`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(rv) });

export const getRun = (id: number) => call<Run>(`/api/runs/${id}`);
export const getRerun = (id: number) => call<Rerun>(`/api/runs/${id}/rerun`);

export const setRunControls = (id: number, c: ControlsDeclaration) =>
  call<Run>(`/api/runs/${id}/controls`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });

/** What running this run's recorded configuration on another cube would carry over and change; refused (an Error with the sentence) when it cannot be done. Stores nothing. */
export const previewRerunOnInput = (runId: number, datasetId: number) => call<RerunOnInputSummary>(`/api/runs/${runId}/rerun-on-input?datasetId=${datasetId}`);
/** Makes the draft job (nothing is submitted). */
export const createRerunOnInput = (runId: number, datasetId: number) => postJSON<{ job: Job; summary: RerunOnInputSummary }>(`/api/runs/${runId}/rerun-on-input`, { datasetId });
