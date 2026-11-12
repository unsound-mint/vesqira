import { describe, expect, it } from "vitest";
import { buildPlot } from "./runPlot";
import type { Conversion } from "./runPlot";
import { csvHeader, runsCsv } from "./runExport";
import { metric, run } from "./runFixtures";

const DEF = "Axial agreement with Planck";
const am = (v: number | null, over = {}) => metric("am", v, { definition: DEF, ...over });
const f = Math.PI / 180;

describe("cross-run plot: a conversion the researcher asked for", () => {
  const rad = (v: number, over = {}) => am(v, { unit: "rad", ...over });
  const deg = (v: number, over = {}) => am(v, { unit: "deg", ...over });
  const runs = [
    run(1, { b: "8" }, { metrics: [rad(0.5, { uncertainty: 0.01, uncertaintyKind: "standard_error" })] }),
    run(2, { b: "16" }, { metrics: [rad(0.6)] }),
    run(3, { b: "32" }, { metrics: [deg(30, { uncertainty: 1, uncertaintyKind: "standard_error" })] }),
    run(4, { b: "64" }, { metrics: [am(0.7, { unit: "deg", definition: "Something else" })] }),
  ];
  const conv = (over = {}): Conversion => ({
    y: "am",
    to: "rad",
    method: "plain unit conversion",
    entries: { 3: { definition: DEF, unit: "deg", value: 30, uncertainty: 1, factor: f, out: { value: 30 * f, uncertainty: 1 * f } } },
    ...over,
  });

  it("offers a panel that differs only by unit, and not one defined differently or without a definition", () => {
    const m = buildPlot(runs, "am", { kind: "param", key: "b" }, "");
    expect(m.offers.map((o) => [o.unit, o.to, o.values.map((v) => v.runId)])).toEqual([["deg", "rad", [3]]]);
    const none = buildPlot([run(1, { b: "8" }, { metrics: [metric("am", 1, { unit: "rad" })] }), run(2, { b: "9" }, { metrics: [metric("am", 2, { unit: "deg" })] })], "am", { kind: "param", key: "b" }, "");
    expect(none.offers).toEqual([]); // no definition stated on either: not comparable, so never offered
  });

  it("without a conversion nothing merges, even between units that could be converted", () => {
    const m = buildPlot(runs, "am", { kind: "param", key: "b" }, "");
    expect(m.panels.map((p) => [p.unit, p.count])).toEqual([["rad", 2], ["deg", 1], ["deg", 1]]);
    expect(m.conversion).toBeNull();
  });

  it("puts converted points on the main panel, marks them, keeps what was recorded, and leaves a differently defined run apart", () => {
    const m = buildPlot(runs, "am", { kind: "param", key: "b" }, "", conv());
    const main = m.panels[0]!;
    expect(main.unit).toBe("rad");
    expect(main.count).toBe(3);
    const pts = main.series.flatMap((s) => s.points);
    const c = pts.find((p) => p.runId === 3)!;
    expect(c.y).toBeCloseTo(Math.PI / 6, 12);
    expect(c.err).toBeCloseTo(f, 12);
    expect(c.converted).toEqual({ fromUnit: "deg", value: 30, uncertainty: 1 });
    expect(pts.find((p) => p.runId === 1)!.converted).toBeUndefined();
    expect(main.convertedFrom).toEqual(["deg"]);
    expect(m.conversion).toEqual({ to: "rad", method: "plain unit conversion", from: [{ unit: "deg", factor: f, count: 1 }] });
    // run 4 has another definition: it is not merged, even though its unit is also deg
    expect(m.panels.slice(1).map((p) => [p.definition, p.count])).toEqual([["Something else", 1]]);
    expect(m.offers).toEqual([]);
    // the error bar kind of the converted point is the one it recorded
    expect(main.series.find((s) => s.points.some((p) => p.runId === 3))!.barKind).toBe("standard_error");
  });

  it("ignores an entry for a run whose recorded result has changed since", () => {
    const changed = runs.map((r) => (r.id === 3 ? run(3, { b: "32" }, { metrics: [deg(31, { uncertainty: 1, uncertaintyKind: "standard_error" })] }) : r));
    const m = buildPlot(changed, "am", { kind: "param", key: "b" }, "", conv());
    expect(m.panels[0]!.count).toBe(2);
    expect(m.conversion).toBeNull();
    expect(m.offers.map((o) => o.unit)).toEqual(["deg"]);
  });

  it("applies a conversion to its own result only", () => {
    expect(buildPlot(runs, "am", { kind: "param", key: "b" }, "", conv({ y: "other" })).conversion).toBeNull();
  });
});

describe("runs CSV with a unit conversion", () => {
  const ctx = () => ({ status: "", host: "" });
  const at = "2026-11-12T00:00:00.000Z";
  const runs = [
    run(1, {}, { metrics: [metric("am", 0.5, { unit: "rad", definition: DEF })] }),
    run(2, {}, { metrics: [metric("am", 30, { unit: "deg", definition: DEF, uncertainty: 1, uncertaintyKind: "standard_error" })] }),
  ];
  const conversion: Conversion = { y: "am", to: "rad", method: "plain unit conversion", entries: { 2: { definition: DEF, unit: "deg", value: 30, uncertainty: 1, factor: f, out: { value: 30 * f, uncertainty: f } } } };
  const lines = runsCsv(runs, ["m:am"], ctx, { query: "", total: 2, at, conversion }).split("\n");

  it("says what was converted and how", () => {
    expect(lines.some((l) => l.startsWith("# am: runs recorded in another unit were converted to rad"))).toBe(true);
  });
  it("keeps the recorded value and unit beside the converted ones and never overwrites them", () => {
    expect(lines.find((l) => l.startsWith("id,"))).toBe("id,name,am,am_unit,am_uncertainty,am_uncertainty_kind,am_n,am_status,am_converted,am_converted_unit,am_converted_uncertainty,am_conversion_factor");
    expect(lines.find((l) => l.startsWith("1,"))).toBe("1,lmc_1,0.5,rad,,,,declared,,,,"); // not converted: empty, not a copy
    expect(lines.find((l) => l.startsWith("2,"))).toBe(`2,lmc_2,30,deg,1,standard_error,,declared,${30 * f},rad,${f},${f}`);
  });
  it("is the old table when nothing was converted", () => {
    expect(csvHeader(["m:am"], null)).toEqual(csvHeader(["m:am"]));
    expect(runsCsv(runs, ["m:am"], ctx, { query: "", total: 2, at, conversion: { ...conversion, entries: {} } })).not.toContain("converted");
  });
});
