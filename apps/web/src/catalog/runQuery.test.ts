import { describe, expect, it } from "vitest";
import { evaluateRuns, matchRunQuery, parseRunQuery, queriedKeys } from "./runQuery";
import type { RunContext } from "./runQuery";
import { metric, run } from "./runFixtures";

const none: RunContext = { status: "", host: "" };

describe("run query: ranges", () => {
  const r = run(1, { block_size: "32", gradient_method: "central_checked", smoothing: "0.5" }, { metrics: [metric("am", 0.344, { unit: "rad" })] });
  it("tests an inclusive range on a parameter, either end open", () => {
    expect(matchRunQuery(r, "block_size:16..64")).toBe(true);
    expect(matchRunQuery(r, "block_size:32..32")).toBe(true);
    expect(matchRunQuery(r, "block_size:33..64")).toBe(false);
    expect(matchRunQuery(r, "block_size:..31")).toBe(false);
    expect(matchRunQuery(r, "block_size:..32")).toBe(true);
    expect(matchRunQuery(r, "block_size:32..")).toBe(true);
    expect(matchRunQuery(r, "block_size=16..64")).toBe(true);
    expect(matchRunQuery(r, "smoothing:0.25..0.75")).toBe(true);
  });
  it("tests a range on a result, and a single value with a colon", () => {
    expect(matchRunQuery(r, "am:0.3..0.4")).toBe(true);
    expect(matchRunQuery(r, "am:0.35..0.4")).toBe(false);
    expect(matchRunQuery(r, "block_size:32")).toBe(true);
    expect(matchRunQuery(r, "gradient_method:central_checked")).toBe(true);
    expect(matchRunQuery(r, "gradient_method:sobel")).toBe(false);
  });
  it("refuses a range that cannot be read or runs backwards instead of matching everything", () => {
    for (const w of ["block_size:64..16", "block_size:a..b", "block_size:..", "block_size:", "block_size>", "block_size>x"]) expect(matchRunQuery(r, w), w).toBe(false);
  });
  it("narrows with every word", () => {
    expect(matchRunQuery(r, "block_size:16..64 gradient_method:central_checked am>0.3")).toBe(true);
    expect(matchRunQuery(r, "block_size:16..64 am>0.4")).toBe(false);
  });
});

describe("run query: units on result comparisons", () => {
  const rad = run(1, {}, { metrics: [metric("am", 0.5, { unit: "rad" })] });
  const deg = run(2, {}, { metrics: [metric("am", 30, { unit: "deg" })] });
  const bare = run(3, {}, { metrics: [metric("am", 0.5)] });
  it("compares only results recorded in the unit the word names", () => {
    expect(matchRunQuery(rad, "am>0.3rad")).toBe(true);
    expect(matchRunQuery(rad, "am<0.3rad")).toBe(false);
    expect(matchRunQuery(deg, "am>0.3rad")).toBe(false); // 30 deg is not converted, and not compared as 30 rad
    expect(matchRunQuery(bare, "am>0.3rad")).toBe(false); // no stated unit is not rad
    expect(matchRunQuery(rad, "am>0.3")).toBe(true); // no unit in the word: the number as recorded
    expect(matchRunQuery(deg, "am>0.3")).toBe(true);
  });
  it("reads exponents, units that start with e, a space inside quotes, and a range with the unit at its end", () => {
    const v = run(4, {}, { metrics: [metric("lambda", 1500, { unit: "eV" }), metric("vel", 12, { unit: "km / s" })] });
    expect(matchRunQuery(v, "lambda>1e3eV")).toBe(true);
    expect(matchRunQuery(v, 'vel>"5 km / s"')).toBe(true);
    expect(matchRunQuery(v, 'vel:10..20"km / s"')).toBe(true);
    expect(matchRunQuery(v, "vel:10..20km")).toBe(false);
  });
  it("says a different unit apart from a failed comparison", () => {
    const ev = evaluateRuns([rad, deg, bare], "am>0.3rad", () => none);
    expect(ev.matched.map((x) => x.id)).toEqual([1]);
    expect(ev.unitDiffers.map((x) => x.run.id)).toEqual([2, 3]);
  });
  it("does not give a parameter a unit", () => {
    const p = run(5, { ker: "8" });
    expect(matchRunQuery(p, "ker>3deg")).toBe(false);
    expect(evaluateRuns([p], "ker>3deg", () => none).unitDiffers).toHaveLength(1);
  });
});

