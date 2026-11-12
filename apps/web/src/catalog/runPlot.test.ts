import { describe, expect, it } from "vitest";
import { buildPlot, niceTicks } from "./runPlot";
import { metric, run } from "./runFixtures";

const am = (v: number | null, over = {}) => metric("am", v, { definition: "Axial agreement with Planck", ...over });

describe("cross-run plot: what is drawn together", () => {
  it("draws a result against a parameter, one point per run, in one panel", () => {
    const m = buildPlot([run(1, { b: "8" }, { metrics: [am(0.2)] }), run(2, { b: "16" }, { metrics: [am(0.3)] })], "am", { kind: "param", key: "b" }, "");
    expect(m.panels).toHaveLength(1);
    expect(m.xNumeric).toBe(true);
    expect(m.panels[0]!.series[0]!.points.map((p) => [p.x, p.y])).toEqual([[8, 0.2], [16, 0.3]]);
    expect(m.excluded).toEqual([]);
  });

  it("puts results in different units on their own panel, labelled not directly comparable, and does not convert unless asked", () => {
    const m = buildPlot(
      [run(1, { b: "8" }, { metrics: [am(0.5, { unit: "rad" })] }), run(2, { b: "16" }, { metrics: [am(0.6, { unit: "rad" })] }), run(3, { b: "32" }, { metrics: [am(30, { unit: "deg" })] })],
      "am", { kind: "param", key: "b" }, "",
    );
    expect(m.panels.map((p) => [p.unit, p.count, p.primary])).toEqual([["rad", 2, true], ["deg", 1, false]]);
    expect(m.panels[1]!.apart).toBe("unit differs: deg here, rad in the main panel");
    expect(m.panels[1]!.series[0]!.points[0]!.y).toBe(30); // as recorded, not 0.52
  });

  it("separates results defined differently, or with no definition, even in the same unit", () => {
    const m = buildPlot(
      [run(1, { b: "8" }, { metrics: [am(0.5)] }), run(2, { b: "16" }, { metrics: [am(0.6)] }), run(3, { b: "32" }, { metrics: [am(0.7, { definition: "Against the stars" })] }), run(4, { b: "64" }, { metrics: [am(0.8, { definition: "" })] })],
      "am", { kind: "param", key: "b" }, "",
    );
    expect(m.panels.map((p) => p.apart)).toEqual(["", "defined differently", "no definition stated"]);
  });
});

describe("cross-run plot: uncertainty bars", () => {
  const u = (kind: string, v = 0.02) => am(0.4, { uncertainty: v, uncertaintyKind: kind });
  it("keeps one kind of bar to a series and splits a series when the kinds differ", () => {
    const m = buildPlot([run(1, { b: "8" }, { metrics: [u("standard_error")] }), run(2, { b: "16" }, { metrics: [u("standard_deviation")] }), run(3, { b: "32" }, { metrics: [u("standard_error")] })], "am", { kind: "param", key: "b" }, "");
    const kinds = m.panels[0]!.series.map((s) => [s.barKind, s.points.length]);
    expect(kinds).toEqual([["standard_deviation", 1], ["standard_error", 2]]);
    expect(m.barKinds).toEqual(["standard_deviation", "standard_error"]);
  });
  it("draws no bar for an uncertainty whose kind is not stated, or is other, and counts what it withheld", () => {
    const m = buildPlot([run(1, { b: "8" }, { metrics: [u("")] }), run(2, { b: "16" }, { metrics: [u("other")] }), run(3, { b: "32" }, { metrics: [am(0.4)] })], "am", { kind: "param", key: "b" }, "");
    const pts = m.panels[0]!.series.flatMap((s) => s.points);
    expect(pts.every((p) => p.err === null)).toBe(true);
    expect(m.barKinds).toEqual([]);
    expect(m.barsWithheld).toBe(2); // the third has no uncertainty at all: nothing was withheld
  });
});

describe("cross-run plot: categories, status and omitted runs", () => {
  it("separates a categorical parameter into series with their own marker index, and one for runs that did not record it", () => {
    const m = buildPlot(
      [run(1, { b: "8", g: "sobel" }, { metrics: [am(0.2)] }), run(2, { b: "16", g: "central" }, { metrics: [am(0.3)] }), run(3, { b: "32" }, { metrics: [am(0.4)] })],
      "am", { kind: "param", key: "b" }, "g",
    );
    expect(m.categories).toEqual(["central", "sobel", "not recorded"]);
    expect(m.panels[0]!.series.map((s) => [s.category, s.categoryIndex])).toEqual([["central", 0], ["sobel", 1], ["not recorded", 2]]);
  });
  it("marks a result inferred by a rule, apart from one the run stated", () => {
    const inferred = am(0.3, { status: "inferred" as never });
    const m = buildPlot([run(1, { b: "8" }, { metrics: [am(0.2)] }), run(2, { b: "16" }, { metrics: [inferred] })], "am", { kind: "param", key: "b" }, "");
    const pts = m.panels[0]!.series.flatMap((s) => s.points);
    expect(pts.map((p) => p.inferred).sort()).toEqual([false, true]);
    expect(m.anyInferred && m.anyDeclared).toBe(true);
  });
  it("lists a run it cannot place with the reason instead of dropping it", () => {
    const m = buildPlot([run(1, { b: "8" }, { metrics: [am(0.2)] }), run(2, {}, { metrics: [am(0.3)] }), run(3, { b: "8" }, { metrics: [am(null)] }), run(4, { b: "8" })], "am", { kind: "param", key: "b" }, "");
    expect(m.excluded.map((x) => [x.runId, x.reason])).toEqual([[2, "b not recorded"], [3, "am not recorded"], [4, "am not recorded"]]);
  });
  it("plots against another result only for runs recording it in one unit and definition", () => {
    const x = (v: number, over = {}) => metric("planck", v, { definition: "Planck AM", unit: "", ...over });
    const m = buildPlot(
      [run(1, {}, { metrics: [am(0.2), x(0.5)] }), run(2, {}, { metrics: [am(0.3), x(0.6)] }), run(3, {}, { metrics: [am(0.4), x(30, { unit: "deg" })] })],
      "am", { kind: "metric", key: "planck" }, "",
    );
    expect(m.panels[0]!.series.flatMap((s) => s.points).map((p) => p.runId)).toEqual([1, 2]);
    expect(m.excluded.map((e) => e.runId)).toEqual([3]);
    expect(m.excluded[0]!.reason).toMatch(/deg.*not directly comparable/);
  });
  it("uses an ordinal axis for a parameter that is not a number", () => {
    const m = buildPlot([run(1, { g: "sobel" }, { metrics: [am(0.2)] }), run(2, { g: "central" }, { metrics: [am(0.3)] })], "am", { kind: "param", key: "g" }, "");
    expect(m.xNumeric).toBe(false);
    expect(m.xCategories).toEqual(["central", "sobel"]);
  });
});

describe("niceTicks", () => {
  it("covers the range with round steps", () => {
    expect(niceTicks(0.12, 0.83, 4)).toEqual([0, 0.2, 0.4, 0.6, 0.8, 1]);
    expect(niceTicks(-3, 140, 5)).toEqual([-50, 0, 50, 100, 150]);
  });
  it("opens up a single value", () => {
    const t = niceTicks(5, 5);
    expect(t[0]!).toBeLessThan(5);
    expect(t[t.length - 1]!).toBeGreaterThan(5);
  });
});
