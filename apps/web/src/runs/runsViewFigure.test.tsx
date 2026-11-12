import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Run } from "../api/runs";
import { metric, run } from "../catalog/runFixtures";
import { RunsView } from "./RunsView";
import { resetConvertibility } from "./useConvertibility";
import { initialRunsView } from "./runsViewState";

const views = vi.hoisted(() => ({ listRunViews: vi.fn(), saveRunView: vi.fn(), deleteRunView: vi.fn() }));
vi.mock("../api/runViews", () => views);
const conv = vi.hoisted(() => ({ convertRunValues: vi.fn(), probeRunConversions: vi.fn() }));
vi.mock("../api/runConvert", () => conv);
const boards = vi.hoisted(() => ({ createRunFigure: vi.fn(), listBoards: vi.fn(), addToBoard: vi.fn(), plotPng: vi.fn() }));
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), createRunFigure: boards.createRunFigure, listBoards: boards.listBoards, addToBoard: boards.addToBoard }));
vi.mock("./runPicture", () => ({ plotPng: boards.plotPng }));

const D = "Axial agreement with Planck";
const RUNS: Run[] = [
  run(1, { block_size: "8" }, { metrics: [metric("am", 0.5, { unit: "rad", definition: D })] }),
  run(2, { block_size: "16" }, { metrics: [metric("am", 0.6, { unit: "rad", definition: D })] }),
  run(3, { block_size: "32" }, { metrics: [metric("am", 30, { unit: "deg", definition: D })] }),
  run(4, { block_size: "64" }),
];

function Page({ query = "" }: { query?: string }) {
  const [state, setState] = useState({ ...initialRunsView, query });
  return <RunsView runs={RUNS} jobs={[]} hostName={() => undefined} state={state} onState={(p) => setState((s) => ({ ...s, ...p }))} onOpenRun={() => undefined} />;
}
async function plotted(u: ReturnType<typeof userEvent.setup>) {
  await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
  await u.selectOptions(screen.getByLabelText("Against (x)"), "param:block_size");
}

beforeEach(() => {
  resetConvertibility();
  conv.probeRunConversions.mockImplementation(async (pairs: { from: string; to: string }[]) => pairs.map((p) => ({ from: p.from, to: p.to, refused: false, factor: 1 })));
  views.listRunViews.mockResolvedValue({ views: [], max: 100 });
  boards.plotPng.mockResolvedValue(new Blob(["png"]));
  boards.createRunFigure.mockResolvedValue({ id: 5, title: "am against block_size" });
  boards.listBoards.mockResolvedValue([{ id: 2, title: "Weekly" }]);
  boards.addToBoard.mockResolvedValue({});
});
afterEach(() => vi.clearAllMocks());

describe("saving the cross-run plot as a board figure", () => {
  it("offers it only when there is something drawn", async () => {
    render(<Page />);
    expect(screen.queryByRole("button", { name: "Save as board figure" })).toBeNull();
    await plotted(userEvent.setup());
    expect(screen.getByRole("button", { name: "Save as board figure" })).toBeTruthy();
  });

  it("saves the runs the filter matches, with the axes and the query, then puts the figure on a board", async () => {
    const u = userEvent.setup();
    render(<Page query="block_size:8..16" />);
    await plotted(u);
    await u.type(screen.getByLabelText("Figure title"), "Sweep am");
    await u.click(screen.getByRole("button", { name: "Save as board figure" }));
    expect(await screen.findByText(/Saved as the figure/)).toBeTruthy();
    expect(boards.createRunFigure).toHaveBeenCalledTimes(1);
    expect(boards.createRunFigure.mock.calls[0]![0]).toMatchObject({ title: "Sweep am", query: "block_size:8..16", y: "am", x: "block_size", xKind: "param", runIds: [1, 2], conversion: null });
    expect(boards.plotPng).toHaveBeenCalledTimes(1);
    await u.selectOptions(await screen.findByLabelText("Add to board"), "2");
    expect(boards.addToBoard).toHaveBeenCalledWith(2, 5);
  });

  it("carries the conversion that is shown, with the factor the worker gave", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: Math.PI / 180 }, results: [{ value: Math.PI / 6, uncertainty: null }], method: "plain unit conversion" });
    render(<Page />);
    await plotted(u);
    await u.click(await screen.findByRole("button", { name: "Convert to rad" }));
    await u.click(screen.getByRole("button", { name: "Save as board figure" }));
    await screen.findByText(/Saved as the figure/);
    expect(boards.createRunFigure.mock.calls[0]![0].conversion).toEqual({ to: "rad", entries: [{ runId: 3, factor: Math.PI / 180 }] });
    expect(boards.createRunFigure.mock.calls[0]![0].runIds).toEqual([1, 2, 3]);
  });

  it("says why when the figure cannot be saved", async () => {
    const u = userEvent.setup();
    boards.createRunFigure.mockRejectedValue(new Error("run 3 is not in the catalog"));
    render(<Page />);
    await plotted(u);
    await u.click(screen.getByRole("button", { name: "Save as board figure" }));
    expect((await screen.findByRole("alert")).textContent).toContain("run 3 is not in the catalog");
    expect(screen.queryByText(/Saved as the figure/)).toBeNull();
  });
});
