import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Board, BoardItem } from "../api/boards";

// An in-memory board with the server's semantics (positions are 0..n-1; add appends; remove closes the gap), and a log of calls.
type Row = { kind: "figure" | "job"; id: number; note: string };
let rows: Row[] = [];
let log: string[] = [];
const BOARD = 5;

const item = (r: Row, position: number): BoardItem =>
  (r.kind === "job"
    ? { kind: "job", position, note: r.note, job: { id: r.id, name: `job${r.id}` } }
    : { kind: "figure", position, note: r.note, figure: { id: r.id, title: `fig${r.id}` } }) as unknown as BoardItem;
const board = (): Board => ({ id: BOARD, title: "b", items: rows.map(item) }) as unknown as Board;

vi.mock("../api/boards", () => ({
  getBoard: async () => board(),
  addToBoard: async (_b: number, figureId: number) => { log.push(`add figure ${figureId}`); rows.push({ kind: "figure", id: figureId, note: "" }); return board(); },
  addJobToBoard: async (_b: number, jobId: number) => { log.push(`add job ${jobId}`); rows.push({ kind: "job", id: jobId, note: "" }); return board(); },
  removeItem: async (_b: number, pos: number) => { log.push(`remove ${pos}`); if (!rows[pos]) throw new Error("not found"); rows.splice(pos, 1); return board(); },
  moveItem: async (_b: number, pos: number, delta: number) => {
    log.push(`move ${pos} ${delta}`);
    const to = pos + delta;
    if (!rows[pos] || !rows[to]) throw new Error("not found");
    [rows[pos], rows[to]] = [rows[to]!, rows[pos]!];
    return board();
  },
  setNote: async (_b: number, pos: number, note: string) => { log.push(`note ${pos} ${JSON.stringify(note)}`); rows[pos]!.note = note; return board(); },
  resolveComment: async (id: number, resolved: boolean) => { log.push(`resolve ${id} ${resolved}`); return { id, resolved }; },
  pinJobItem: async () => board(),
}));

const cmds = await import("./boardCommands");
const stack = () => cmds.boardStack(BOARD);
const ids = () => rows.map((r) => `${r.kind[0]}${r.id}`).join(" ");

beforeEach(() => {
  cmds.forgetBoardStacks();
  rows = [{ kind: "figure", id: 1, note: "one" }, { kind: "figure", id: 2, note: "" }, { kind: "job", id: 9, note: "run" }, { kind: "figure", id: 4, note: "four" }];
  log = [];
});

describe("board commands", () => {
  it("move: undo calls the opposite move, redo repeats it", async () => {
    await cmds.moveItem(BOARD, board(), 1, 1);
    expect(ids()).toBe("f1 j9 f2 f4");
    expect(stack().getSnapshot().undoName).toBe("move panel");
    log = [];
    await stack().undo();
    expect(log).toEqual(["move 2 -1"]);
    expect(ids()).toBe("f1 f2 j9 f4");
    log = [];
    await stack().redo();
    expect(log).toEqual(["move 1 1"]);
    expect(ids()).toBe("f1 j9 f2 f4");
  });

  it("remove: undo puts the same panel back in the same place with the same note, for figures and for run panels", async () => {
    await cmds.removeItem(BOARD, board(), 0);
    expect(ids()).toBe("f2 j9 f4");
    log = [];
    await stack().undo();
    expect(ids()).toBe("f1 f2 j9 f4");
    expect(rows[0]!.note).toBe("one");
    expect(log).toEqual(["add figure 1", "move 3 -1", "move 2 -1", "move 1 -1", 'note 0 "one"']);
    await cmds.removeItem(BOARD, board(), 2); // the run panel
    await stack().undo();
    expect(ids()).toBe("f1 f2 j9 f4");
    expect(rows[2]).toMatchObject({ kind: "job", note: "run" });
    await stack().redo();
    expect(ids()).toBe("f1 f2 f4");
  });

  it("add: undo removes the appended panel, redo appends it again", async () => {
    await cmds.addFigure(BOARD, 7);
    expect(ids()).toBe("f1 f2 j9 f4 f7");
    log = [];
    await stack().undo();
    expect(log).toEqual(["remove 4"]);
    expect(ids()).toBe("f1 f2 j9 f4");
    await stack().redo();
    expect(ids()).toBe("f1 f2 j9 f4 f7");
    await cmds.addJob(BOARD, 12);
    await stack().undo();
    expect(ids()).toBe("f1 f2 j9 f4 f7");
  });

  it("note: undo restores the previous note, redo the new one", async () => {
    await cmds.setNote(BOARD, board(), 3, "changed");
    log = [];
    await stack().undo();
    expect(log).toEqual(['note 3 "four"']);
    expect(rows[3]!.note).toBe("four");
    await stack().redo();
    expect(rows[3]!.note).toBe("changed");
    await cmds.setNote(BOARD, board(), 3, "changed"); // not an edit
    expect(stack().getSnapshot().undoName).toBe("edit note");
  });

  it("comment: resolve is undone by reopening", async () => {
    await cmds.resolveComment(BOARD, 31, true);
    expect(stack().getSnapshot().undoName).toBe("resolve comment");
    await stack().undo();
    expect(log).toEqual(["resolve 31 true", "resolve 31 false"]);
    expect(stack().getSnapshot().redoName).toBe("resolve comment");
  });

  it("a conflict fails clearly, sends nothing and drops the redo stack", async () => {
    await cmds.moveItem(BOARD, board(), 0, 1); // f2 f1 j9 f4
    await cmds.setNote(BOARD, board(), 2, "later"); // an edit after it
    await stack().undo(); // the note goes back: fine
    expect(stack().getSnapshot().redoName).toBe("edit note");
    rows.reverse(); // another window rearranged the board
    log = [];
    expect(await stack().undo()).toBe(false);
    const snap = stack().getSnapshot();
    expect(log).toEqual([]); // nothing was sent
    expect(snap.error).toMatch(/Could not undo “move panel”.*board has changed.*not changed/);
    expect(snap.redoName).toBeNull(); // dropped with the failed one
    expect(snap.undoName).toBeNull();
  });

  it("a redo that no longer fits also fails clearly and drops the rest of the redo stack", async () => {
    await cmds.removeItem(BOARD, board(), 1);
    await cmds.removeItem(BOARD, board(), 0);
    await stack().undo();
    await stack().undo();
    expect(stack().getSnapshot().redoName).toBe("remove panel");
    rows.splice(1, 1); // another window removed the panel the redo would remove
    expect(await stack().redo()).toBe(false);
    expect(stack().getSnapshot().error).toMatch(/Could not redo “remove panel”/);
    expect(stack().getSnapshot().redoName).toBeNull();
  });

  it("reports a partial undo truthfully", async () => {
    await cmds.removeItem(BOARD, board(), 0);
    const api = await import("../api/boards");
    vi.spyOn(api, "moveItem").mockRejectedValueOnce(new Error("offline"));
    await stack().undo();
    expect(stack().getSnapshot().error).toMatch(/put back on the board, at the end.*offline/);
  });

  it("is bounded and each board has its own stack", async () => {
    for (let i = 0; i < cmds.boardStack(1).depth + 60; i++) await cmds.addFigure(BOARD, 100 + i);
    expect(stack().depth).toBe(50);
    expect(cmds.boardStack(6).depth).toBe(0);
    expect(cmds.boardStack(6)).not.toBe(stack());
  });
});
