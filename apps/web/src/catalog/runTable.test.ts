import { describe, expect, it } from "vitest";
import type { Job } from "../api/jobs";
import { cellOf, columnChoices, contextIndex, sortRuns } from "./runTable";
import { metric, run } from "./runFixtures";

const none = () => ({ status: "", host: "" });
const ids = (rs: { id: number }[]) => rs.map((r) => r.id);

describe("runs table: sorting", () => {
  const runs = [
    run(1, { block_size: "8" }, { metrics: [metric("am", 0.4)] }),
    run(2, {}, { metrics: [] }), // records neither
    run(3, { block_size: "16" }, { metrics: [metric("am", null)] }), // am present but not recorded
    run(4, { block_size: "128" }, { metrics: [metric("am", 0.1)] }),
    run(5, { block_size: "32" }, { metrics: [metric("am", 0)] }), // a recorded zero is a value
  ];
  it("sorts a parameter as numbers (128 after 16, not before) and puts runs without it last in either direction", () => {
    expect(ids(sortRuns(runs, "p:block_size", 1, none))).toEqual([1, 3, 5, 4, 2]);
    expect(ids(sortRuns(runs, "p:block_size", -1, none))).toEqual([4, 5, 3, 1, 2]);
  });
  it("sorts a result by value, a recorded zero first, and an unrecorded result with the missing ones, in the order they came", () => {
    expect(ids(sortRuns(runs, "m:am", 1, none))).toEqual([5, 4, 1, 2, 3]);
    expect(ids(sortRuns(runs, "m:am", -1, none))).toEqual([1, 4, 5, 2, 3]);
  });
  it("sorts by time, newest first when asked, and a run without a date last", () => {
    const dated = [run(1, {}, { importedAt: "2026-08-01T00:00:00Z", generatedAt: "" }), run(2, {}, { importedAt: "2026-08-03T00:00:00Z", generatedAt: "2026-07-01T00:00:00Z" }), run(3, {}, { importedAt: "2026-08-02T00:00:00Z", generatedAt: "2026-07-05T00:00:00Z" })];
    expect(ids(sortRuns(dated, "imported", -1, none))).toEqual([2, 3, 1]);
    expect(ids(sortRuns(dated, "generated", 1, none))).toEqual([2, 3, 1]);
    expect(ids(sortRuns(dated, "generated", -1, none))).toEqual([3, 2, 1]);
  });
  it("sorts a result recorded in two units by unit first, not as if they were one scale", () => {
    const mixed = [run(1, {}, { metrics: [metric("am", 30, { unit: "deg" })] }), run(2, {}, { metrics: [metric("am", 0.5, { unit: "rad" })] }), run(3, {}, { metrics: [metric("am", 0.1, { unit: "rad" })] })];
    expect(ids(sortRuns(mixed, "m:am", 1, none))).toEqual([1, 3, 2]); // deg, then rad 0.1, rad 0.5
  });
  it("does not change the order of runs that tie", () => {
    const ties = [run(7, { k: "1" }), run(3, { k: "1" }), run(5, { k: "1" })];
    expect(ids(sortRuns(ties, "p:k", -1, none))).toEqual([7, 3, 5]);
  });
});

describe("runs table: cells and status", () => {
  it("shows nothing recorded as missing, never 0 or empty text", () => {
    const r = run(1, { a: "" }, { generatedAt: "not a date", metrics: [metric("am", null)] });
    for (const c of ["p:a", "p:absent", "m:am", "m:absent", "generated", "status", "host"]) expect(cellOf(r, c, none()).kind, c).toBe("missing");
  });
  it("carries a result with its unit and keeps the metric for the uncertainty", () => {
    const r = run(1, {}, { metrics: [metric("am", 0.344, { unit: "rad", uncertainty: 0.02, uncertaintyKind: "standard_error" })] });
    const c = cellOf(r, "m:am", none());
    expect(c.kind === "num" && c.text).toBe("0.344 rad");
    expect(c.kind === "num" && c.metric?.uncertaintyKind).toBe("standard_error");
  });
  it("takes status and host from the job that made the run, by the run's job id or the job's run id; an imported run records neither", () => {
    const jobs = [{ id: 10, hostId: 2, state: "completed", runId: null }, { id: 11, hostId: 2, state: "failed", runId: 3 }] as unknown as Job[];
    const runs = [run(1, {}, { jobId: 10 }), run(2), run(3)];
    const ctx = contextIndex(runs, jobs, (id) => (id === 2 ? "cluster" : undefined));
    expect(ctx(runs[0]!)).toEqual({ status: "completed", host: "cluster" });
    expect(ctx(runs[1]!)).toEqual({ status: "", host: "" });
    expect(ctx(runs[2]!)).toEqual({ status: "failed", host: "cluster" });
  });
});

describe("runs table: column choices", () => {
  it("lists what occurs with how many runs record it, and counts only recorded results", () => {
    const c = columnChoices([run(1, { a: "1" }, { metrics: [metric("am", 1, { unit: "rad" }), metric("x", null)] }), run(2, { a: "2", b: "3" }, { metrics: [metric("am", 2, { unit: "deg" })] })]);
    expect(c.params).toEqual([{ key: "a", count: 2 }, { key: "b", count: 1 }]);
    expect(c.metrics.map((m) => [m.key, m.count, m.units.sort()])).toEqual([["am", 2, ["deg", "rad"]]]);
  });
});
