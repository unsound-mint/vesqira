import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BoardsView } from "./BoardsView";
import { forgetBoardStacks } from "../undo/boardCommands";
import type { Rect } from "./layout";

vi.mock("../viewer/useViewer", () => ({ useViewer: () => ({ plane: null, interp: null, reliability: null }) }));
vi.mock("../viewer/PlaneViewer", () => ({ PlaneViewer: () => <div /> }));

// A stateful stand-in for the board routes: panels with an optional stored place, and the layout route's guards.
type Panel = { id: number; layout: Rect | null; run?: string; channel?: number };
let panels: Panel[] = [];
let posts: { url: string; body: Record<string, unknown> }[] = [];
const fig = (p: Panel) => ({
  id: p.id, datasetId: 1, title: `Figure ${p.id}`, caption: "", createdAt: "", revision: `r${p.id}`.padEnd(12, "0"), width: 10, height: 10,
  provenance: { source: { name: "cube", identity: "i" }, view: { kind: "plane", ...(p.channel === undefined ? {} : { channel: p.channel }) }, statements: [], ...(p.run ? { run: { name: `run-${p.run}`, kind: p.run, role: "output", gitCommit: "", dirty: false, reproducibleFromCommit: false, generatedAt: "", params: [], metrics: [], review: null } } : {}) },
});
const board = () => ({ id: 1, title: "Board", createdAt: "", updatedAt: "", items: panels.map((p, i) => ({ position: i, kind: "figure", figure: fig(p), note: "", ...(p.layout ? { layout: p.layout } : {}) })) });
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  forgetBoardStacks();
  panels = [{ id: 1, layout: null }, { id: 2, layout: null }, { id: 3, layout: null }];
  posts = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST" && url === "/api/boards/1/layout") {
      const body = JSON.parse(String(init.body)) as { layout: (Rect | null)[]; expect: { id: number }[]; from?: (Rect | null)[] };
      posts.push({ url, body });
      const stale = body.expect.length !== panels.length || body.expect.some((e, i) => panels[i]?.id !== e.id) || (body.from && body.from.some((r, i) => JSON.stringify(r) !== JSON.stringify(panels[i]!.layout)));
      if (stale) return json({ error: "the board has changed", board: board() }, 409);
      body.layout.forEach((r, i) => { panels[i]!.layout = r; });
      return json(board());
    }
    const m = /^\/api\/boards\/1\/items(?:\/(\d+)\/(move|remove))?$/.exec(url);
    if (init?.method === "POST" && m) {
      const body = init.body ? JSON.parse(String(init.body)) : {};
      const pos = Number(m[1]);
      if (!m[2]) panels.push({ id: body.figureId, layout: null });
      else if (m[2] === "remove") panels.splice(pos, 1);
      else [panels[pos], panels[pos + body.delta]] = [panels[pos + body.delta]!, panels[pos]!];
      return json(board());
    }
    if (init?.method === "POST") return json(board());
    return json(url.endsWith("/snapshots") ? { snapshots: [] } : url.startsWith("/api/boards/1") ? board() : url.startsWith("/api/boards") ? { boards: [board()] } : {});
  }));
});
afterEach(() => vi.unstubAllGlobals());

const handle = (n: number) => screen.getByRole("button", { name: `Move or resize “Figure ${n}”` });
const places = () => panels.map((p) => p.layout);
const live = () => screen.getAllByRole("status").map((e) => e.textContent).join(" | ");

