import { useSyncExternalStore } from "react";

/**
 * What the viewer last learned about each dataset's answers: whether the last one came from an offline copy (ADR 0059), and
 * when a request for something the copy does not hold was refused. It imports nothing but React, so http.ts can feed it.
 */

export type OfflineCopy = { takenAt: string; signature: { hostId: number; size: number; mtimeNs: number; identity: string } };
export type OfflineView = { copy: OfflineCopy | null; missing: string | null };

const NONE: OfflineView = { copy: null, missing: null };
const states = new Map<number, OfflineView>();
const listeners = new Set<() => void>();

function set(id: number, next: OfflineView) {
  const cur = states.get(id) ?? NONE;
  if (cur.copy === next.copy && cur.missing === next.missing) return;
  if (next.copy === null && next.missing === null) states.delete(id);
  else states.set(id, next);
  for (const l of [...listeners]) l();
}

/** The four routes that may answer from an offline copy, and the dataset they are for. */
const ROUTE = /^\/api\/datasets\/(\d+)\/(?:view|plane|spectrum|header-cards)(?:\?|$)/;

function parseMark(h: string | null): OfflineCopy | null {
  if (!h) return null;
  try {
    const m = JSON.parse(h) as { copy?: boolean; takenAt?: string; signature?: OfflineCopy["signature"] };
    return m.copy === true && typeof m.takenAt === "string" && m.signature ? { takenAt: m.takenAt, signature: m.signature } : null;
  } catch {
    return null;
  }
}

/** Called for every response of requestRaw: a copy marker sets the dataset's state, a live answer clears it. */
export function observeResponse(url: string, res: Response) {
  const m = ROUTE.exec(url);
  if (!m) return;
  const id = Number(m[1]);
  if (res.ok) {
    const copy = parseMark(res.headers.get("X-Vesqira-Offline"));
    const cur = states.get(id) ?? NONE;
    // an answer from the copy keeps what was said about a missing piece; a live answer ends the offline state altogether
    set(id, copy ? { copy: sameCopy(cur.copy, copy) ? cur.copy : copy, missing: cur.missing } : NONE);
  }
}

const sameCopy = (a: OfflineCopy | null, b: OfflineCopy) => a !== null && a.takenAt === b.takenAt && a.signature.size === b.signature.size && a.signature.mtimeNs === b.signature.mtimeNs;

/** Called with a failed request's server body: a refusal that names what the offline copy does not hold. */
export function observeFailure(url: string, status: number, body: unknown) {
  const m = ROUTE.exec(url);
  if (!m || status !== 503) return;
  const o = body as { code?: unknown; error?: unknown } | null;
  if (o?.code !== "offline_missing" || typeof o.error !== "string") return;
  const id = Number(m[1]);
  const cur = states.get(id) ?? NONE;
  set(id, { copy: cur.copy, missing: o.error });
}

/** A refusal that says the offline copy does not hold what was asked for (503 offline_missing). */
export function isOfflineMissing(e: unknown): boolean {
  const x = e as { status?: number; body?: { code?: string } | null } | null;
  return x?.status === 503 && x.body?.code === "offline_missing";
}

export function useOfflineView(datasetId: number | null): OfflineView {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => void listeners.delete(cb);
    },
    () => (datasetId === null ? NONE : (states.get(datasetId) ?? NONE)),
    () => NONE,
  );
}

/** Tests start from nothing. */
export function resetOfflineState() {
  states.clear();
  for (const l of [...listeners]) l();
}
