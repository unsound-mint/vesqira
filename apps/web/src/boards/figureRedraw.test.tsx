import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Figure, RedrawCheck } from "../api/boards";
import type { Redrawn } from "../viewer/redraw";

const check = vi.fn<(id: number) => Promise<RedrawCheck>>();
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), getRedrawCheck: (id: number) => check(id) }));
const redraw = vi.fn<(f: Figure, o: { refresh: boolean }) => Promise<Redrawn>>();
const same = vi.fn(async () => true);
vi.mock("../viewer/redraw", async (orig) => ({ ...(await orig<typeof import("../viewer/redraw")>()), redrawFigure: (f: Figure, o: { refresh: boolean }) => redraw(f, o), samePixels: () => same(), savedPicture: async () => new Blob(["x"]) }));
const { FigureRedraw } = await import("./FigureRedraw");

const scene = { schema: "scene/1" } as unknown as Figure["scene"];
const fig = (over: Partial<Figure> = {}) => ({ id: 7, title: "Peak", caption: "", scene, provenance: {}, ...over }) as unknown as Figure;
const source = (status: string, text: string, readable = true) => ({ role: "source" as const, datasetId: 1, name: "cube.fits", status, differs: [], readable, text }) as RedrawCheck["sources"][number];
const drawn = (over: Partial<Redrawn> = {}): Redrawn => ({ kind: "scene", blob: new Blob(["png"]), notes: [], svg: () => "<svg/>", save: vi.fn(async () => ({ id: 8, title: "Peak" }) as Figure), ...over });

