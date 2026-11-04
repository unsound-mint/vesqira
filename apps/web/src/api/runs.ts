import { ApiError } from "./catalog";

export type RunParam = { key: string; value: string; source: "config" | "run_parameters" };
export type RunOutput = { role: "psi" | "reliability" | "support" | "fit_rbar2"; datasetId: number; name: string };

export type RunReview = { qc: "not_assessed" | "passed" | "failed" | "inconclusive"; interpretation: "not_interpreted" | "supports" | "challenges" | "inconclusive"; note: string };

export type RunFact = { key: string; label: string; status: "declared" | "inferred" | "unresolved"; detail: string };

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
  inputHint: { datasetId: number; basis: string } | null;
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

export const listRuns = () => call<{ runs: Run[] }>("/api/runs").then((r) => r.runs);
export const importRun = (path: string) =>
  call<Run>("/api/runs/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });

export type RunReviewRevision = RunReview & { at: string };
export const runReviewHistory = (id: number) => call<{ history: RunReviewRevision[] }>(`/api/runs/${id}/history`).then((r) => r.history);

export const setRunReview = (id: number, rv: RunReview) =>
  call<Run>(`/api/runs/${id}/review`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(rv) });
