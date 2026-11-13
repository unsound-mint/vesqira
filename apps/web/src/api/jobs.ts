import { request as call, postJSON as post } from "./http";
import type { components } from "./schema";
import type { Run, RerunCheck, RerunMode } from "./runs";

/** What the scheduler recorded a finished job used (ADR 0045). null is "not reported", never 0. */
export type JobUsage = {
  state: string;
  exitCode: string;
  elapsedSeconds: number;
  timelimitSeconds: number | null;
  reqMemBytes: number | null;
  maxRssBytes: number | null;
  allocCpus: number | null;
  totalCpuSeconds: number | null;
  cpuEfficiency: number | null;
  /** TotalCPU is about zero over a long run: the work ran where Slurm does not account CPU. */
  cpuNotAccounted: boolean;
  oomKilled: boolean;
  suggest: UsageSuggestion | null;
  /** sacct columns this site's sacct does not report (absent on usage stored before this was recorded). */
  unsupported?: string[];
};
/** A request sized from one job's measured use. It is shown, never applied by itself. */
export type UsageSuggestion = { mem: string; time: string; memBytes: number; timeSeconds: number; basis: string };

export type JobState = "draft" | "submitting" | "submitted" | "running" | "completed" | "failed" | "cancelled";

export type Job = {
  id: number;
  hostId: number;
  name: string;
  /** "vgt" and "rohsa" are the templates Vesqira ships; any other id names a template a tool profile declares (GET /api/job-templates). */
  template: "vgt" | "rohsa" | "custom" | "analysis" | (string & {});
  script: string;
  scriptHash: string;
  state: JobState;
  detail: string;
  slurmId: string;
  workDir: string;
  outputDir: string;
  createdAt: string;
  submittedAt: string | null;
  finishedAt: string | null;
  runId: number | null;
  parentRunId: number | null;
  /** Why the owner made this job, or an empty string. */
  note: string;
  /** Import the results as a run by itself when the job completes (VGT jobs only); fixed once the job is submitted. */
  autoImport: boolean;
  /** What the scheduler said the job used, once it has ended; null before that or when the end was not read from sacct. */
  usage: JobUsage | null;
};

export type RohsaJobRequest = {
  hostId: number;
  template: "rohsa";
  name: string;
  /** Why, kept with the job (at most 500 characters). */
  note?: string;
  /** The run this draft repeats with changes. */
  parentRunId?: number;
  /** How closely it repeats it (ADR 0054); adapted when left out. */
  rerunMode?: RerunMode;
  /** The config is read from configPath on the host unless configText is given (a rerun passes the text the parent started from). set holds only the settings that change; the output folder is never one of them. */
  rohsa: { repoDir: string; configPath: string; configText?: string; set?: Record<string, string>; cpus: number; mem: string; time: string; partition: string; account: string };
};

/** What the owner's sizing table (the pipeline's submit_rohsa.sh) suggests for a crop and a Gaussian count. A suggestion: nothing applies it. */
export type RohsaSizing = components["schemas"]["RohsaSizing"];

export type VgtJobRequest = {
  hostId: number;
  template: "vgt";
  name: string;
  /** Why, kept with the job (at most 500 characters). */
  note?: string;
  /** The run this draft repeats with changes. */
  parentRunId?: number;
  /** How closely it repeats it (ADR 0054); adapted when left out. */
  rerunMode?: RerunMode;
  /** With configText the host's file is not read again: a rerun passes the config text the parent started from. */
  vgt: { repoDir: string; configPath: string; configText?: string; set: Record<string, string>; cpus: number; mem: string; time: string; partition: string; account: string };
};

/** How a Slurm job of a tool is written, as its profile declares it: the keys a person may change with their types and ranges, the preparation, the command, the pinned commit. */
export type JobTemplate = components["schemas"]["JobTemplate"];
export type TemplateKey = components["schemas"]["JobTemplateKey"];
export type TemplateStep = components["schemas"]["JobTemplateStep"];

/** A job draft for any template: the same body whatever the tool. The server checks every setting against the template and writes the script. */
export type ToolJobRequest = {
  hostId: number;
  template: string;
  name: string;
  note?: string;
  /** A draft that repeats a finished run, and how closely (the server checks the mode against what the run observed). */
  parentRunId?: number;
  rerunMode?: "strict" | "best_effort" | "adapted";
  tool: { repoDir: string; configPath: string; configText?: string; set?: Record<string, string>; cpus: number; mem: string; time: string; partition: string; account: string; pinProfileCommit?: boolean };
};

export const rohsaSizing = (cropSize: string, nGauss: string) => {
  const q = new URLSearchParams();
  if (/^\d+$/.test(cropSize.trim())) q.set("cropSize", cropSize.trim());
  if (/^\d+$/.test(nGauss.trim())) q.set("nGauss", nGauss.trim());
  const qs = q.toString();
  return call<RohsaSizing>(`/api/rohsa/sizing${qs ? `?${qs}` : ""}`);
};
export const listJobs = () => call<{ jobs: Job[] }>("/api/jobs").then((r) => r.jobs);
export const listJobTemplates = () => call<{ templates: JobTemplate[] }>("/api/job-templates").then((r) => (Array.isArray(r?.templates) ? r.templates : []));
export const getJob = (id: number) => call<Job>(`/api/jobs/${id}`);
export const createJob = (r: VgtJobRequest | RohsaJobRequest | ToolJobRequest) => post<Job>("/api/jobs", r);
export const editScript = (id: number, script: string) => post<Job>(`/api/jobs/${id}/script`, { script });
export const submitJob = (id: number, scriptHash: string, autoImport?: boolean) => post<Job>(`/api/jobs/${id}/submit`, autoImport === undefined ? { scriptHash } : { scriptHash, autoImport });
export const refreshJob = (id: number) => post<Job>(`/api/jobs/${id}/refresh`, {});
export const reopenJob = (id: number) => post<Job>(`/api/jobs/${id}/reopen`, {});
export const cancelJob = (id: number) => post<Job>(`/api/jobs/${id}/cancel`, {});
/** What can be said about a rerun draft before it is submitted; the host is only asked for strict and best effort drafts. */
export const rerunCheck = (id: number) => call<RerunCheck>(`/api/jobs/${id}/rerun-check`);
export const jobLog = (id: number) => call<{ log: string }>(`/api/jobs/${id}/log`).then((r) => r.log);
export const registerOutputs = (id: number) => post<Run>(`/api/jobs/${id}/register`, {});
export const hostText = (hostId: number, path: string) => call<{ text: string }>(`/api/hosts/${hostId}/text?path=${encodeURIComponent(path)}`).then((r) => r.text);

/** Values of the settings the job form edits, read from a config's text so the form starts from the real configuration. */
export function readSetting(text: string, key: string): string {
  const m = new RegExp(`^\\s*${key}\\s*=\\s*(.+?)\\s*(#.*)?$`, "m").exec(text);
  return m ? m[1]!.replace(/^"(.*)"$/, "$1") : "";
}
