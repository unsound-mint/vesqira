import { ApiError } from "./catalog";
import type { Run } from "./runs";

export type JobState = "draft" | "submitted" | "running" | "completed" | "failed" | "cancelled";

export type Job = {
  id: number;
  hostId: number;
  name: string;
  template: "vgt" | "custom" | "analysis";
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
};

export type VgtJobRequest = {
  hostId: number;
  template: "vgt";
  name: string;
  vgt: { repoDir: string; configPath: string; set: Record<string, string>; cpus: number; mem: string; time: string; partition: string; account: string };
};

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export const listJobs = () => call<{ jobs: Job[] }>("/api/jobs").then((r) => r.jobs);
export const createJob = (r: VgtJobRequest) => post<Job>("/api/jobs", r);
export const editScript = (id: number, script: string) => post<Job>(`/api/jobs/${id}/script`, { script });
export const submitJob = (id: number, scriptHash: string) => post<Job>(`/api/jobs/${id}/submit`, { scriptHash });
export const refreshJob = (id: number) => post<Job>(`/api/jobs/${id}/refresh`, {});
export const cancelJob = (id: number) => post<Job>(`/api/jobs/${id}/cancel`, {});
export const jobLog = (id: number) => call<{ log: string }>(`/api/jobs/${id}/log`).then((r) => r.log);
export const registerOutputs = (id: number) => post<Run>(`/api/jobs/${id}/register`, {});
export const hostText = (hostId: number, path: string) => call<{ text: string }>(`/api/hosts/${hostId}/text?path=${encodeURIComponent(path)}`).then((r) => r.text);

/** Values of the settings the job form edits, read from a config's text so the form starts from the real configuration. */
export function readSetting(text: string, key: string): string {
  const m = new RegExp(`^\\s*${key}\\s*=\\s*(.+?)\\s*(#.*)?$`, "m").exec(text);
  return m ? m[1]!.replace(/^"(.*)"$/, "$1") : "";
}
