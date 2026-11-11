import { ApiError } from "./catalog";

/** What can be archived or deleted from the catalog (ADR 0041). The names are the route segments. */
export type EntryKind = "datasets" | "runs" | "figures" | "boards";

export type ImpactItem = { id: number; label: string };
/** `stays` is kept as it is, `unlinked` is kept but no longer connected, `removed` goes with the entry, `blocks` refuses the delete. */
export type ImpactEffect = "stays" | "unlinked" | "removed" | "blocks";
export type ImpactGroup = { key: string; effect: ImpactEffect; text: string; count: number; items: ImpactItem[] };
export type OwnedBytes = { kind: string; name: string; sizeBytes: number };
export type Impact = {
  kind: string;
  id: number;
  name: string;
  archived: boolean;
  groups: ImpactGroup[];
  blocked: boolean;
  blockers: string[];
  digest: string;
  fileNote: string;
  ownedBytes: OwnedBytes | null;
};

/** The dependents changed between the preview and the confirmation; `impact` is how they are now. */
export class ImpactChanged extends ApiError {
  constructor(message: string, readonly impact: Impact) {
    super(message);
  }
}

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string; impact?: Impact } | null;
  if (res.status === 409 && body?.impact) throw new ImpactChanged(body.error ?? "What depends on this changed.", body.impact);
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

type Verb = "impact" | "archive" | "restore" | "delete";
// Written out, not assembled from the kind, so tools/contracts.py can check each against the routes the server serves.
const PATH: Record<EntryKind, Record<Verb, (id: number) => string>> = {
  datasets: { impact: (id) => `/api/datasets/${id}/impact`, archive: (id) => `/api/datasets/${id}/archive`, restore: (id) => `/api/datasets/${id}/restore`, delete: (id) => `/api/datasets/${id}/delete` },
  runs: { impact: (id) => `/api/runs/${id}/impact`, archive: (id) => `/api/runs/${id}/archive`, restore: (id) => `/api/runs/${id}/restore`, delete: (id) => `/api/runs/${id}/delete` },
  figures: { impact: (id) => `/api/figures/${id}/impact`, archive: (id) => `/api/figures/${id}/archive`, restore: (id) => `/api/figures/${id}/restore`, delete: (id) => `/api/figures/${id}/delete` },
  boards: { impact: (id) => `/api/boards/${id}/impact`, archive: (id) => `/api/boards/${id}/archive`, restore: (id) => `/api/boards/${id}/restore`, delete: (id) => `/api/boards/${id}/delete` },
};

export const getImpact = (kind: EntryKind, id: number) => call<Impact>(PATH[kind].impact(id));
export const archiveEntry = (kind: EntryKind, id: number) => post<{ archived: boolean }>(PATH[kind].archive(id), {});
export const restoreEntry = (kind: EntryKind, id: number) => post<{ archived: boolean }>(PATH[kind].restore(id), {});
/** Forgets the entry. `impactDigest` is the digest of the preview the person saw; a changed impact answers with ImpactChanged. */
export const deleteEntry = (kind: EntryKind, id: number, body: { impactDigest: string; ownedBytes?: "keep" | "remove" }) =>
  post<{ deleted: boolean; ownedBytes: "none" | "kept" | "removed" | "failed" }>(PATH[kind].delete(id), body);
