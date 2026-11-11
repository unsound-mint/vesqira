import * as api from "../api/boards";
import type { Board, BoardItem } from "../api/boards";
import { ApiError } from "../api/http";
import { Conflict, UndoStack } from "./stack";

// Board edits as commands with an inverse (roadmap Stage 6, ADR 0057). Each function performs the edit through the existing
// API and, once it succeeded, records the command on that board's stack. A board is mutable working state: its edits are
// undoable while the session lasts. What is pinned evidence is not: publishing a review freezes the board and has no command,
// and an owner's reply to a reviewer is not undoable either (the reviewer may already have read it). Pinning a finished run's
// result over its panel is automatic (a system action, JobPanel), not an edit: undoing it would only pin it again.
//
// The server has no board revision; items are addressed by position. So an undo or redo of an edit at a position sends `expect`
// (what the panel there was when the command ran): the server applies the edit in one transaction only if the panel still
// matches, else answers 409 and changes nothing, and that is a Conflict here. Re-adding a panel and checking the board's length
// (reinsert, redo of an add) cannot be guarded that way, as an add has no position: those still read the board first, which is
// not atomic.

const stacks = new Map<number, UndoStack>();
/** One stack per board, for the whole session (the board screen unmounting does not forget it). */
export function boardStack(boardId: number): UndoStack {
  let s = stacks.get(boardId);
  if (!s) stacks.set(boardId, (s = new UndoStack()));
  return s;
}
export function forgetBoardStacks() {
  stacks.clear();
}

/** Identity of a panel across moves: the figure's id or the job's id. */
export const itemKey = (it: BoardItem): string => (it.kind === "job" ? `job:${it.job.id}` : `figure:${it.figure.id}`);
const label = (it: BoardItem) => (it.kind === "job" ? `run ${it.job.name}` : `“${it.figure.title}”`);

const CHANGED = "The board has changed since (in another window, or by a later edit that cannot be undone first). The board was not changed.";

const expectOf = (item: BoardItem, note?: string): api.BoardExpect => ({ kind: item.kind, id: item.kind === "job" ? item.job.id : item.figure.id, ...(note === undefined ? {} : { note }) });
const expectKey = (key: string): api.BoardExpect => {
  const [kind, id] = key.split(":") as ["figure" | "job", string];
  return { kind, id: Number(id) };
};

/** Runs a guarded edit: the server's 409 (the panel is not the one expected) is a Conflict, anything else passes through. */
async function guarded<T>(edit: () => Promise<T>): Promise<T> {
  try {
    return await edit();
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) throw new Conflict(CHANGED);
    throw e;
  }
}

async function expectLength(boardId: number, n: number): Promise<Board> {
  const board = await api.getBoard(boardId);
  if (board.items.length !== n) throw new Conflict(CHANGED);
  return board;
}

/** Puts item back at pos: appended (the API only appends), then moved up to its place, then its note restored. */
async function reinsert(boardId: number, item: BoardItem, pos: number, note: string, length: number): Promise<void> {
  let b = await expectLength(boardId, length);
  b = item.kind === "job" ? await api.addJobToBoard(boardId, item.job.id) : await api.addToBoard(boardId, item.figure.id);
  let at = b.items.length - 1;
  try {
    while (at > pos) {
      await api.moveItem(boardId, at, -1, expectOf(item));
      at -= 1;
    }
    if (note) await api.setNote(boardId, at, note, expectOf(item, ""));
  } catch (e) {
    throw new Conflict(`${label(item)} was put back on the board, at the end rather than its old place${note ? " and without its note" : ""}, because a later step failed (${e instanceof Error ? e.message : "error"}).`);
  }
}

/** A figure was added to a board (it is appended). */
export async function addFigure(boardId: number, figureId: number): Promise<Board> {
  const b = await api.addToBoard(boardId, figureId);
  const length = b.items.length;
  boardStack(boardId).push({
    name: "add figure to board",
    undo: async () => {
      await guarded(() => api.removeItem(boardId, length - 1, expectKey(`figure:${figureId}`)));
    },
    redo: async () => {
      await expectLength(boardId, length - 1);
      await api.addToBoard(boardId, figureId);
    },
  });
  return b;
}

/** A run panel was added to a board. */
export async function addJob(boardId: number, jobId: number): Promise<Board> {
  const b = await api.addJobToBoard(boardId, jobId);
  const length = b.items.length;
  boardStack(boardId).push({
    name: "add run to board",
    undo: async () => {
      await guarded(() => api.removeItem(boardId, length - 1, expectKey(`job:${jobId}`)));
    },
    redo: async () => {
      await expectLength(boardId, length - 1);
      await api.addJobToBoard(boardId, jobId);
    },
  });
  return b;
}

/** Moves the panel at pos one place up or down. */
export async function moveItem(boardId: number, board: Board, pos: number, delta: -1 | 1): Promise<Board> {
  const item = board.items[pos];
  const b = await api.moveItem(boardId, pos, delta);
  if (!item) return b;
  const key = itemKey(item);
  boardStack(boardId).push({
    name: "move panel",
    undo: async () => {
      await guarded(() => api.moveItem(boardId, pos + delta, delta === 1 ? -1 : 1, expectKey(key)));
    },
    redo: async () => {
      await guarded(() => api.moveItem(boardId, pos, delta, expectKey(key)));
    },
  });
  return b;
}

/** Takes the panel at pos off the board; undo puts it back in the same place with the same note. */
export async function removeItem(boardId: number, board: Board, pos: number): Promise<Board> {
  const item = board.items[pos];
  const b = await api.removeItem(boardId, pos);
  if (!item) return b;
  const key = itemKey(item);
  const length = board.items.length;
  boardStack(boardId).push({
    name: "remove panel",
    undo: () => reinsert(boardId, item, pos, item.note, length - 1),
    redo: async () => {
      await guarded(() => api.removeItem(boardId, pos, expectKey(key)));
    },
  });
  return b;
}

/** Changes the reviewer note of the panel at pos. */
export async function setNote(boardId: number, board: Board, pos: number, note: string): Promise<Board> {
  const item = board.items[pos];
  const b = await api.setNote(boardId, pos, note);
  if (!item || item.note === note) return b;
  const key = itemKey(item);
  const prev = item.note;
  const set = (from: string, to: string) => async () => {
    await guarded(() => api.setNote(boardId, pos, to, { ...expectKey(key), note: from }));
  };
  boardStack(boardId).push({ name: "edit note", undo: set(note, prev), redo: set(prev, note) });
  return b;
}

/** Marks a reviewer's comment resolved or open again. (A reply is not a command: the reviewer may have read it.) */
export async function resolveComment(boardId: number, commentId: number, resolved: boolean): Promise<void> {
  await api.resolveComment(commentId, resolved);
  boardStack(boardId).push({
    name: resolved ? "resolve comment" : "reopen comment",
    undo: async () => void (await api.resolveComment(commentId, !resolved)),
    redo: async () => void (await api.resolveComment(commentId, resolved)),
  });
}
