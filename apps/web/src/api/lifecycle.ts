import { ApiError, request } from "./http";

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

/** A refused delete (409) carries the dependents as they are now. */
async function call<T>(url: string, init?: RequestInit): Promise<T> {
  try {
    return await request<T>(url, init);
  } catch (e) {
    const b = e instanceof ApiError && e.status === 409 ? (e.body as { impact?: Impact } | null) : null;
    if (b?.impact) throw new ImpactChanged((e as ApiError).message || "What depends on this changed.", b.impact);
    throw e;
  }
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
