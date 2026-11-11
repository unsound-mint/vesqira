import { request as call, postJSON as post } from "./http";
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
export type OutcomeValue = Schemas["OutcomeValue"];
export const OUTCOME_TEXT: Record<OutcomeValue, string> = { commented: "Commented", changes_requested: "Changes requested", reviewed: "Reviewed" };
export type Snapshot = Schemas["Snapshot"];
/** A place on a published picture, in fractions of its width and height from the top left. */
export type Anchor = Schemas["Anchor"];
export type Comment = Schemas["Comment"];
export type FrozenFigure = Schemas["FrozenFigure"];
/** A run that had not finished when the board was published: only its name and state at that moment. */
export type FrozenJob = Schemas["FrozenJob"];
export type FrozenItem = Schemas["FrozenItem"];
/** What a reviewer's browser receives for one link. */
export type Review = Schemas["Review"];
export type Preflight = Schemas["Preflight"];

export type FigureRequest = Schemas["CreateFigureRequest"];

export const createFigure = (r: FigureRequest) => post<Figure>("/api/figures", r);
export const listFigures = () => call<{ figures: Figure[] }>("/api/figures").then((r) => r.figures);
export const figurePreviewUrl = (id: number) => `/api/figures/${id}/preview`;
/** What "draw again" must tell the owner before it draws (ADR 0042, section 5); reads and changes nothing. */
export type RedrawCheck = Schemas["RedrawCheck"];
export type RedrawSource = Schemas["RedrawSource"];
export const getRedrawCheck = (id: number) => call<RedrawCheck>(`/api/figures/${id}/redraw-check`);
export const listBoards = (opts: { archived?: "all" | "only" } = {}) => call<{ boards: Board[] }>(`/api/boards${opts.archived ? `?archived=${opts.archived}` : ""}`).then((r) => r.boards);
/** One board with the live state of its job panels (this is the call that reads their logs; the list does not). */
export const getBoard = (id: number) => call<Board>(`/api/boards/${id}`);
export const createBoard = (title: string) => post<Board>("/api/boards", { title });
export const addToBoard = (boardId: number, figureId: number) => post<Board>(`/api/boards/${boardId}/items`, { figureId });
/** Puts a panel on the board that follows a job and becomes its result when it finishes. */
export const addJobToBoard = (boardId: number, jobId: number) => post<Board>(`/api/boards/${boardId}/items`, { jobId });
/** Replaces a job panel with the figure saved from its result, in the same place and with the same note. */
export const pinJobItem = (boardId: number, pos: number, figureId: number) => post<Board>(`/api/boards/${boardId}/items/${pos}/pin`, { figureId });
/** What the client last saw at the position: the server applies the edit only if the panel there still matches, else 409 (ADR 0057). */
export type BoardExpect = Schemas["BoardExpect"];
export const removeItem = (boardId: number, pos: number, expect?: BoardExpect) => post<Board>(`/api/boards/${boardId}/items/${pos}/remove`, expect ? { expect } : {});
export const moveItem = (boardId: number, pos: number, delta: -1 | 1, expect?: BoardExpect) => post<Board>(`/api/boards/${boardId}/items/${pos}/move`, { delta, ...(expect ? { expect } : {}) });
export const setNote = (boardId: number, pos: number, note: string, expect?: BoardExpect) => post<Board>(`/api/boards/${boardId}/items/${pos}/note`, { note, ...(expect ? { expect } : {}) });
/** A panel's place on the board's 12-column grid (ADR 0060). */
export type BoardRect = Schemas["BoardRect"];
/** Saves every panel's place in one write. `expect` names the panel at each position, `from` the layout being replaced; a mismatch is a 409. A null place clears it. */
export const setLayout = (boardId: number, layout: (BoardRect | null)[], expect: BoardExpect[], from?: (BoardRect | null)[]) =>
  post<Board>(`/api/boards/${boardId}/layout`, { layout, expect, ...(from ? { from } : {}) });
export const publish = (boardId: number, label: string, hours: number, extra: { question?: string; supersedes?: number; reason?: string } = {}) =>
  post<Schemas["PublishResponse"]>(`/api/boards/${boardId}/publish`, { label, hours, ...extra });
export const preflight = (boardId: number, question: string) => call<Preflight>(`/api/boards/${boardId}/preflight?question=${encodeURIComponent(question)}`);
export const listSnapshots = (boardId: number) => call<{ snapshots: Snapshot[] }>(`/api/boards/${boardId}/snapshots`).then((r) => r.snapshots);
export const revokeSnapshot = (id: number) => post<Schemas["Revoked"]>(`/api/snapshots/${id}/revoke`, {});
export const snapshotComments = (id: number) => call<Schemas["CommentList"]>(`/api/snapshots/${id}/comments`).then((r) => r.comments);

export const getReview = (token: string) => call<Review>(`/review-api/${encodeURIComponent(token)}`);
export const reviewPreviewUrl = (token: string, sha: string) => `/review-api/${encodeURIComponent(token)}/preview/${sha}`;
export const addReviewComment = (token: string, c: Schemas["ReviewCommentRequest"]) => post<Comment>(`/review-api/${encodeURIComponent(token)}/comments`, c);
export const ownerReply = (snapshotId: number, parentId: number, body: string) => post<Comment>(`/api/snapshots/${snapshotId}/comments`, { parentId, body });
export const resolveComment = (id: number, resolved: boolean) => post<Schemas["Resolved"]>(`/api/comments/${id}/resolve`, { resolved });
export const setOutcome = (token: string, o: Schemas["OutcomeRequest"]) => post<Review["outcome"]>(`/review-api/${encodeURIComponent(token)}/outcome`, o);
