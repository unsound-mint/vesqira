import { ApiError } from "./catalog";
import type { components } from "./schema";

type Schemas = components["schemas"];

export type Figure = Schemas["Figure"];
/** Reviewer-safe account of where a figure came from: no path, no dataset id. */
export type Provenance = Schemas["FigureProvenance"];
/** A run that was still running when it was put on a board. Its fields are the owner's; a reviewer never gets it. */
export type JobInfo = Schemas["JobInfo"];
export type FigureItem = Schemas["FigureBoardItem"];
export type JobItem = Schemas["JobBoardItem"];
export type BoardItem = Schemas["BoardItem"];
export type Board = Schemas["Board"];
export type OutcomeValue = "commented" | "changes_requested" | "reviewed";
export const OUTCOME_TEXT: Record<OutcomeValue, string> = { commented: "Commented", changes_requested: "Changes requested", reviewed: "Reviewed" };
export type Snapshot = Schemas["Snapshot"];
/** A place on a published picture, in fractions of its width and height from the top left. */
export type Anchor = { kind: "point"; x: number; y: number } | { kind: "box"; x: number; y: number; x1: number; y1: number };
export type Comment = { id: number; figureRevision: string; parentId: number; author: string; fromOwner: boolean; body: string; anchor?: Anchor; createdAt: string; resolvedAt?: string };

export type FrozenFigure = { kind?: "figure"; revision: string; title: string; caption: string; note: string; previewSha: string; width: number; height: number; provenance: Provenance };
/** A run that had not finished when the board was published: only its name and state at that moment. */
export type FrozenJob = { kind: "job"; title: string; state: string; at: string; note: string };
export type FrozenItem = FrozenFigure | FrozenJob;
export type Review = { snapshot: { boardTitle: string; label: string; question?: string; createdAt: string; expiresAt: string; items: FrozenItem[] }; comments: Comment[]; outcome: { value: "" | OutcomeValue; note?: string; by?: string; at?: string }; superseded?: { reason: string; at: string } };
export type Preflight = Schemas["Preflight"];

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(res.status === 404 && url.startsWith("/review-api") ? "This review link is not valid, has expired, or was revoked." : (body?.error ?? `Request failed (${res.status})`));
  return body as T;
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export type FigureRequest = Schemas["CreateFigureRequest"];

export const createFigure = (r: FigureRequest) => post<Figure>("/api/figures", r);
export const listFigures = () => call<{ figures: Figure[] }>("/api/figures").then((r) => r.figures);
export const figurePreviewUrl = (id: number) => `/api/figures/${id}/preview`;
export const listBoards = () => call<{ boards: Board[] }>("/api/boards").then((r) => r.boards);
/** One board with the live state of its job panels (this is the call that reads their logs; the list does not). */
export const getBoard = (id: number) => call<Board>(`/api/boards/${id}`);
export const createBoard = (title: string) => post<Board>("/api/boards", { title });
export const addToBoard = (boardId: number, figureId: number) => post<Board>(`/api/boards/${boardId}/items`, { figureId });
/** Puts a panel on the board that follows a job and becomes its result when it finishes. */
export const addJobToBoard = (boardId: number, jobId: number) => post<Board>(`/api/boards/${boardId}/items`, { jobId });
/** Replaces a job panel with the figure saved from its result, in the same place and with the same note. */
export const pinJobItem = (boardId: number, pos: number, figureId: number) => post<Board>(`/api/boards/${boardId}/items/${pos}/pin`, { figureId });
export const removeItem = (boardId: number, pos: number) => post<Board>(`/api/boards/${boardId}/items/${pos}/remove`, {});
export const moveItem = (boardId: number, pos: number, delta: -1 | 1) => post<Board>(`/api/boards/${boardId}/items/${pos}/move`, { delta });
export const setNote = (boardId: number, pos: number, note: string) => post<Board>(`/api/boards/${boardId}/items/${pos}/note`, { note });
export const publish = (boardId: number, label: string, hours: number, extra: { question?: string; supersedes?: number; reason?: string } = {}) =>
  post<Schemas["PublishResponse"]>(`/api/boards/${boardId}/publish`, { label, hours, ...extra });
export const preflight = (boardId: number, question: string) => call<Preflight>(`/api/boards/${boardId}/preflight?question=${encodeURIComponent(question)}`);
export const listSnapshots = (boardId: number) => call<{ snapshots: Snapshot[] }>(`/api/boards/${boardId}/snapshots`).then((r) => r.snapshots);
export const revokeSnapshot = (id: number) => post<{ revoked: boolean }>(`/api/snapshots/${id}/revoke`, {});
export const snapshotComments = (id: number) => call<{ comments: Comment[] }>(`/api/snapshots/${id}/comments`).then((r) => r.comments);

export const getReview = (token: string) => call<Review>(`/review-api/${encodeURIComponent(token)}`);
export const reviewPreviewUrl = (token: string, sha: string) => `/review-api/${encodeURIComponent(token)}/preview/${sha}`;
export const addReviewComment = (token: string, c: { figureRevision?: string; parentId?: number; author: string; body: string; anchor?: Anchor }) => post<Comment>(`/review-api/${encodeURIComponent(token)}/comments`, c);
export const ownerReply = (snapshotId: number, parentId: number, body: string) => post<Comment>(`/api/snapshots/${snapshotId}/comments`, { parentId, body });
export const resolveComment = (id: number, resolved: boolean) => post<{ resolved: boolean }>(`/api/comments/${id}/resolve`, { resolved });
export const setOutcome = (token: string, o: { outcome: OutcomeValue; note: string; author: string }) => post<Review["outcome"]>(`/review-api/${encodeURIComponent(token)}/outcome`, o);
