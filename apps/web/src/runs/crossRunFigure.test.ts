import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CrossRunDescription, Figure } from "../api/boards";
import type { Run } from "../api/runs";
import { buildPlot } from "../catalog/runPlot";
import type { Conversion } from "../catalog/runPlot";
import { metric, run } from "../catalog/runFixtures";

const api = vi.hoisted(() => ({ createRunFigure: vi.fn(), listRuns: vi.fn(), listJobs: vi.fn(), listHosts: vi.fn(), convertRunValues: vi.fn(), plotPng: vi.fn() }));
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), createRunFigure: api.createRunFigure }));
vi.mock("../api/runs", async (orig) => ({ ...(await orig<typeof import("../api/runs")>()), listRuns: api.listRuns }));
vi.mock("../api/jobs", async (orig) => ({ ...(await orig<typeof import("../api/jobs")>()), listJobs: api.listJobs }));
vi.mock("../api/hosts", async (orig) => ({ ...(await orig<typeof import("../api/hosts")>()), listHosts: api.listHosts }));
vi.mock("../api/runConvert", () => ({ convertRunValues: api.convertRunValues, probeRunConversions: vi.fn() }));
vi.mock("./runPicture", () => ({ plotPng: api.plotPng }));
const { diffLines, diffPlot, drawnRunIds, figureRequest, footerOf, redrawCrossRun, saveRunFigure } = await import("./crossRunFigure");

const D = "Axial agreement with Planck";
const f = Math.PI / 180;
const am = (v: number, unit: string, over = {}) => metric("am", v, { unit, definition: D, uncertainty: 0.01, uncertaintyKind: "standard_error", ...over });
const RUNS: Run[] = [
  run(1, { block_size: "8" }, { metrics: [am(0.5, "rad")] }),
  run(2, { block_size: "16" }, { metrics: [am(0.6, "rad")] }),
  run(3, { block_size: "32" }, { metrics: [am(30, "deg", { uncertainty: 1 })] }),
];
const conv: Conversion = { y: "am", to: "rad", method: "plain unit conversion", entries: { 3: { definition: D, unit: "deg", value: 30, uncertainty: 1, factor: f, out: { value: 30 * f, uncertainty: f } } } };
const axis = { kind: "param" as const, key: "block_size" };

/** What the server writes for the plot of RUNS with run 3 converted: the recorded values beside the converted one. */
const saved = (over: Partial<CrossRunDescription> = {}): CrossRunDescription => ({
  schema: "cross-run/1", query: "block_size:8..64", y: "am", x: "block_size", xKind: "param", by: "",
  conversion: { to: "rad", method: "plain unit conversion", from: [{ unit: "deg", factor: f, count: 1 }] },
  runs: [
    { id: 1, name: "lmc_1", x: "8", y: { value: 0.5, unit: "rad", uncertainty: 0.01, uncertaintyKind: "standard_error", definition: D } },
    { id: 2, name: "lmc_2", x: "16", y: { value: 0.6, unit: "rad", uncertainty: 0.01, uncertaintyKind: "standard_error", definition: D } },
    { id: 3, name: "lmc_3", x: "32", y: { value: 30, unit: "deg", uncertainty: 1, uncertaintyKind: "standard_error", definition: D }, converted: { value: 30 * f, uncertainty: f } },
  ],
  ...over,
});

