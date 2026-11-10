/** The reader's client for the publish server (ADR 0030). It knows only the /read-api surface, one token in the path. */
import type { DocFigure, RunSummary } from "../reports/Document";

export type ReportSummary = { key: string; latest: number; title: string; publishedAt: string; date?: string; summary?: string };
export type SpaceView = { reader: string; title: string; reports: ReportSummary[]; lastSync?: string; comments: number };

export type ReaderBlock = { id: string; kind: "text" | "figure" | "run"; text?: string; figure?: { asset: string; width: number; height: number; title: string; caption: string; provenance: DocFigure["provenance"] }; run?: RunSummary | null; shared?: string[] };
export type ReaderRevision = { id: string; report: string; number: number; title: string; publishedAt: string; authors: string; summary?: string; date?: string; blocks: ReaderBlock[] };
export type RevisionRef = { number: number; publishedAt: string };

export type ReaderComment = { id: number; report: string; revision: string; block: string; parentId: number; author: string; fromOwner: boolean; body: string; createdAt: string; resolvedAt?: string; quote?: string; mine?: boolean };
export type ReportView = { revision: ReaderRevision; revisions: RevisionRef[]; comments: ReaderComment[] };

/** notfound is the same for a bad, revoked or expired link, so the page can say nothing more. */
export class ReadError extends Error {
  constructor(public kind: "notfound" | "limited" | "network" | "other", message: string) {
    super(message);
  }
}

const base = (token: string) => `/read-api/${encodeURIComponent(token)}`;

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ReadError("network", "Could not reach the server.");
  }
  if (res.status === 404) throw new ReadError("notfound", "Not found.");
  if (res.status === 429) throw new ReadError("limited", "Too many attempts.");
  if (!res.ok) throw new ReadError("other", (await res.text().catch(() => "")).trim() || `The server answered ${res.status}.`);
  return (await res.json()) as T;
}

export const getSpace = (token: string) => call<SpaceView>(base(token));
export const getReport = (token: string, key: string, rev?: number) => call<ReportView>(`${base(token)}/reports/${encodeURIComponent(key)}${rev ? `?rev=${rev}` : ""}`);
export const assetUrl = (token: string, sha: string) => `${base(token)}/assets/${encodeURIComponent(sha)}`;

/** The author is set by the server to the link's name; it is never sent. */
export const postComment = (token: string, c: { report: string; revision: string; block: string; parentId?: number; body: string; quote?: string }) =>
  call<ReaderComment>(`${base(token)}/comments`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });

/** Only a comment posted through this same link, and only while nobody has answered it. */
export async function deleteComment(token: string, id: number): Promise<void> {
  let res: Response;
  try {
    res = await fetch(`${base(token)}/comments/${id}`, { method: "DELETE" });
  } catch {
    throw new ReadError("network", "Could not reach the server.");
  }
  if (res.status === 404) throw new ReadError("notfound", "Not found.");
  if (!res.ok) throw new ReadError("other", (await res.text().catch(() => "")).trim() || `The server answered ${res.status}.`);
}
