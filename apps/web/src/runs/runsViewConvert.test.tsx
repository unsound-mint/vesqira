import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import type { Run } from "../api/runs";
import { metric, run } from "../catalog/runFixtures";
import { RunsView } from "./RunsView";
import { resetConvertibility } from "./useConvertibility";
import { initialRunsView } from "./runsViewState";

const views = vi.hoisted(() => ({ listRunViews: vi.fn(), saveRunView: vi.fn(), deleteRunView: vi.fn() }));
vi.mock("../api/runViews", () => views);
const conv = vi.hoisted(() => ({ convertRunValues: vi.fn(), probeRunConversions: vi.fn() }));
vi.mock("../api/runConvert", () => conv);

const D = "Axial agreement with Planck";
const RUNS: Run[] = [
  run(1, { block_size: "8" }, { metrics: [metric("am", 0.5, { unit: "rad", definition: D })] }),
  run(2, { block_size: "16" }, { metrics: [metric("am", 0.6, { unit: "rad", definition: D })] }),
  run(3, { block_size: "32" }, { metrics: [metric("am", 30, { unit: "deg", definition: D, uncertainty: 1, uncertaintyKind: "standard_error" })] }),
];

function Page({ runs = RUNS }: { runs?: Run[] }) {
  const [state, setState] = useState(initialRunsView);
  return <RunsView runs={runs} jobs={[]} hostName={() => undefined} state={state} onState={(p) => setState((s) => ({ ...s, ...p }))} onOpenRun={() => undefined} />;
}

async function plotted(u: ReturnType<typeof userEvent.setup>) {
  await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
  await u.selectOptions(screen.getByLabelText("Against (x)"), "param:block_size");
}
const panels = () => screen.getByTestId("runs-plot").querySelectorAll("[data-panel]");

const yes = (from: string, to: string) => ({ from, to, refused: false, factor: 1 });
const no = (from: string, to: string, reason: string) => ({ from, to, refused: true, reason });
const button = () => screen.queryByRole("button", { name: /^Convert to / });

beforeEach(() => {
  resetConvertibility(); // the session cache would carry one test's answers into the next
  views.listRunViews.mockResolvedValue({ views: [], max: 100 });
  conv.probeRunConversions.mockImplementation(async (pairs: { from: string; to: string }[]) => pairs.map((p) => yes(p.from, p.to)));
});
afterEach(() => vi.clearAllMocks());

describe("cross-run plot: unit conversion", () => {
  it("offers the conversion, asks the worker, merges the converted run into the main panel and marks it", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: Math.PI / 180 }, results: [{ value: Math.PI / 6, uncertainty: Math.PI / 180 }], method: "plain unit conversion" });
    render(<Page />);
    await plotted(u);
    expect(panels()).toHaveLength(2);
    await u.click(await screen.findByRole("button", { name: "Convert to rad" }));
    expect(conv.convertRunValues).toHaveBeenCalledWith("rad", [{ value: 30, uncertainty: 1, from: "deg" }]); // the recorded numbers, in the unit they were recorded in
    const svg = screen.getByTestId("runs-plot");
    expect(panels()).toHaveLength(1);
    expect(svg.querySelectorAll('[data-converted="deg"]')).toHaveLength(1);
    expect(within(svg as unknown as HTMLElement).getByText(/includes runs converted from deg/)).toBeTruthy();
    expect(within(svg as unknown as HTMLElement).getByText(/1 run recorded in deg converted to rad/)).toBeTruthy();
    await u.click(screen.getByRole("button", { name: "Undo conversion" }));
    expect(panels()).toHaveLength(2);
  });

  it("says why when the worker refuses, and draws nothing converted", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: true, reason: '"deg" and "rad" measure different things' });
    render(<Page />);
    await plotted(u);
    await u.click(await screen.findByRole("button", { name: "Convert to rad" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/Not converted: "deg" and "rad" measure different things/);
    expect(screen.getByTestId("runs-plot").querySelectorAll("[data-converted]")).toHaveLength(0);
    expect(panels()).toHaveLength(2);
  });

  it("changing the result drops the conversion", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: Math.PI / 180 }, results: [{ value: Math.PI / 6, uncertainty: null }], method: "m" });
    render(<Page />);
    await plotted(u);
    await u.click(await screen.findByRole("button", { name: "Convert to rad" }));
    await u.selectOptions(screen.getByLabelText("Result (y)"), "");
    await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
    expect(screen.getByTestId("runs-plot").querySelectorAll("[data-converted]")).toHaveLength(0);
  });

  it("asks which pairs convert in one request, and shows a button only for the pair that does", async () => {
    const u = userEvent.setup();
    render(<Page runs={[...RUNS, run(4, { block_size: "64" }, { metrics: [metric("am", 2, { unit: "K", definition: D })] })]} />);
    conv.probeRunConversions.mockImplementation(async () => [yes("deg", "rad"), no("K", "rad", '"K" and "rad" measure different things; converting needs a beam')]);
    await plotted(u);
    expect(await screen.findByRole("button", { name: "Convert to rad" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: /^Convert to / })).toHaveLength(1); // not one for K
    expect(conv.probeRunConversions).toHaveBeenCalledTimes(1);
    expect(conv.probeRunConversions.mock.calls[0]![0]).toEqual([{ from: "K", to: "rad" }, { from: "deg", to: "rad" }]);
    // the plot's own note keeps its words and adds the reason
    const k = screen.getByTestId("runs-plot").querySelector('[data-panel] [data-reason]');
    expect(k?.textContent).toMatch(/measure different things/);
    expect(screen.getByTestId("runs-plot").textContent).toMatch(/not directly comparable: unit differs: K here, rad in the main panel/);
    expect(screen.getByText(/1 run recorded am in K, not rad, and it is not converted: "K" and "rad" measure different things/)).toBeTruthy();
  });

  it("shows no button and no note while the answer is pending, and draws the plot meanwhile", async () => {
    const u = userEvent.setup();
    let answer!: (v: unknown) => void;
    conv.probeRunConversions.mockImplementation(() => new Promise((res) => { answer = res; }));
    render(<Page />);
    await plotted(u);
    expect(panels()).toHaveLength(2); // drawn without waiting
    expect(button()).toBeNull();
    expect(screen.queryByRole("group", { name: "Unit conversion" })).toBeNull();
    await act(async () => answer([yes("deg", "rad")]));
    expect(button()).not.toBeNull();
  });

  it("keeps the answer for the session: a second plot of the same pair does not ask again", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await plotted(u);
    await screen.findByRole("button", { name: "Convert to rad" });
    await u.selectOptions(screen.getByLabelText("Result (y)"), "");
    await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
    expect(await screen.findByRole("button", { name: "Convert to rad" })).toBeTruthy();
    expect(conv.probeRunConversions).toHaveBeenCalledTimes(1);
  });

  it("offers nothing when the check itself failed, and says it could not be checked", async () => {
    const u = userEvent.setup();
    conv.probeRunConversions.mockRejectedValue(new Error("down"));
    render(<Page />);
    await plotted(u);
    expect(await screen.findByText(/could not be checked/)).toBeTruthy();
    expect(button()).toBeNull();
  });
});
