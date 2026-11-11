import { request as call, postJSON as post } from "./http";
import type { Run } from "./runs";

/** What the scheduler recorded a finished job used (ADR 0044). null is "not reported", never 0. */
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
  template: "vgt" | "rohsa" | "custom" | "analysis";
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
  rohsa: { repoDir: string; configPath: string; cpus: number; mem: string; time: string; partition: string; account: string };
};

export type VgtJobRequest = {
  hostId: number;
  template: "vgt";
  name: string;
  /** Why, kept with the job (at most 500 characters). */
  note?: string;
  /** The run this draft repeats with changes. */
  parentRunId?: number;
  /** With configText the host's file is not read again: a rerun passes the config text the parent started from. */
  vgt: { repoDir: string; configPath: string; configText?: string; set: Record<string, string>; cpus: number; mem: string; time: string; partition: string; account: string };
};

export const listJobs = () => call<{ jobs: Job[] }>("/api/jobs").then((r) => r.jobs);
export const getJob = (id: number) => call<Job>(`/api/jobs/${id}`);
export const createJob = (r: VgtJobRequest | RohsaJobRequest) => post<Job>("/api/jobs", r);
export const editScript = (id: number, script: string) => post<Job>(`/api/jobs/${id}/script`, { script });
export const submitJob = (id: number, scriptHash: string, autoImport?: boolean) => post<Job>(`/api/jobs/${id}/submit`, autoImport === undefined ? { scriptHash } : { scriptHash, autoImport });
export const refreshJob = (id: number) => post<Job>(`/api/jobs/${id}/refresh`, {});
export const reopenJob = (id: number) => post<Job>(`/api/jobs/${id}/reopen`, {});
export const cancelJob = (id: number) => post<Job>(`/api/jobs/${id}/cancel`, {});
export const jobLog = (id: number) => call<{ log: string }>(`/api/jobs/${id}/log`).then((r) => r.log);
export const registerOutputs = (id: number) => post<Run>(`/api/jobs/${id}/register`, {});
export const hostText = (hostId: number, path: string) => call<{ text: string }>(`/api/hosts/${hostId}/text?path=${encodeURIComponent(path)}`).then((r) => r.text);

/** Values of the settings the job form edits, read from a config's text so the form starts from the real configuration. */
export function readSetting(text: string, key: string): string {
  const m = new RegExp(`^\\s*${key}\\s*=\\s*(.+?)\\s*(#.*)?$`, "m").exec(text);
  return m ? m[1]!.replace(/^"(.*)"$/, "$1") : "";
}