describe("run query: dates, status and host", () => {
  const r = run(1, {}, { generatedAt: "2026-08-01T23:30:00Z", importedAt: "2026-08-03T01:00:00Z" });
  it("tests the UTC day of when a run was generated or imported", () => {
    expect(matchRunQuery(r, "generated:2026-08-01")).toBe(true);
    expect(matchRunQuery(r, "generated>=2026-08-01")).toBe(true);
    expect(matchRunQuery(r, "generated>2026-08-01")).toBe(false);
    expect(matchRunQuery(r, "generated:2026-07-15..2026-08-31")).toBe(true);
    expect(matchRunQuery(r, "generated:..2026-07-31")).toBe(false);
    expect(matchRunQuery(r, "imported<=2026-08-02")).toBe(false);
    expect(matchRunQuery(r, "imported:2026-08-03")).toBe(true);
    expect(matchRunQuery(r, "generated:yesterday")).toBe(false);
  });
  it("does not match a run with no date, and reports it as not recorded", () => {
    const u = run(2, {}, { generatedAt: "" });
    expect(matchRunQuery(u, "generated>=2026-01-01")).toBe(false);
    expect(evaluateRuns([u], "generated>=2026-01-01", () => none).notRecorded).toHaveLength(1);
    expect(evaluateRuns([u], "generated<2026-01-01", () => none).matched).toHaveLength(0);
  });
  it("tests status and host from the context; a run that records neither never matches", () => {
    const ctx: RunContext = { status: "completed", host: "Cluster" };
    expect(matchRunQuery(r, "status:completed", ctx)).toBe(true);
    expect(matchRunQuery(r, "status:failed", ctx)).toBe(false);
    expect(matchRunQuery(r, "host:cluster", ctx)).toBe(true);
    expect(matchRunQuery(r, "status:completed host:cluster", ctx)).toBe(true);
    expect(matchRunQuery(r, "status:completed")).toBe(false);
    expect(evaluateRuns([r], "status:completed", () => none).notRecorded).toHaveLength(1);
  });
});

describe("run query: absence is not a value", () => {
  const has = run(1, { ker: "8" }, { metrics: [metric("am", 0.4), metric("zero", 0), metric("lost", null)] });
  const lacks = run(2, { ker: "8" });
  it("separates a run that does not record what a word asks about from one that fails it", () => {
    const ev = evaluateRuns([has, lacks, run(3, { ker: "4" })], "ker=8 am>0.3", () => none);
    expect(ev.matched.map((x) => x.id)).toEqual([1]);
    expect(ev.notRecorded.map((x) => [x.run.id, x.words])).toEqual([[2, ["am>0.3"]]]);
  });
  it("a run that fails one word outright is not listed as not recorded for another", () => {
    const ev = evaluateRuns([run(1, { ker: "4" })], "ker=8 am>0.3", () => none);
    expect(ev.matched).toHaveLength(0);
    expect(ev.notRecorded).toHaveLength(0);
  });
  it("keeps a recorded zero and treats an unrecorded result as missing", () => {
    expect(matchRunQuery(has, "zero=0")).toBe(true);
    expect(matchRunQuery(has, "lost=0")).toBe(false);
    expect(evaluateRuns([has], "lost=0", () => none).notRecorded).toHaveLength(1);
  });
  it("hides archived runs unless the query asks for them", () => {
    const a = run(9, {}, { archived: true });
    expect(evaluateRuns([a], "", () => none).matched).toHaveLength(0);
    expect(evaluateRuns([a], "archived:yes", () => none).matched).toHaveLength(1);
  });
});

describe("run query: parsing", () => {
  it("reads quoted words as one and leaves archived out of the conditions", () => {
    expect(parseRunQuery('host:"my cluster" archived:all lmc').map((c) => c.kind)).toEqual(["host", "text"]);
  });
  it("lists the parameters and results a query asks about, for the table to show", () => {
    expect(queriedKeys("block_size:16..64 am>0.3 status:completed generated>=2026-01-01 block_size<100 lmc")).toEqual(["block_size", "am"]);
  });
});
