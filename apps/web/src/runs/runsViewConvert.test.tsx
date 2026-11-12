import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Run } from "../api/runs";
import { metric, run } from "../catalog/runFixtures";
import { RunsView } from "./RunsView";
import { initialRunsView } from "./runsViewState";

const views = vi.hoisted(() => ({ listRunViews: vi.fn(), saveRunView: vi.fn(), deleteRunView: vi.fn() }));
vi.mock("../api/runViews", () => views);
const conv = vi.hoisted(() => ({ convertRunValues: vi.fn() }));
vi.mock("../api/runConvert", () => conv);

const D = "Axial agreement with Planck";
const RUNS: Run[] = [
  run(1, { block_size: "8" }, { metrics: [metric("am", 0.5, { unit: "rad", definition: D })] }),
  run(2, { block_size: "16" }, { metrics: [metric("am", 0.6, { unit: "rad", definition: D })] }),
  run(3, { block_size: "32" }, { metrics: [metric("am", 30, { unit: "deg", definition: D, uncertainty: 1, uncertaintyKind: "standard_error" })] }),
];

function Page() {
  const [state, setState] = useState(initialRunsView);
  return <RunsView runs={RUNS} jobs={[]} hostName={() => undefined} state={state} onState={(p) => setState((s) => ({ ...s, ...p }))} onOpenRun={() => undefined} />;
}

async function plotted(u: ReturnType<typeof userEvent.setup>) {
  await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
  await u.selectOptions(screen.getByLabelText("Against (x)"), "param:block_size");
}
const panels = () => screen.getByTestId("runs-plot").querySelectorAll("[data-panel]");

beforeEach(() => views.listRunViews.mockResolvedValue({ views: [], max: 100 }));
afterEach(() => vi.clearAllMocks());

describe("cross-run plot: unit conversion", () => {
  it("offers the conversion, asks the worker, merges the converted run into the main panel and marks it", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: Math.PI / 180 }, results: [{ value: Math.PI / 6, uncertainty: Math.PI / 180 }], method: "plain unit conversion" });
    render(<Page />);
    await plotted(u);
    expect(panels()).toHaveLength(2);
    await u.click(screen.getByRole("button", { name: "Convert to rad" }));
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
    await u.click(screen.getByRole("button", { name: "Convert to rad" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/Not converted: "deg" and "rad" measure different things/);
    expect(screen.getByTestId("runs-plot").querySelectorAll("[data-converted]")).toHaveLength(0);
    expect(panels()).toHaveLength(2);
  });

  it("changing the result drops the conversion", async () => {
    const u = userEvent.setup();
    conv.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: Math.PI / 180 }, results: [{ value: Math.PI / 6, uncertainty: null }], method: "m" });
    render(<Page />);
    await plotted(u);
    await u.click(screen.getByRole("button", { name: "Convert to rad" }));
    await u.selectOptions(screen.getByLabelText("Result (y)"), "");
    await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
    expect(screen.getByTestId("runs-plot").querySelectorAll("[data-converted]")).toHaveLength(0);
  });
});