beforeEach(() => {
  check.mockReset();
  redraw.mockReset();
  same.mockReset();
  same.mockResolvedValue(true);
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(cleanup);

describe("draw again", () => {
  it("draws straight away when every file is proven unchanged, and says the pixels are the same", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ready", sources: [source("unchanged", "cube.fits: unchanged (its SHA-256 equals the one recorded when the figure was drawn).")] });
    redraw.mockResolvedValue(drawn());
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect(await screen.findByText(/same pixels as the saved one/)).toBeTruthy();
    expect(redraw).toHaveBeenCalledWith(expect.anything(), { refresh: false });
    expect(screen.getByText(/The original picture is kept/)).toBeTruthy();
  });

  it("says what changed per file and asks first when a file changed; nothing is drawn until the owner agrees", async () => {
    check.mockResolvedValue({
      figureId: 7, kind: "scene", state: "ask",
      prompt: "Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?",
      sources: [source("changed", "cube.fits: changed since the figure was drawn (time and fingerprint differ)."), source("probable", "co.fits: probably unchanged, not verified (size and time match, but no fingerprint was recorded to compare).")],
    });
    redraw.mockResolvedValue(drawn());
    same.mockResolvedValue(false);
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    const region = await screen.findByRole("region", { name: "What changed since this figure was drawn" });
    expect(region.textContent).toContain("cube.fits: changed since the figure was drawn (time and fingerprint differ).");
    expect(region.textContent).toContain("co.fits: probably unchanged, not verified (size and time match, but no fingerprint was recorded to compare).");
    expect(region.textContent).toContain("Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?");
    expect(redraw).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Draw anyway" }));
    expect(await screen.findByText(/not the same as the saved one/)).toBeTruthy();
    expect(redraw).toHaveBeenCalledWith(expect.anything(), { refresh: true }); // the sigma is measured again, only now
  });

  it("does not call the data unchanged when the owner agreed to draw from data that was not proven so, even if the pixels are the same", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ask", prompt: "p", sources: [source("probable", "x")] });
    redraw.mockResolvedValue(drawn());
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    await userEvent.click(await screen.findByRole("button", { name: "Draw anyway" }));
    const text = (await screen.findByText(/same pixels as the saved one/)).textContent ?? "";
    expect(text).toContain("although the data was not proven unchanged");
    expect(text).not.toContain("from unchanged data");
  });

  it("draws nothing on Cancel", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ask", prompt: "p", sources: [source("probable", "x")] });
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(redraw).not.toHaveBeenCalled();
    expect(screen.queryByRole("region")).toBeNull();
  });

  it("offers no way to draw when a file cannot be read, and says which", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "blocked", prompt: "This figure cannot be drawn again until every file it was drawn from can be read. The original PNG is kept.", sources: [source("missing", "cube.fits: the file was not found.", false)] });
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect((await screen.findByRole("region")).textContent).toContain("cube.fits: the file was not found.");
    expect(screen.queryByRole("button", { name: "Draw anyway" })).toBeNull();
    expect(redraw).not.toHaveBeenCalled();
  });

  it("says a figure with no description is shown as its PNG, and a figure saved before descriptions has no buttons", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "none", prompt: "This figure's description does not record the resolution its picture was drawn at, so it is shown as the PNG that was saved.", sources: [] });
    const { unmount } = render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect(await screen.findByText(/shown as the PNG that was saved/)).toBeTruthy();
    expect(redraw).not.toHaveBeenCalled();
    unmount();
    render(<FigureRedraw figure={fig({ scene: undefined })} />);
    expect(screen.queryByRole("button", { name: "Draw again" })).toBeNull();
    expect(screen.getByText(/keeps no description of how it was drawn, so it is shown as the PNG that was saved/)).toBeTruthy();
  });

  it("saves the redrawing as a new revision only on request, and says the original and the boards are untouched", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ready", sources: [source("unchanged", "x")] });
    const r = drawn();
    redraw.mockResolvedValue(r);
    const onSaved = vi.fn();
    render(<FigureRedraw figure={fig()} onSaved={onSaved} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    await screen.findByRole("region", { name: "The figure drawn again" });
    expect(r.save).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save as a new revision" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ id: 8 })));
    expect(r.save).toHaveBeenCalledOnce();
    expect(screen.getByText(/boards and shared reviews keep the figure they hold/)).toBeTruthy();
  });

  it("offers to put a saved revision on the board, only once and only when it was saved", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ready", sources: [source("unchanged", "x")] });
    redraw.mockResolvedValue(drawn());
    const addTo = vi.fn(async () => {});
    render(<FigureRedraw figure={fig()} addTo={addTo} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    await screen.findByRole("region", { name: "The figure drawn again" });
    expect(screen.queryByRole("button", { name: "Add to this board" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Save as a new revision" }));
    await userEvent.click(await screen.findByRole("button", { name: "Add to this board" }));
    expect(addTo).toHaveBeenCalledWith(8);
    expect(await screen.findByText(/Added to this board, after the original/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add to this board" })).toBeNull();
  });

  it("offers only the SVG export for a figure that was just saved", () => {
    render(<FigureRedraw figure={fig()} only="svg" />);
    expect(screen.queryByRole("button", { name: "Draw again" })).toBeNull();
    expect(screen.getByRole("button", { name: "Export SVG" })).toBeTruthy();
  });

  it("shows why a redrawing failed, such as a layer the worker now refuses", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ready", sources: [source("unchanged", "x")] });
    redraw.mockRejectedValue(new Error("co.fits contours cannot be drawn again: the recorded noise level does not match the data"));
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Draw again" }));
    expect((await screen.findByRole("alert")).textContent).toContain("cannot be drawn again");
  });
});

describe("export SVG", () => {
  it("goes through the same check, then downloads a file; a grid figure has no SVG button", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ready", sources: [source("unchanged", "x")] });
    redraw.mockResolvedValue(drawn());
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const { unmount } = render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Export SVG" }));
    expect(await screen.findByText("SVG exported.")).toBeTruthy();
    expect(check).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    unmount();
    render(<FigureRedraw figure={fig({ scene: { schema: "channel-grid/1" } as unknown as Figure["scene"] })} />);
    expect(screen.queryByRole("button", { name: "Export SVG" })).toBeNull();
    expect(screen.getByRole("button", { name: "Draw again" })).toBeTruthy();
    click.mockRestore();
  });

  it("asks before exporting from changed data, then says what the export drew", async () => {
    check.mockResolvedValue({ figureId: 7, kind: "scene", state: "ask", prompt: "Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?", sources: [source("changed", "cube.fits: changed since the figure was drawn (size differs).")] });
    redraw.mockResolvedValue(drawn({ notes: ["co.fits contours: the noise level (σ) is now 0.25, the figure recorded 0.1; the levels are drawn at the new value."] }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    render(<FigureRedraw figure={fig()} />);
    await userEvent.click(screen.getByRole("button", { name: "Export SVG" }));
    expect(click).not.toHaveBeenCalled();
    await userEvent.click(await screen.findByRole("button", { name: "Export anyway" }));
    expect((await screen.findByText(/SVG exported\./)).textContent).toContain("the noise level (σ) is now 0.25");
    expect(click).toHaveBeenCalledOnce();
    click.mockRestore();
  });
});