describe("the board grid", () => {
  it("shows a board that was never arranged one panel per row, and writes nothing", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    expect(handle(1)).toHaveTextContent("column 1, row 1 · 12 × 8");
    expect(handle(2)).toHaveTextContent("column 1, row 9 · 12 × 8");
    expect(screen.getAllByRole("button", { name: "Move up" })).toHaveLength(3); // the reading-order buttons are still there
    expect(screen.queryByRole("button", { name: "Back to reading order" })).toBeNull();
    expect(posts).toEqual([]);
  });

  it("shows a stored layout as saved", async () => {
    panels = [{ id: 1, layout: { x: 0, y: 0, w: 6, h: 4 } }, { id: 2, layout: { x: 6, y: 0, w: 6, h: 4 } }, { id: 3, layout: { x: 0, y: 4, w: 12, h: 3 } }];
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    expect(handle(2)).toHaveTextContent("column 7, row 1 · 6 × 4");
    expect(handle(3)).toHaveTextContent("column 1, row 5 · 12 × 3");
    expect(screen.queryByRole("button", { name: "Move up" })).toBeNull(); // the grid, not the order, says where a panel is
    expect(screen.getByRole("button", { name: "Back to reading order" })).toBeInTheDocument();
  });

  it("resizes with Shift and the arrows, moves with the arrows, announces each result, and saves every panel's place once per change", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    handle(1).focus();
    await userEvent.keyboard("{Shift>}{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}{/Shift}"); // 12 -> 6 columns
    await waitFor(() => expect(places()[0]).toEqual({ x: 0, y: 0, w: 6, h: 8 }));
    expect(live()).toContain("“Figure 1” resized to column 1, row 1; 6 columns wide, 8 rows tall.");
    expect(posts).toHaveLength(6);
    expect(posts[0]!.body.from).toEqual([null, null, null]); // the first change replaces "no layout"
    expect(posts[0]!.body.layout).toEqual([{ x: 0, y: 0, w: 11, h: 8 }, { x: 0, y: 8, w: 12, h: 8 }, { x: 0, y: 16, w: 12, h: 8 }]); // the whole board, every panel placed

    await userEvent.keyboard("{ArrowRight}{ArrowRight}");
    await waitFor(() => expect(places()[0]).toEqual({ x: 2, y: 0, w: 6, h: 8 }));
    expect(live()).toContain("“Figure 1” moved to column 3, row 1; 6 columns wide, 8 rows tall.");
    expect(handle(1)).toHaveFocus();
    // the panels never overlap
    const rs = places() as Rect[];
    for (const a of rs) for (const b of rs) if (a !== b) expect(a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h).toBe(false);
  });

  it("says so, and saves nothing, when a key cannot move or resize any further", async () => {
    panels = [{ id: 1, layout: { x: 0, y: 0, w: 2, h: 2 } }];
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    handle(1).focus();
    await userEvent.keyboard("{ArrowLeft}");
    expect(live()).toContain("Cannot move further that way. “Figure 1” is at column 1, row 1; 2 columns wide, 2 rows tall.");
    await userEvent.keyboard("{Shift>}{ArrowLeft}{/Shift}");
    expect(live()).toContain("Cannot resize further that way.");
    expect(posts).toEqual([]);
  });

  it("leaves the arrow keys alone inside a note", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    const note = screen.getAllByLabelText("Note for the reviewer")[0]!;
    note.focus();
    fireEvent.keyDown(note, { key: "ArrowRight" });
    expect(posts).toEqual([]);
  });

  it("is one undoable step per change, and undoing the first one returns the board to having no layout", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    handle(2).focus();
    await userEvent.keyboard("{Shift>}{ArrowUp}{/Shift}");
    await waitFor(() => expect(places()[1]).toEqual({ x: 0, y: 8, w: 12, h: 7 }));
    expect(screen.getByRole("button", { name: "Undo: resize panel" })).toBeEnabled();

    await userEvent.click(screen.getByRole("button", { name: "Undo: resize panel" }));
    await waitFor(() => expect(places()).toEqual([null, null, null]));
    expect(posts.at(-1)!.body.layout).toEqual([null, null, null]);
    expect(screen.getByRole("button", { name: "Redo: resize panel" })).toBeEnabled();

    fireEvent.keyDown(document.body, { key: "z", shiftKey: true, ctrlKey: true });
    await waitFor(() => expect(places()[1]).toEqual({ x: 0, y: 8, w: 12, h: 7 }));
  });

  it("refuses an undo when the board changed in another window, and changes nothing", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    handle(1).focus();
    await userEvent.keyboard("{Shift>}{ArrowLeft}{/Shift}");
    await waitFor(() => expect(places()[0]).toEqual({ x: 0, y: 0, w: 11, h: 8 }));
    panels[0]!.layout = { x: 0, y: 0, w: 4, h: 8 }; // another window resized it again
    await userEvent.click(screen.getByRole("button", { name: "Undo: resize panel" }));
    expect(await screen.findByText(/Could not undo “resize panel”/)).toBeInTheDocument();
    expect(panels[0]!.layout).toEqual({ x: 0, y: 0, w: 4, h: 8 });
  });

  it("arranges channels as columns and methods as rows from what the figures record", async () => {
    panels = [
      { id: 1, layout: null, run: "vgt", channel: 0 }, { id: 2, layout: null, run: "vgt", channel: 1 },
      { id: 3, layout: null, run: "rohsa", channel: 0 }, { id: 4, layout: null, run: "rohsa", channel: 1 },
    ];
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    await userEvent.click(screen.getByRole("button", { name: "Arrange automatically" }));
    await waitFor(() => expect(places()[3]).toEqual({ x: 6, y: 8, w: 6, h: 8 }));
    expect(places()[0]).toEqual({ x: 0, y: 0, w: 6, h: 8 });
    expect(live()).toContain("2 channels as columns and 2 methods (run kind) as rows");
    expect(screen.getByRole("button", { name: "Undo: arrange panels automatically" })).toBeEnabled();
  });

  it("does not guess when the figures do not say which run made them: reading order, with the reason", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    await userEvent.click(screen.getByRole("button", { name: "Arrange automatically" }));
    await waitFor(() => expect(live()).toContain("Not enough is recorded to arrange by channel and method: 3 figures do not record a channel"));
    expect(live()).toContain("placed in reading order instead");
    expect(posts).toEqual([]); // reading order is what it already was: nothing to save
  });

  it("puts a removed panel back with its place, and every other panel's place as it was", async () => {
    panels = [{ id: 1, layout: { x: 0, y: 0, w: 6, h: 4 } }, { id: 2, layout: { x: 6, y: 0, w: 6, h: 6 } }, { id: 3, layout: { x: 0, y: 6, w: 12, h: 3 } }];
    const before = places();
    render(<BoardsView />);
    await screen.findByText("Figure 2");
    await userEvent.click(screen.getAllByRole("button", { name: "Remove from board" })[1]!);
    await waitFor(() => expect(panels).toHaveLength(2));
    await userEvent.click(screen.getByRole("button", { name: "Undo: remove panel" }));
    await waitFor(() => expect(panels).toHaveLength(3));
    await waitFor(() => expect(places()).toEqual(before));
    expect(panels.map((p) => p.id)).toEqual([1, 2, 3]);
  });

  it("takes the board back to reading order with one undoable step", async () => {
    panels = [{ id: 1, layout: { x: 6, y: 0, w: 6, h: 4 } }, { id: 2, layout: { x: 0, y: 0, w: 6, h: 4 } }];
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    await userEvent.click(screen.getByRole("button", { name: "Back to reading order" }));
    await waitFor(() => expect(places()).toEqual([null, null]));
    expect(screen.getAllByRole("button", { name: "Move up" })).toHaveLength(2);
    await act(async () => { fireEvent.keyDown(document.body, { key: "z", ctrlKey: true }); });
    await waitFor(() => expect(places()[0]).toEqual({ x: 6, y: 0, w: 6, h: 4 }));
  });
});
