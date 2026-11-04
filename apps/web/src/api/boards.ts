import { ApiError } from "./catalog";

export type Figure = {
  id: number;
  datasetId: number;
  title: string;
  caption: string;
  createdAt: string;
  revision: string;
  previewSha: string;
  width: number;
  height: number;
  provenance: Provenance;
};

/** Reviewer-safe account of where a figure came from: no path, no dataset id. */
export type Provenance = {
  source: { name: string; sizeBytes: number; modifiedAt: string; identity: string };
  interpretation?: { kind: string; unit: string; declaredBy: string; hasReliabilityMap: boolean };
  view: { kind: string; channel?: number; spectralValue?: number; spectralUnit?: string; colormap?: string; stretch?: string; range?: string; limits?: number[]; limitsUnit?: string };
  run?: { name: string; kind: string; role: string; gitCommit: string; dirty: boolean; reproducibleFromCommit: boolean; generatedAt: string; params: { key: string; value: string }[] };
  statements: string[];
};

export type BoardItem = { position: number; figure: Figure; note: string };
export type Board = { id: number; title: string; createdAt: string; updatedAt: string; items: BoardItem[] };
export type Snapshot = { id: number; boardId: number; label: string; createdAt: string; expiresAt: string; revoked: boolean; items: number };
export type Comment = { id: number; figureRevision: string; author: string; body: string; createdAt: string };

export type FrozenItem = { revision: string; title: string; caption: string; note: string; previewSha: string; width: number; height: number; provenance: Provenance };
export type Review = { snapshot: { boardTitle: string; label: string; createdAt: string; expiresAt: string; items: FrozenItem[] }; comments: Comment[] };

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

export type FigureRequest = { datasetId: number; title: string; caption: string; view: Record<string, unknown>; previewPng: string };

export const createFigure = (r: FigureRequest) => post<Figure>("/api/figures", r);
export const listFigures = () => call<{ figures: Figure[] }>("/api/figures").then((r) => r.figures);
export const figurePreviewUrl = (id: number) => `/api/figures/${id}/preview`;
export const listBoards = () => call<{ boards: Board[] }>("/api/boards").then((r) => r.boards);
export const createBoard = (title: string) => post<Board>("/api/boards", { title });
export const addToBoard = (boardId: number, figureId: number) => post<Board>(`/api/boards/${boardId}/items`, { figureId });
export const removeItem = (boardId: number, pos: number) => post<Board>(`/api/boards/${boardId}/items/${pos}/remove`, {});
export const moveItem = (boardId: number, pos: number, delta: -1 | 1) => post<Board>(`/api/boards/${boardId}/items/${pos}/move`, { delta });
export const setNote = (boardId: number, pos: number, note: string) => post<Board>(`/api/boards/${boardId}/items/${pos}/note`, { note });
export const publish = (boardId: number, label: string, hours: number) =>
  post<{ snapshot: Snapshot; token: string; reviewPath: string; note: string }>(`/api/boards/${boardId}/publish`, { label, hours });
export const listSnapshots = (boardId: number) => call<{ snapshots: Snapshot[] }>(`/api/boards/${boardId}/snapshots`).then((r) => r.snapshots);
export const revokeSnapshot = (id: number) => post<{ revoked: boolean }>(`/api/snapshots/${id}/revoke`, {});
export const snapshotComments = (id: number) => call<{ comments: Comment[] }>(`/api/snapshots/${id}/comments`).then((r) => r.comments);

export const getReview = (token: string) => call<Review>(`/review-api/${encodeURIComponent(token)}`);
export const reviewPreviewUrl = (token: string, sha: string) => `/review-api/${encodeURIComponent(token)}/preview/${sha}`;
export const addReviewComment = (token: string, c: { figureRevision: string; author: string; body: string }) => post<Comment>(`/review-api/${encodeURIComponent(token)}/comments`, c);
