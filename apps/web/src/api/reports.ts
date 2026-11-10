import { ApiError } from "./catalog";

/** Owner-side reports (ADR 0036). The reader page never imports this file. */

export type ReportSummary = { id: number; key: string; title: string; revision: number; updatedAt: string; lastAuthor: "owner" | "agent"; agentUnseen: boolean; published: number; publishedFrom: number; parent: number; date?: string; summary?: string };
export type Report = { id: number; key: string; title: string; body: string; revision: number; seenRevision: number; createdAt: string; updatedAt: string; lastAuthor: "owner" | "agent"; parent: number };
export type Publication = { number: number; revisionId: string; workingRevision: number; publishedAt: string; space: string };
export type ThreadComment = { id: number; space?: string; report: string; revision: string; block: string; parentId: number; author: string; fromOwner: boolean; body: string; createdAt: string; resolvedAt?: string; quote?: string };
export type ReplyDraft = { id: number; commentId: number; body: string; author: "owner" | "agent"; createdAt: string };
export type ReportView = Report & { backlinks: ReportSummary[]; publications: Publication[]; comments: ThreadComment[]; drafts: ReplyDraft[]; unresolvedLinks: string[] | null; blockIds: string[] };
export type Change = { status: "same" | "added" | "removed" | "changed"; before?: string; after?: string };
export type Changes = { since: number; revision: number; by: string[]; changes: Change[] };
export type GraphNode = { id: string; kind: "report" | "figure" | "run"; label: string; published?: boolean; missing?: boolean };
export type GraphEdge = { from: string; to: string; kind: "link" | "embeds" };
export type Graph = { nodes: GraphNode[]; edges: GraphEdge[] };
export type PublishedBlock = { id: string; kind: "text" | "figure" | "run"; text?: string };
export type Preflight = { revision: { blocks: PublishedBlock[]; title: string; number: number; authors: string }; warnings: string[]; errors: string[]; changes: { block: string; kind: string; status: "new" | "unchanged" | "removed"; text?: string }[]; space: string; readers: string[] | null; workingRevision: number };
export type PublishState = { url: string; keySet: boolean; space: string; spaceTitle: string; ownerName: string; agentName: string; lastSync?: string; lastError?: string };
export type Reader = { id: number; space: string; name: string; createdAt: string; expiresAt?: string; revoked: boolean };

/** A save refused because someone saved in between; current is the report as it is now. */
export class ConflictError extends ApiError {
  constructor(message: string, readonly current: Report) {
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
  const body = (await res.json().catch(() => null)) as { error?: string; current?: Report } | null;
  if (res.status === 409 && body?.current) throw new ConflictError(body.error ?? "The report changed.", body.current);
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export const listReports = () => call<ReportSummary[]>("/api/reports");
export const getReport = (id: number) => call<ReportView>(`/api/reports/${id}`);
export const createReport = (title: string, parent = 0, body = "") => post<Report>("/api/reports", { title, body, author: "owner", parent });
export const moveReport = (id: number, parent: number) => post<{ ok: boolean }>(`/api/reports/${id}/move`, { parent });
export const withdrawReport = (id: number) => post<{ ok: boolean }>(`/api/reports/${id}/withdraw`, {});
export const saveReport = (id: number, base: number, change: { title?: string; body?: string }) => post<Report>(`/api/reports/${id}`, { ...change, base, author: "owner" });
export const markSeen = (id: number, revision: number) => post<{ ok: boolean }>(`/api/reports/${id}/seen`, { revision });
export const deleteReport = (id: number) => post<{ ok: boolean }>(`/api/reports/${id}/delete`, {});
export const reportChanges = (id: number) => call<Changes>(`/api/reports/${id}/changes`);
export const reportGraph = () => call<Graph>("/api/reports/graph");
export const preflightReport = (id: number) => call<Preflight>(`/api/reports/${id}/preflight`);
export const publishReport = (id: number, revision: number) => post<{ number: number; revisionId: string; space: string }>(`/api/reports/${id}/publish`, { revision });
export const attachmentUrl = (sha: string) => `/api/attachments/${sha}`;
export const uploadAttachment = (data: string) => post<{ sha: string; mime: string; markdown: string }>("/api/attachments", { data });

export const publishState = () => call<PublishState>("/api/publish");
export const savePublishSettings = (s: Partial<{ url: string; key: string; space: string; spaceTitle: string; ownerName: string; agentName: string }>) => post<PublishState>("/api/publish/settings", s);
export const syncNow = () => post<PublishState>("/api/publish/sync", {});
export const listReaders = () => call<Reader[]>("/api/publish/readers");
export const createReader = (name: string) => post<{ reader: Reader & { token: string }; link: string }>("/api/publish/readers", { name });
export const revokeReader = (id: number) => post<{ ok: boolean }>(`/api/publish/readers/${id}/revoke`, {});

export const replyTo = (commentId: number, body: string) => post<ThreadComment>(`/api/report-comments/${commentId}/reply`, { body });
export const resolveThread = (commentId: number, resolved: boolean) => post<ThreadComment>(`/api/report-comments/${commentId}/resolve`, { resolved });
export const sendDraft = (id: number, body?: string) => post<ThreadComment>(`/api/reply-drafts/${id}/send`, body === undefined ? {} : { body });
export const deleteComment = (commentId: number) => post<{ deleted: number[] }>(`/api/report-comments/${commentId}/delete`, {});
export const discardDraft = (id: number) => post<{ ok: boolean }>(`/api/reply-drafts/${id}/discard`, {});
