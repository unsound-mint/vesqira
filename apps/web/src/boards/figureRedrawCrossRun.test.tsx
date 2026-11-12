import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Figure, RedrawCheck } from "../api/boards";
import type { Redrawn } from "../viewer/redraw";

const check = vi.fn<(id: number) => Promise<RedrawCheck>>();
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), getRedrawCheck: (id: number) => check(id) }));
const redraw = vi.fn<(f: Figure, o: { refresh: boolean }) => Promise<Redrawn>>();
const same = vi.fn(async () => true);
vi.mock("../viewer/redraw", async (orig) => ({ ...(await orig<typeof import("../viewer/redraw")>()), redrawFigure: (f: Figure, o: { refresh: boolean }) => redraw(f, o), samePixels: () => same(), savedPicture: async () => new Blob(["png"]) }));
const { FigureRedraw } = await import("./FigureRedraw");

const fig = { id: 7, title: "am over the sweep", caption: "", scene: { schema: "cross-run/1" }, provenance: {} } as unknown as Figure;
const ready: RedrawCheck = { figureId: 7, kind: "cross-run", state: "ready", sources: [] };
const drawn = (over: Partial<Redrawn> = {}): Redrawn => ({ kind: "cross-run", blob: new Blob(["png"]), notes: ["The same runs with the same recorded values as when the figure was saved."], dataChanged: false, svg: null, save: vi.fn(async () => ({ id: 8, title: "am over the sweep" }) as Figure), ...over });

beforeEach(() => {
  check.mockReset().mockResolvedValue(ready);
  redraw.mockReset();
  same.mockReset().mockResolvedValue(true);
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(cleanup);

describe("draw again for a cross-run plot", () => {
  it("is offered for a figure with no dataset, with no SVG export (the page exports that)", () => {
    render(<FigureRedraw figure={fig} />);
    expect(screen.getByRole("button", { name: "Draw again" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Export SVG" })).toBeNull();
    expect(screen.queryByText(/keeps no description of how it was drawn/)).toBeNull();
  });

  it("says the same when the runs and the pixels are the same", async () => {
    redraw.mockResolvedValue(drawn());
    render(<FigureRedraw figure={fig} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect(await screen.findByText(/Drawn again from unchanged data: the picture has the same pixels/)).toBeTruthy();
    expect(redraw).toHaveBeenCalledWith(fig, { refresh: false });
  });

  it("announces that the runs changed even when the pixels happen to be the same, and lists what changed", async () => {
    redraw.mockResolvedValue(drawn({ dataChanged: true, notes: ["1 run now matches that did not when it was saved: lmc_4."] }));
    render(<FigureRedraw figure={fig} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect(await screen.findByText(/The runs or what they record are not what they were when this figure was saved/)).toBeTruthy();
    expect(screen.queryByText(/Drawn again from unchanged data/)).toBeNull();
    expect(screen.getByText(/1 run now matches that did not when it was saved: lmc_4\./)).toBeTruthy();
  });

  it("saves a new linked revision only when asked, and says why a figure could not be drawn", async () => {
    const r = drawn({ dataChanged: true, notes: ["x"] });
    redraw.mockResolvedValueOnce(r);
    render(<FigureRedraw figure={fig} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    await screen.findByText(/not what they were/);
    expect(r.save).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save as a new revision" }));
    expect(await screen.findByText(/Saved as a new revision/)).toBeTruthy();
    expect(r.save).toHaveBeenCalledTimes(1);
    cleanup();
    redraw.mockRejectedValueOnce(new Error("The conversion this figure used is refused now, so it is not drawn: different dimensions"));
    render(<FigureRedraw figure={fig} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect((await screen.findByRole("alert")).textContent).toContain("refused now");
  });
});