describe("saving a cross-run plot as a figure", () => {
  it("names the drawn runs, the axes and the conversion with the factor used, and nothing it did not draw", () => {
    const model = buildPlot(RUNS, "am", axis, "", conv);
    const req = figureRequest(model, { query: "block_size:8..64", y: "am", x: "block_size", xKind: "param", by: "" }, conv, { title: "T", caption: "" }, "PNG");
    expect(req).toMatchObject({ title: "T", previewPng: "PNG", query: "block_size:8..64", y: "am", x: "block_size", xKind: "param", runIds: [1, 2, 3] });
    expect(req.conversion).toEqual({ to: "rad", entries: [{ runId: 3, factor: f }] });
    expect(req).not.toHaveProperty("redrawnFrom");
  });

  it("sends no conversion when nothing drawn was converted, and the revision link for a redrawing", () => {
    const model = buildPlot(RUNS.slice(0, 2), "am", axis, "", conv); // run 3 is not in the plot: its entry is not sent
    const req = figureRequest(model, { query: "", y: "am", x: "block_size", xKind: "param", by: "" }, conv, { title: "T", caption: "c" }, "PNG", 9);
    expect(req.conversion).toBeNull();
    expect(req.redrawnFrom).toBe(9);
    expect(drawnRunIds(model)).toEqual([1, 2]);
  });

  it("draws the picture with the query and the count written on it, then saves", async () => {
    api.plotPng.mockResolvedValue(new Blob(["png"]));
    api.createRunFigure.mockResolvedValue({ id: 4, title: "T" });
    const model = buildPlot(RUNS, "am", axis, "", conv);
    await saveRunFigure(model, { query: "q", y: "am", x: "block_size", xKind: "param", by: "" }, conv, { title: "T", caption: "" });
    expect(api.plotPng).toHaveBeenCalledWith(model, footerOf("q", 3));
    expect(footerOf("  ", 3)).toBe("all runs · 3 runs");
    expect(api.createRunFigure).toHaveBeenCalledTimes(1);
    expect(api.createRunFigure.mock.calls[0]![0].runIds).toEqual([1, 2, 3]);
  });
});

describe("what changed since a cross-run plot was saved", () => {
  const draw = (runs: Run[], c: Conversion | null = conv) => {
    const matched = runs.filter((r) => !r.archived);
    return { model: buildPlot(matched, "am", axis, "", c), matched, runs };
  };

  it("says nothing changed for the same runs with the same values", () => {
    const { model, runs, matched } = draw(RUNS);
    const d = diffPlot(saved(), model, runs, matched);
    expect(d).toEqual({ added: [], gone: [], changed: [] });
    expect(diffLines(d)).toEqual([]);
  });

  it("names a run that now matches, and says why a run is no longer drawn: deleted, archived, filtered out, or no longer recording the result", () => {
    const more = [...RUNS.slice(0, 2), run(4, { block_size: "64" }, { metrics: [am(0.7, "rad")] })];
    let { model, runs, matched } = draw(more);
    let d = diffPlot(saved(), model, runs, matched);
    expect(d.added.map((x) => x.name)).toEqual(["lmc_4"]);
    expect(d.gone).toEqual([{ runId: 3, name: "lmc_3", what: "no longer in the catalog" }]); // deleted
    ({ model, runs, matched } = draw([RUNS[0]!, RUNS[1]!, { ...RUNS[2]!, archived: true }]));
    expect(diffPlot(saved(), model, runs, matched).gone[0]!.what).toBe("archived");
    matched = [RUNS[0]!, RUNS[1]!];
    model = buildPlot(matched, "am", axis, "", conv);
    expect(diffPlot(saved(), model, RUNS, matched).gone[0]!.what).toBe("no longer matches the filter");
    const blank = [RUNS[0]!, RUNS[1]!, run(3, { block_size: "32" }, { metrics: [] })];
    ({ model, runs, matched } = draw(blank));
    expect(diffPlot(saved(), model, runs, matched).gone[0]!.what).toBe("am not recorded");
    d = diffPlot(saved(), buildPlot(more, "am", axis, "", conv), more, more);
    expect(diffLines(d).join(" ")).toMatch(/1 run drawn in the saved figure is not drawn now: lmc_3 \(no longer in the catalog\)\..*1 run now matches that did not.*lmc_4/);
  });

  it("reports a changed number, unit, definition, x or conversion, run by run", () => {
    const edited = [
      run(1, { block_size: "8" }, { metrics: [am(0.55, "rad")] }), // value
      run(2, { block_size: "20" }, { metrics: [am(0.6, "rad", { definition: "Another" })] }), // x and definition
      run(3, { block_size: "32" }, { metrics: [am(30, "deg", { uncertainty: 1 })] }), // same, converted
    ];
    const { model, runs, matched } = draw(edited);
    const d = diffPlot(saved(), model, runs, matched);
    expect(d.changed.find((x) => x.runId === 1)!.what).toBe("am 0.5 rad → 0.55 rad");
    expect(d.changed.find((x) => x.runId === 2)!.what).toBe("its definition; block_size 16 → 20");
    expect(d.changed.find((x) => x.runId === 3)).toBeUndefined();
    // without a conversion the same run is a run that is no longer converted
    const plain = draw(RUNS, null);
    expect(diffPlot(saved(), plain.model, plain.runs, plain.matched).changed.find((x) => x.runId === 3)!.what).toBe("no longer converted");
    // a text number is the same number however it is written
    const x = draw([run(1, { block_size: "8.0" }, { metrics: [am(0.5, "rad")] }), RUNS[1]!, RUNS[2]!]);
    expect(diffPlot(saved(), x.model, x.runs, x.matched).changed).toEqual([]);
  });
});

