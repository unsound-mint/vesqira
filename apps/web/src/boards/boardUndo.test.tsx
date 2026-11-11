import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BoardsView } from "./BoardsView";
import { forgetBoardStacks } from "../undo/boardCommands";

vi.mock("../viewer/useViewer", () => ({ useViewer: () => ({ plane: null, interp: null, reliability: null }) }));
vi.mock("../viewer/PlaneViewer", () => ({ PlaneViewer: () => <div /> }));

// A stateful stand-in for the board routes: the figure panels as {id, note}, positions 0..n-1.
let panels: { id: number; note: string }[] = [];
let calls: string[] = [];
const fig = (id: number) => ({
  id, datasetId: 1, title: `Figure ${id}`, caption: "", createdAt: "", revision: `r${id}`.padEnd(12, "0"), width: 10, height: 10,
  provenance: { source: { name: "cube", identity: "i" }, view: { channel: 0 }, statements: [] },
});
const board = () => ({ id: 1, title: "Board", createdAt: "", updatedAt: "", items: panels.map((p, i) => ({ position: i, kind: "figure", figure: fig(p.id), note: p.note })) });
const ok = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  forgetBoardStacks();
  panels = [{ id: 1, note: "" }, { id: 2, note: "n2" }, { id: 3, note: "" }];
  calls = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const m = /^\/api\/boards\/1\/items(?:\/(\d+)\/(move|remove|note))?$/.exec(url);
    if (init?.method === "POST" && m) {
      const body = init.body ? JSON.parse(String(init.body)) : {};
      const pos = Number(m[1]);
      const e = body.expect as { id: number; note?: string } | undefined; // the server's guard: the panel there must still be the expected one
      if (e && (panels[pos]?.id !== e.id || (e.note !== undefined && panels[pos]!.note !== e.note))) {
        return new Response(JSON.stringify({ error: "the board has changed", board: board() }), { status: 409, headers: { "Content-Type": "application/json" } });
      }
      calls.push(`${m[2] ?? "add"} ${m[1] ?? ""} ${init.body ?? ""}`.trim());
      if (!m[2]) panels.push({ id: body.figureId, note: "" });
      else if (m[2] === "move") [panels[pos], panels[pos + body.delta]] = [panels[pos + body.delta]!, panels[pos]!];
      else if (m[2] === "remove") panels.splice(pos, 1);
      else panels[pos]!.note = body.note;
      return ok(board());
    }
    const body = url.endsWith("/snapshots") ? { snapshots: [] } : url.startsWith("/api/boards/1") ? board() : url.startsWith("/api/boards") ? { boards: [board()] } : url.includes("/newer") ? {} : {};
    return ok(body);
  }));
});
afterEach(() => vi.unstubAllGlobals());

const titles = () => screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent).filter((t) => t?.startsWith("Figure"));

describe("undo on a board", () => {
  it("moves a panel, undoes it with the button and with Ctrl+Z, redoes it with Ctrl+Shift+Z, and names the command", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    const undoBtn = () => screen.getByRole("button", { name: /^Undo/ });
    expect(undoBtn()).toBeDisabled();
    await userEvent.click(screen.getAllByRole("button", { name: "Move down" })[0]!);
    await waitFor(() => expect(titles()).toEqual(["Figure 2", "Figure 1", "Figure 3"]));
    expect(undoBtn()).toHaveAccessibleName("Undo: move panel");

    await userEvent.click(undoBtn());
    await waitFor(() => expect(titles()).toEqual(["Figure 1", "Figure 2", "Figure 3"]));
    expect(screen.getByRole("button", { name: "Redo: move panel" })).toBeEnabled();

    fireEvent.keyDown(document.body, { key: "z", shiftKey: true, ctrlKey: true });
    await waitFor(() => expect(titles()).toEqual(["Figure 2", "Figure 1", "Figure 3"]));
    fireEvent.keyDown(document.body, { key: "z", metaKey: true });
    await waitFor(() => expect(titles()).toEqual(["Figure 1", "Figure 2", "Figure 3"]));
    expect(calls.filter((c) => c.startsWith("move"))).toEqual([
      'move 0 {"delta":1}',
      'move 1 {"delta":-1,"expect":{"kind":"figure","id":1}}',
      'move 0 {"delta":1,"expect":{"kind":"figure","id":1}}',
      'move 1 {"delta":-1,"expect":{"kind":"figure","id":1}}',
    ]);
  });

  it("puts a removed panel back in its place with its note", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 2");
    await userEvent.click(screen.getAllByRole("button", { name: "Remove from board" })[1]!);
    await waitFor(() => expect(titles()).toEqual(["Figure 1", "Figure 3"]));
    expect(screen.getByRole("button", { name: "Undo: remove panel" })).toBeEnabled();
    fireEvent.keyDown(document.body, { key: "z", ctrlKey: true });
    await waitFor(() => expect(titles()).toEqual(["Figure 1", "Figure 2", "Figure 3"]));
    expect(panels[1]).toEqual({ id: 2, note: "n2" });
  });

  it("leaves Ctrl+Z in a note to the text field", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    await userEvent.click(screen.getAllByRole("button", { name: "Move down" })[0]!);
    await waitFor(() => expect(titles()[0]).toBe("Figure 2"));
    const note = screen.getAllByLabelText("Note for the reviewer")[0]!;
    note.focus();
    fireEvent.keyDown(note, { key: "z", ctrlKey: true });
    expect(titles()[0]).toBe("Figure 2"); // not undone from inside the field
  });

  it("fails clearly when the board changed elsewhere, changes nothing, and drops the redo stack", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    await userEvent.click(screen.getAllByRole("button", { name: "Move down" })[0]!);
    await waitFor(() => expect(titles()[0]).toBe("Figure 2"));
    panels.splice(1, 1); // another window removed the panel that was moved
    const before = calls.length;
    await userEvent.click(screen.getByRole("button", { name: "Undo: move panel" }));
    const alert = await screen.findByText(/Could not undo “move panel”/);
    expect(alert).toHaveTextContent(/board has changed/);
    expect(calls).toHaveLength(before);
    expect(screen.getByRole("button", { name: "Redo" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Undo" })).toBeDisabled();
    await userEvent.click(within(alert.parentElement!).getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(/Could not undo/)).toBeNull();
  });

  it("has no undo for publishing: sharing is pinned evidence", async () => {
    render(<BoardsView />);
    await screen.findByText("Figure 1");
    expect(screen.getByRole("button", { name: /Freeze and create link/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Undo: (publish|share|freeze)/i })).toBeNull();
  });
});