describe("Draw again for a cross-run plot", () => {
  const fig = { id: 7, title: "am over the sweep", caption: "cap", scene: saved() } as unknown as Figure;
  beforeEach(() => {
    api.listJobs.mockResolvedValue([]);
    api.listHosts.mockResolvedValue([]);
    api.plotPng.mockResolvedValue(new Blob(["png"]));
    api.createRunFigure.mockResolvedValue({ id: 8, title: "am over the sweep" });
    api.convertRunValues.mockResolvedValue({ refused: false, to: "rad", factors: { deg: f }, results: [{ value: 30 * f, uncertainty: f }], method: "plain unit conversion" });
  });
  afterEach(() => vi.clearAllMocks());

  it("reads the runs again, asks the worker for the conversion again, and says the same when nothing changed", async () => {
    api.listRuns.mockResolvedValue(RUNS);
    const r = await redrawCrossRun(fig);
    expect(api.listRuns).toHaveBeenCalledWith({ archived: "all" });
    expect(api.convertRunValues).toHaveBeenCalledWith("rad", [{ value: 30, uncertainty: 1, from: "deg" }]); // never a stored factor
    expect(r.kind).toBe("cross-run");
    expect(r.dataChanged).toBe(false);
    expect(r.notes).toEqual(["The same runs with the same recorded values as when the figure was saved."]);
    expect(r.svg).toBeNull();
    expect(api.plotPng.mock.calls[0]![1]).toBe("query: block_size:8..64 · 3 runs"); // no time, no selection: the same runs give the same pixels
  });

  it("announces added, gone and changed runs before anything is saved, and saves a linked revision only on request", async () => {
    api.listRuns.mockResolvedValue([run(1, { block_size: "8" }, { metrics: [am(0.55, "rad")] }), RUNS[1]!, run(4, { block_size: "64" }, { metrics: [am(0.7, "rad")] })]);
    const r = await redrawCrossRun(fig);
    expect(r.dataChanged).toBe(true);
    expect(r.notes.join("\n")).toMatch(/lmc_3 \(no longer in the catalog\)/);
    expect(r.notes.join("\n")).toMatch(/now match.* lmc_4/);
    expect(r.notes.join("\n")).toMatch(/lmc_1 \(am 0.5 rad → 0.55 rad\)/);
    expect(api.createRunFigure).not.toHaveBeenCalled();
    await r.save();
    const req = api.createRunFigure.mock.calls[0]![0];
    expect(req).toMatchObject({ redrawnFrom: 7, runIds: [1, 2, 4], title: "am over the sweep", caption: "cap", conversion: null });
  });

  it("does not draw when the worker refuses the conversion the figure used", async () => {
    api.listRuns.mockResolvedValue(RUNS);
    api.convertRunValues.mockResolvedValue({ refused: true, reason: "different dimensions" });
    await expect(redrawCrossRun(fig)).rejects.toThrow(/refused now, so it is not drawn: different dimensions/);
    expect(api.plotPng).not.toHaveBeenCalled();
  });

  it("refuses to draw nothing", async () => {
    api.listRuns.mockResolvedValue([run(1, { block_size: "8" }, { metrics: [] })]);
    await expect(redrawCrossRun(fig)).rejects.toThrow(/nothing to draw/);
  });
});
